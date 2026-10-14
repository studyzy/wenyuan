package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ClaimNextOperation atomically claims the oldest claimable operation for a
// worker.
//
// The select uses SKIP LOCKED so two consumers polling at the same moment take
// different rows instead of blocking on each other, and the claim is written in
// the same statement through the UPDATE ... RETURNING, so a row cannot be
// claimed twice. A pending operation is claimable once its retry gate has
// passed; a processing operation is claimable when its claim is older than the
// lease, which is how a consumer that died mid-operation stops stranding its
// work. A non-positive lease marks every existing claim abandoned immediately,
// which is the same rule the in-memory backend applies.
//
// An operation whose serialization key another live claim holds is skipped. The
// key exists so two runs of the same work cannot proceed beside each other and
// leave whichever finished last winning; claiming both would defeat it. The
// check reads the same table the statement writes, which READ COMMITTED would
// let race, so the claim runs at SERIALIZABLE and a lost race is retried.
func (s *PostgresStore) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration) (Operation, error) {
	if workerID == "" {
		return Operation{}, ErrInvalidOperationID
	}
	leaseSeconds := lease.Seconds()
	// The candidate select is parenthesized: the claimable predicate is a
	// disjunction, and without the grouping a later conjunct would bind to only
	// its right-hand side.
	query := `WITH candidate AS (
	            SELECT id FROM ` + quoteIdent(s.schema) + `.async_operations
	            WHERE ((status = 'pending'
	                    AND (next_retry_at IS NULL OR next_retry_at <= now()))
	                OR (status = 'processing'
	                    AND ($2 <= 0
	                         OR claimed_at IS NULL
	                         OR claimed_at <= now() - make_interval(secs => $2))))
	              AND (serialization_key IS NULL
	                   OR NOT EXISTS (
	                        SELECT 1 FROM ` + quoteIdent(s.schema) + `.async_operations held
	                        WHERE held.serialization_key = ` + quoteIdent(s.schema) + `.async_operations.serialization_key
	                          AND held.status = 'processing'
	                          AND held.id <> ` + quoteIdent(s.schema) + `.async_operations.id
	                          AND ($2 <= 0
	                               OR held.claimed_at IS NULL
	                               OR held.claimed_at > now() - make_interval(secs => $2))))
	            ORDER BY created_at ASC, id ASC
	            FOR UPDATE SKIP LOCKED
	            LIMIT 1
	          )
	          UPDATE ` + quoteIdent(s.schema) + `.async_operations AS o
	          SET status = 'processing', worker_id = $1, claimed_at = now(), updated_at = now()
	          FROM candidate
	          WHERE o.id = candidate.id
	          RETURNING o.id, o.bank_id, COALESCE(o.operation_type,''), o.status,
	                    COALESCE(o.error_message,''), o.run, o.created_at, o.updated_at,
	                    o.completed_at, COALESCE(o.retry_count,0), o.next_retry_at,
	                    o.result_metadata, COALESCE(o.worker_id,''), o.claimed_at,
	                    COALESCE(o.task_payload, 'null'::jsonb), COALESCE(o.serialization_key,'')`
	for attempt := 0; ; attempt++ {
		op, err := s.claimOnce(ctx, query, workerID, leaseSeconds)
		if err == nil {
			return op, nil
		}
		if !errors.Is(err, errOperationNotFoundSentinel) {
			return Operation{}, err
		}
		if attempt >= claimSerializationAttempts {
			return Operation{}, ErrOperationNotFound
		}
	}
}

// claimSerializationAttempts bounds the retries of a claim that lost a
// serialization race, so a persistently contended key cannot spin.
const claimSerializationAttempts = 3

// errOperationNotFoundSentinel distinguishes an empty claim from a real error.
var errOperationNotFoundSentinel = errors.New("no claimable operation")

// claimOnce runs one claim attempt.
func (s *PostgresStore) claimOnce(ctx context.Context, query, workerID string, leaseSeconds float64) (Operation, error) {
	row := s.db.QueryRowContext(ctx, query, workerID, leaseSeconds)
	op, err := scanOperationWithClaim(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, errOperationNotFoundSentinel
		}
		// A serialization failure means another claim committed the key we
		// were deciding against. Retrying re-reads the committed state, which
		// is the point of the isolation level rather than a workaround for it.
		if isSerializationFailure(err) {
			return Operation{}, errOperationNotFoundSentinel
		}
		return Operation{}, err
	}
	return op, nil
}

// ReleaseOperationClaim returns a claimed operation to the claimable pool,
// clearing the claim so another consumer can pick it up immediately.
func (s *PostgresStore) ReleaseOperationClaim(ctx context.Context, opID, workerID string) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status = 'pending', worker_id = NULL, claimed_at = NULL, updated_at = now()
	          WHERE id = $1 AND worker_id = $2`
	result, err := s.db.ExecContext(ctx, query, opID, workerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrOperationConflict
	}
	return nil
}

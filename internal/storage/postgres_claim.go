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
func (s *PostgresStore) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration) (Operation, error) {
	if workerID == "" {
		return Operation{}, ErrInvalidOperationID
	}
	leaseSeconds := lease.Seconds()
	query := `WITH candidate AS (
	            SELECT id FROM ` + quoteIdent(s.schema) + `.async_operations
	            WHERE (status = 'pending'
	                   AND (next_retry_at IS NULL OR next_retry_at <= now()))
	               OR (status = 'processing'
	                   AND ($2 <= 0
	                        OR claimed_at IS NULL
	                        OR claimed_at <= now() - make_interval(secs => $2)))
	            ORDER BY created_at ASC
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
	                    COALESCE(o.task_payload, 'null'::jsonb)`
	row := s.db.QueryRowContext(ctx, query, workerID, leaseSeconds)
	op, err := scanOperationWithClaim(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, ErrOperationNotFound
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

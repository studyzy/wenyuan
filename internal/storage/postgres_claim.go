package storage

import (
	"context"
	"errors"
	"strconv"
	"strings"
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
func (s *PostgresStore) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration, opts ...ClaimOption) (Operation, error) {
	return s.claimNextOperation(ctx, workerID, lease, nil, opts...)
}

// ClaimNextOperationForTypes claims the oldest claimable operation whose type
// is in types. An empty list is equivalent to ClaimNextOperation.
func (s *PostgresStore) ClaimNextOperationForTypes(ctx context.Context, workerID string, lease time.Duration, types []string, opts ...ClaimOption) (Operation, error) {
	return s.claimNextOperation(ctx, workerID, lease, types, opts...)
}

func (s *PostgresStore) claimNextOperation(ctx context.Context, workerID string, lease time.Duration, types []string, opts ...ClaimOption) (Operation, error) {
	options := applyClaimOptions(opts)
	if options.hasReclaimLimit {
		if err := s.reapExhaustedRecoveries(ctx, lease, options.maxReclaims); err != nil {
			return Operation{}, err
		}
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Operation
		return zero0, schemaErr
	}
	if workerID == "" {
		return Operation{}, ErrInvalidOperationID
	}
	leaseSeconds := lease.Seconds()
	// The candidate select is parenthesized: the claimable predicate is a
	// disjunction, and without the grouping a later conjunct would bind to only
	// its right-hand side. The candidate's prior status and retry count ride
	// along, so reclaiming an abandoned claim counts against its budget.
	query := `WITH candidate AS (
	            SELECT operation_id, status, COALESCE(retry_count,0) AS retry_count FROM ` + quoteIdent(schema) + `.async_operations
	            WHERE ((status = 'pending'
	                    AND (next_retry_at IS NULL OR next_retry_at <= now()))
	                OR (status = 'processing'
	                    AND ($2 <= 0
	                         OR claimed_at IS NULL
	                         OR claimed_at <= now() - make_interval(secs => $2))))
	              AND ($3::text[] IS NULL OR operation_type = ANY($3))
	              AND task_payload IS NOT NULL
	              AND (serialization_key IS NULL
	                   OR NOT EXISTS (
	                        SELECT 1 FROM ` + quoteIdent(schema) + `.async_operations held
	                        WHERE held.serialization_key = ` + quoteIdent(schema) + `.async_operations.serialization_key
	                          AND held.status = 'processing'
	                          AND held.operation_id <> ` + quoteIdent(schema) + `.async_operations.operation_id
	                          AND ($2 <= 0
	                               OR held.claimed_at IS NULL
	                               OR held.claimed_at > now() - make_interval(secs => $2))))
	            -- Bank fairness: the candidate whose bank has the oldest pending work is
	            -- served first, so one bank's backlog cannot starve the others.
	            -- Within a bank the order stays oldest-first.
	            ORDER BY (SELECT MIN(pending.created_at)
	                      FROM ` + quoteIdent(schema) + `.async_operations pending
	                      WHERE pending.bank_id = ` + quoteIdent(schema) + `.async_operations.bank_id
	                        AND pending.status = 'pending'
	                        AND (pending.next_retry_at IS NULL OR pending.next_retry_at <= now())),
	                     created_at ASC, operation_id ASC
	            FOR UPDATE SKIP LOCKED
	            LIMIT 1
	          )
	          UPDATE ` + quoteIdent(schema) + `.async_operations AS o
	          SET status = 'processing', worker_id = $1, claimed_at = now(), updated_at = now(),
	              -- Recovering an abandoned claim spends one recovery attempt
	              -- (worker/poller.py:1511-1518); a fresh claim spends none.
	              retry_count = CASE WHEN candidate.status = 'processing'
	                                 THEN candidate.retry_count + 1 ELSE o.retry_count END
	          FROM candidate
	          WHERE o.operation_id = candidate.operation_id
	          RETURNING o.operation_id, o.bank_id, COALESCE(o.operation_type,''), o.status,
	                    COALESCE(o.error_message,''), o.created_at, o.updated_at,
	                    o.completed_at, COALESCE(o.retry_count,0), o.next_retry_at,
	                    o.result_metadata, COALESCE(o.worker_id,''), o.claimed_at,
	                    COALESCE(o.task_payload, 'null'::jsonb), COALESCE(o.serialization_key,'')`
	for attempt := 0; ; attempt++ {
		op, err := s.claimOnce(ctx, query, workerID, leaseSeconds, types)
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
func (s *PostgresStore) claimOnce(ctx context.Context, query, workerID string, leaseSeconds float64, types []string) (Operation, error) {
	return s.claimRow(ctx, query, workerID, leaseSeconds, nullableTagArray(types))
}

// claimRow runs one claim statement and scans its single row. An empty result
// and a lost serialization race both report errOperationNotFoundSentinel, which
// the caller retries or maps to ErrOperationNotFound.
func (s *PostgresStore) claimRow(ctx context.Context, query string, args ...any) (Operation, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Operation{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			if isSerializationFailure(err) {
				return Operation{}, errOperationNotFoundSentinel
			}
			return Operation{}, err
		}
		return Operation{}, errOperationNotFoundSentinel
	}
	op, err := scanOperationWithClaim(rows)
	if err != nil {
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET status = 'pending', worker_id = NULL, claimed_at = NULL, updated_at = now()
	          WHERE operation_id = $1 AND worker_id = $2`
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

// ConsolidationPriorityClaimer is the optional capability a backend implements
// when it can order consolidation claims by bank priority.
//
// It exists because one bank's backlog should not starve another's observation
// folding: an operator names the banks whose consolidation matters most, and
// the claim prefers them. A backend that cannot order this way is simply not
// asked; the consumer falls back to a plain consolidation claim, so the
// capability only ever adds a preference.
type ConsolidationPriorityClaimer interface {
	// ClaimConsolidationByPriority claims the oldest claimable consolidation
	// operation, preferring banks whose identifier matches a higher-priority
	// pattern. An empty priority map means "no preference configured". The
	// variadic options adjust lease-expiry recovery (WithReclaimLimit).
	ClaimConsolidationByPriority(ctx context.Context, workerID string, lease time.Duration, priority map[string]int, opts ...ClaimOption) (Operation, error)
}

// Both backends carry the capability; a store without it would fall back to the
// plain consolidation claim in the consumer.
var (
	_ ConsolidationPriorityClaimer = (*MemoryStore)(nil)
	_ ConsolidationPriorityClaimer = (*PostgresStore)(nil)
)

// ClaimConsolidationByPriority claims the oldest claimable consolidation
// operation, preferring banks whose identifier matches a higher-priority
// pattern.
//
// When a priority map is configured the claim runs as one statement: an
// expression ranks each candidate bank by the highest-priority specific pattern
// it matches (falling back to the catch-all, then to the reference's implicit
// tier of one), and the candidate with the highest rank and then the oldest
// work is taken. Every ordering the reference builds as separate tiered queries
// folds into that single expression, so a claim stays one round trip.
func (s *PostgresStore) ClaimConsolidationByPriority(ctx context.Context, workerID string, lease time.Duration, priority map[string]int, opts ...ClaimOption) (Operation, error) {
	options := applyClaimOptions(opts)
	if options.hasReclaimLimit {
		if err := s.reapExhaustedRecoveries(ctx, lease, options.maxReclaims); err != nil {
			return Operation{}, err
		}
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Operation
		return zero0, schemaErr
	}
	if len(priority) == 0 {
		return s.ClaimNextOperationForTypes(ctx, workerID, lease, []string{OperationTypeConsolidate}, opts...)
	}
	if workerID == "" {
		return Operation{}, ErrInvalidOperationID
	}
	leaseSeconds := lease.Seconds()
	qualified := quoteIdent(schema)
	rank := consolidationPriorityRank(priority)

	query := `WITH candidate AS (
	            SELECT operation_id, status, COALESCE(retry_count,0) AS retry_count FROM ` + qualified + `.async_operations
	            WHERE ((status = 'pending'
	                    AND (next_retry_at IS NULL OR next_retry_at <= now()))
	                OR (status = 'processing'
	                    AND ($2 <= 0
	                         OR claimed_at IS NULL
	                         OR claimed_at <= now() - make_interval(secs => $2))))
	              AND operation_type = ` + quoteLiteral(OperationTypeConsolidate) + `
	              AND task_payload IS NOT NULL
	              AND (serialization_key IS NULL
	                   OR NOT EXISTS (
	                        SELECT 1 FROM ` + qualified + `.async_operations held
	                        WHERE held.serialization_key = ` + qualified + `.async_operations.serialization_key
	                          AND held.status = 'processing'
	                          AND held.operation_id <> ` + qualified + `.async_operations.operation_id
	                          AND ($2 <= 0
	                               OR held.claimed_at IS NULL
	                               OR held.claimed_at > now() - make_interval(secs => $2))))
	            -- The configured bank priority ranks first, highest tier first;
	            -- within a tier the oldest work goes first, matching the
	            -- reference's per-tier created_at ordering.
	            ORDER BY ` + rank + ` DESC, created_at ASC, operation_id ASC
	            FOR UPDATE SKIP LOCKED
	            LIMIT 1
	          )
	          UPDATE ` + qualified + `.async_operations AS o
	          SET status = 'processing', worker_id = $1, claimed_at = now(), updated_at = now(),
	              -- Recovering an abandoned claim spends one recovery attempt
	              -- (worker/poller.py:1511-1518); a fresh claim spends none.
	              retry_count = CASE WHEN candidate.status = 'processing'
	                                 THEN candidate.retry_count + 1 ELSE o.retry_count END
	          FROM candidate
	          WHERE o.operation_id = candidate.operation_id
	          RETURNING o.operation_id, o.bank_id, COALESCE(o.operation_type,''), o.status,
	                    COALESCE(o.error_message,''), o.created_at, o.updated_at,
	                    o.completed_at, COALESCE(o.retry_count,0), o.next_retry_at,
	                    o.result_metadata, COALESCE(o.worker_id,''), o.claimed_at,
	                    COALESCE(o.task_payload, 'null'::jsonb), COALESCE(o.serialization_key,'')`
	for attempt := 0; ; attempt++ {
		op, err := s.claimRow(ctx, query, workerID, leaseSeconds)
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

// reapExhaustedRecoveries fails every abandoned 'processing' row whose recovery
// budget is spent, before the claim can offer it for another run: its lease
// expired and it has already been recovered limit times, so running it again
// would only crash-loop the worker (worker/poller.py:1527-1546, the second of
// the two recovery UPDATEs). Each failed row's batch parent is rolled up, one
// row at a time, exactly as the reference propagates its reclaimed children.
func (s *PostgresStore) reapExhaustedRecoveries(ctx context.Context, lease time.Duration, limit int) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET status = 'failed', worker_id = NULL, claimed_at = NULL,
	              error_message = $3, completed_at = now(), updated_at = now()
	          WHERE status = 'processing'
	            AND ($1 <= 0
	                 OR claimed_at IS NULL
	                 OR claimed_at <= now() - make_interval(secs => $1))
	            AND COALESCE(retry_count,0) >= $2
	          RETURNING bank_id, COALESCE(result_metadata,'{}'::jsonb)::text`
	rows, err := s.db.QueryContext(ctx, query, lease.Seconds(), limit, reclaimExhaustedMessage(limit))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	parents := make([][2]string, 0)
	for rows.Next() {
		var bankID, metadataRaw string
		if err := rows.Scan(&bankID, &metadataRaw); err != nil {
			return err
		}
		if parentID, _ := decodeJSONMap([]byte(metadataRaw))["parent_operation_id"].(string); parentID != "" {
			parents = append(parents, [2]string{bankID, parentID})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// The failing UPDATE has already committed, so the children stay failed
	// regardless; a roll-up lapse — a parent already promoted, or gone with its
	// bank — leaves the others to be rolled up, as the reference logs and moves
	// on rather than undoing the reaps.
	for _, parent := range parents {
		_, _, _ = s.FinalizeBatchParent(ctx, parent[0], parent[1])
	}
	return nil
}

// consolidationPriorityRank renders the SQL expression that ranks a candidate
// row's bank. A specific pattern (any key but "*") always outranks the
// catch-all, so a bank matching one takes that pattern's priority even when the
// catch-all is higher; a bank matching no specific pattern takes the catch-all,
// and one with nothing configured takes the reference's implicit tier of one.
//
// The patterns carry no bind parameters because the value is a small operator
// setting and the expression is built once per claim; each is rendered through
// quoteLiteral, so a pattern holding a quote is escaped rather than breaking the
// statement. "*" becomes SQL's "%", exactly as the reference converts it.
func consolidationPriorityRank(priority map[string]int) string {
	specific := make([]string, 0, len(priority))
	catchAll := 0
	hasCatchAll := false
	for pattern, value := range priority {
		if pattern == "*" {
			catchAll = value
			hasCatchAll = true
			continue
		}
		specific = append(specific, "("+quoteLiteral(strings.ReplaceAll(pattern, "*", "%"))+"::text, "+strconv.Itoa(value)+")")
	}
	fallback := "1"
	if hasCatchAll {
		fallback = strconv.Itoa(catchAll)
	}
	if len(specific) == 0 {
		return fallback
	}
	return "COALESCE((SELECT MAX(pri.pri) FROM (VALUES " + strings.Join(specific, ", ") + ") AS pri(pat, pri) WHERE bank_id LIKE pri.pat), " + fallback + ")"
}

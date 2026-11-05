package storage

import (
	"context"
)

// PendingRetainPeers returns the queued retain operations waiting behind a
// claimed operation on the same bank and serialization key: pending rows with
// a payload whose retry gate has passed, oldest first with the identifier
// breaking ties — the order the document accumulates in.
func (s *PostgresStore) PendingRetainPeers(ctx context.Context, bankID, serializationKey string, limit int) ([]Operation, error) {
	if limit <= 0 || serializationKey == "" {
		return nil, nil
	}
	schema, err := s.schemaFor(ctx)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + operationColumns + ` FROM ` + quoteIdent(schema) + `.async_operations
	          WHERE status = 'pending'
	            AND task_payload IS NOT NULL
	            AND operation_type = ` + quoteLiteral(OperationTypeRetain) + `
	            AND bank_id = $1
	            AND serialization_key = $2
	            AND (next_retry_at IS NULL OR next_retry_at <= now())
	          ORDER BY created_at, operation_id
	          LIMIT $3`
	rows, err := s.db.QueryContext(ctx, query, bankID, serializationKey, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	peers := make([]Operation, 0, limit)
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		peers = append(peers, op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return peers, nil
}

// ClaimPendingOperations claims the named operations that are still pending
// and returns the identifiers it took, in the order they were given. One
// statement claims every still-pending row atomically; a name that was settled
// or cancelled while the caller was planning is skipped, so a caller that
// folded only the operations it actually holds never executes work it does not
// own.
func (s *PostgresStore) ClaimPendingOperations(ctx context.Context, workerID string, ids []string) ([]string, error) {
	if workerID == "" {
		return nil, ErrInvalidOperationID
	}
	if len(ids) == 0 {
		return nil, nil
	}
	schema, err := s.schemaFor(ctx)
	if err != nil {
		return nil, err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET status = 'processing', worker_id = $1, claimed_at = now(), updated_at = now()
	          WHERE operation_id = ANY($2::uuid[]) AND status = 'pending'
	          RETURNING operation_id`
	rows, err := s.db.QueryContext(ctx, query, workerID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	taken := make(map[string]struct{}, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		taken[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	claimed := make([]string, 0, len(taken))
	for _, id := range ids {
		if _, ok := taken[id]; ok {
			claimed = append(claimed, id)
		}
	}
	return claimed, nil
}

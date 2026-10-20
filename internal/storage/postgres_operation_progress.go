package storage

import (
	"context"
	"encoding/json"
)

// RecordOperationProgress merges a progress snapshot into the operation's
// result metadata, the way the reference does: a top-level `||` merge, so the
// `progress` member is replaced while siblings such as `is_parent` survive, and
// `updated_at` moves with it. The reference's own write is the same statement
// (`memory_engine.py:5170-5179`).
func (s *PostgresStore) RecordOperationProgress(ctx context.Context, opID string, snapshot map[string]any) error {
	if opID == "" || snapshot == nil {
		return nil
	}
	// An operation_id that is not a UUID cannot name a row; the reference's
	// driver would reject the cast, and the caller treats any error here as a
	// heartbeat it can ignore.
	encoded, err := json.Marshal(map[string]any{"progress": snapshot})
	if err != nil {
		return err
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET result_metadata = (COALESCE(result_metadata, '{}'::jsonb) || $2::jsonb),
	              updated_at = now()
	          WHERE operation_id = $1`
	_, err = s.db.ExecContext(ctx, query, opID, encoded)
	return err
}

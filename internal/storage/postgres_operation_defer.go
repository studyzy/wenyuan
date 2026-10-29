package storage

import (
	"context"
	"time"
)

// DeferOperation returns a deferred operation to the claimable pool.
//
// It differs from ScheduleOperationRetry in exactly one way, and that way is
// the point: a deferral records no failure. The status goes back to pending
// with the next attempt time, the claim is released, and the retry count is
// untouched — so sustained backpressure cannot walk work toward the
// permanent-failure cliff — while the error column keeps whatever it had,
// because nothing went wrong with the work itself. Whatever the processor
// reported about the attempt is merged onto the operation so an operator can
// still see why it was deferred.
func (s *PostgresStore) DeferOperation(ctx context.Context, opID string, next time.Time, result map[string]any) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET status = 'pending', next_retry_at = $2, worker_id = NULL, claimed_at = NULL,
	              updated_at = now()
	          WHERE operation_id = $1 AND status NOT IN ('completed','failed','cancelled')`
	args := []any{opID, next.UTC()}
	if len(result) > 0 {
		// The attempt's own record is kept; a processor that reports nothing
		// takes the same path with one statement fewer.
		metadata, err := marshalMetadata(result)
		if err != nil {
			return err
		}
		query = `UPDATE ` + quoteIdent(schema) + `.async_operations
		         SET status = 'pending', next_retry_at = $2, worker_id = NULL, claimed_at = NULL,
		             updated_at = now(),
		             result_metadata = (COALESCE(result_metadata, '{}'::jsonb) || $3::jsonb)
		         WHERE operation_id = $1 AND status NOT IN ('completed','failed','cancelled')`
		args = append(args, metadata)
	}
	execResult, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := execResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// The row is gone, or it reached a terminal state under us; either way
		// the deferral did not land, and the row's own state says why.
		if _, getErr := s.getOperation(ctx, opID); getErr != nil {
			return getErr
		}
		return ErrOperationConflict
	}
	return nil
}

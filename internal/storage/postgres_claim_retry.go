package storage

import (
	"context"
	"time"
)

// ScheduleOperationRetry records a failed attempt and the next attempt time,
// returning the operation to the claimable pool.
func (s *PostgresStore) ScheduleOperationRetry(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status = 'pending', retry_count = $2, next_retry_at = $3, error_message = $4,
	              worker_id = NULL, claimed_at = NULL, updated_at = now()
	          WHERE operation_id = $1 AND status NOT IN ('completed','failed','cancelled')`
	result, err := s.db.ExecContext(ctx, query, opID, retryCount, nextRetryAt.UTC(), errMsg)
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

// ScheduleOperationRetryWithResult records a retry and merges result onto the
// operation's result metadata in the same statement, implementing
// OperationRetryRecorder. The JSON merge (||) leaves any metadata written when
// the operation was enqueued, or by an earlier attempt, in place; the retry
// columns are set exactly as ScheduleOperationRetry sets them, so the deferral's
// schedule is unchanged.
func (s *PostgresStore) ScheduleOperationRetryWithResult(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string, result map[string]any) error {
	if len(result) == 0 {
		return s.ScheduleOperationRetry(ctx, opID, retryCount, nextRetryAt, errMsg)
	}
	metadata, err := marshalMetadata(result)
	if err != nil {
		return err
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status = 'pending', retry_count = $2, next_retry_at = $3, error_message = $4,
	              result_metadata = (COALESCE(result_metadata, '{}'::jsonb) || $5::jsonb),
	              worker_id = NULL, claimed_at = NULL, updated_at = now()
	          WHERE operation_id = $1 AND status NOT IN ('completed','failed','cancelled')`
	execResult, err := s.db.ExecContext(ctx, query, opID, retryCount, nextRetryAt.UTC(), errMsg, metadata)
	if err != nil {
		return err
	}
	affected, err := execResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrOperationConflict
	}
	return nil
}

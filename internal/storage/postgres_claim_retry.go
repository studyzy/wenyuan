package storage

import (
	"context"
	"time"
)

// retryCauseMetadata is the metadata the retry write merges on every scheduled
// retry: the cause of the attempt just lost, kept in result_metadata because
// completion clears error_message (worker/poller.py:1095-1098).
func retryCauseMetadata(errMsg string) map[string]any {
	return map[string]any{"last_retry_error": errMsg}
}

// ScheduleOperationRetry records a failed attempt and the next attempt time,
// returning the operation to the claimable pool. The cause of the attempt just
// lost is merged into result_metadata as last_retry_error on every retry, so a
// consumer that reads the record later still sees why the attempt failed.
func (s *PostgresStore) ScheduleOperationRetry(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	metadata, err := marshalMetadata(retryCauseMetadata(errMsg))
	if err != nil {
		return err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET status = 'pending', retry_count = $2, next_retry_at = $3, error_message = $4,
	              result_metadata = (COALESCE(result_metadata, '{}'::jsonb) || $5::jsonb),
	              worker_id = NULL, claimed_at = NULL, updated_at = now()
	          WHERE operation_id = $1 AND status NOT IN ('completed','failed','cancelled')`
	result, err := s.db.ExecContext(ctx, query, opID, retryCount, nextRetryAt.UTC(), errMsg, metadata)
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
// schedule is unchanged. last_retry_error is the retry's own cause, so a
// reported key of the same name does not displace it.
func (s *PostgresStore) ScheduleOperationRetryWithResult(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string, result map[string]any) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	merged := make(map[string]any, len(result)+1)
	for key, value := range result {
		merged[key] = value
	}
	merged["last_retry_error"] = errMsg
	metadata, err := marshalMetadata(merged)
	if err != nil {
		return err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
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

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

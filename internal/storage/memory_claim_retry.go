package storage

import (
	"context"
	"time"
)

// ScheduleOperationRetry records a failed attempt and the next attempt time.
func (s *MemoryStore) ScheduleOperationRetry(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	op, ok := s.operations[opID]
	if !ok {
		return ErrOperationNotFound
	}
	if op.Status.Terminal() {
		return ErrOperationConflict
	}
	release := nextRetryAt.UTC()
	op.RetryCount = retryCount
	op.NextRetryAt = &release
	op.Error = errMsg
	op.Status = StatusPending
	op.WorkerID = ""
	op.ClaimedAt = nil
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return nil
}

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

// ScheduleOperationRetryWithResult records a retry and merges result onto the
// operation's metadata, implementing OperationRetryRecorder. The retry is
// scheduled first, so its semantics — status, retry count and next attempt time
// — are exactly ScheduleOperationRetry's, and the merge only adds to the record.
func (s *MemoryStore) ScheduleOperationRetryWithResult(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string, result map[string]any) error {
	if err := s.ScheduleOperationRetry(ctx, opID, retryCount, nextRetryAt, errMsg); err != nil {
		return err
	}
	if len(result) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok {
		return ErrOperationNotFound
	}
	mergeOperationMetadata(&op, result)
	s.operations[opID] = op
	return nil
}

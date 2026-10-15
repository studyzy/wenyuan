package storage

import (
	"context"
	"time"
)

// DeferOperation returns a deferred operation to the claimable pool. The
// postgres backend documents the semantics; this is the same rule applied to
// the in-memory operation table.
func (s *MemoryStore) DeferOperation(ctx context.Context, opID string, next time.Time, result map[string]any) error {
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
	release := next.UTC()
	op.NextRetryAt = &release
	op.Status = StatusPending
	op.WorkerID = ""
	op.ClaimedAt = nil
	op.UpdatedAt = time.Now().UTC()
	// The attempt's own record is kept; the error column is not touched, because
	// a deferral is not a failure of the work.
	mergeOperationMetadata(&op, result)
	s.operations[opID] = op
	return nil
}

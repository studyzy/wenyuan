package storage

import (
	"context"
	"time"
)

// RecordOperationProgress merges a progress snapshot into the operation's
// result metadata. The postgres backend documents the semantics; this is the
// same top-level merge applied to the in-memory operation table, with the same
// tolerance for an unknown operation — a heartbeat for a row that has since
// been pruned is not an error the caller can act on.
func (s *MemoryStore) RecordOperationProgress(ctx context.Context, opID string, snapshot map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opID == "" || snapshot == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	op, ok := s.operations[opID]
	if !ok {
		return nil
	}
	mergeOperationMetadata(&op, map[string]any{"progress": snapshot})
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return nil
}

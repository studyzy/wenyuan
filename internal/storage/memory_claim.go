package storage

import (
	"context"
	"sort"
	"time"
)

// ClaimNextOperation claims the oldest claimable operation for a worker. A
// pending operation is claimable when its retry gate has passed; a processing
// operation is claimable only when its claim is older than the lease, which is
// how a consumer that died mid-operation stops stranding its work.
func (s *MemoryStore) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	if workerID == "" {
		return Operation{}, ErrInvalidOperationID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Operation{}, s.writeErr
	}

	now := time.Now().UTC()
	candidates := make([]string, 0, len(s.operations))
	for id, op := range s.operations {
		if claimable(op, now, lease) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return Operation{}, ErrOperationNotFound
	}
	// Deterministic order so two callers never race on which one is oldest.
	sort.Slice(candidates, func(i, j int) bool {
		left, right := s.operations[candidates[i]], s.operations[candidates[j]]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return candidates[i] < candidates[j]
	})

	claimed := s.operations[candidates[0]]
	claimed.Status = StatusProcessing
	claimed.WorkerID = workerID
	claimed.ClaimedAt = &now
	claimed.UpdatedAt = now
	s.operations[claimed.ID] = claimed
	return claimed, nil
}

// ReleaseOperationClaim returns a claimed operation to the claimable pool. It
// is what a graceful shutdown uses so its in-flight work is picked up again
// rather than waiting out the lease.
func (s *MemoryStore) ReleaseOperationClaim(ctx context.Context, opID, workerID string) error {
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
	if op.WorkerID != workerID {
		return ErrOperationConflict
	}
	now := time.Now().UTC()
	op.Status = StatusPending
	op.WorkerID = ""
	op.ClaimedAt = nil
	op.UpdatedAt = now
	s.operations[opID] = op
	return nil
}

// claimable reports whether an operation can be claimed now.
func claimable(op Operation, now time.Time, lease time.Duration) bool {
	switch op.Status {
	case StatusPending:
		if op.NextRetryAt != nil && op.NextRetryAt.After(now) {
			return false
		}
		return true
	case StatusProcessing:
		// A claim older than the lease is abandoned and may be reclaimed.
		if op.ClaimedAt == nil {
			return true
		}
		if lease <= 0 {
			return false
		}
		return now.Sub(*op.ClaimedAt) >= lease
	default:
		return false
	}
}

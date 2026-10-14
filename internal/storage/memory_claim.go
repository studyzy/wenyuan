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
		if !claimable(op, now, lease) {
			continue
		}
		// An operation whose serialization key another live claim holds is not
		// claimable: the key exists so two runs of the same work cannot proceed
		// beside each other and leave whichever finished last winning.
		if s.serializationHeldLocked(op, now, lease) {
			continue
		}
		candidates = append(candidates, id)
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
	s.operations[claimed.OperationID] = claimed
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

// serializationHeldLocked reports whether another live claim holds this
// operation's serialization key.
//
// An operation carries no key, or a key nobody else holds, in which case
// nothing constrains it. A claim past its lease is abandoned, so a consumer
// that died holding a key does not block that key forever — the same rule the
// claim itself applies.
func (s *MemoryStore) serializationHeldLocked(op Operation, now time.Time, lease time.Duration) bool {
	if op.SerializationKey == "" {
		return false
	}
	for id, held := range s.operations {
		if id == op.OperationID || held.SerializationKey != op.SerializationKey {
			continue
		}
		if held.Status != StatusProcessing {
			continue
		}
		if held.ClaimedAt == nil || lease <= 0 || now.Sub(*held.ClaimedAt) >= lease {
			continue
		}
		return true
	}
	return false
}

package storage

import (
	"context"
	"sort"
	"time"
)

// PendingRetainPeers returns the queued retain operations waiting behind a
// claimed operation on the same bank and serialization key. Candidates are
// pending rows with a payload whose retry gate has passed, oldest first with
// the identifier breaking ties — the order the document accumulates in.
func (s *MemoryStore) PendingRetainPeers(ctx context.Context, bankID, serializationKey string, limit int) ([]Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || serializationKey == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	now := time.Now().UTC()
	peers := make([]Operation, 0)
	for _, op := range s.operations {
		if op.BankID != bankID || op.SerializationKey != serializationKey {
			continue
		}
		if op.OperationType != OperationTypeRetain || op.Status != StatusPending || op.Payload == nil {
			continue
		}
		if op.NextRetryAt != nil && op.NextRetryAt.After(now) {
			continue
		}
		peers = append(peers, op)
	}
	sort.Slice(peers, func(i, j int) bool {
		if !peers[i].CreatedAt.Equal(peers[j].CreatedAt) {
			return peers[i].CreatedAt.Before(peers[j].CreatedAt)
		}
		return peers[i].OperationID < peers[j].OperationID
	})
	if len(peers) > limit {
		peers = peers[:limit]
	}
	return peers, nil
}

// ClaimPendingOperations claims the named operations that are still pending
// and returns the identifiers it took, in the order they were given. A name
// that is gone or no longer pending is skipped: the caller folds only the
// operations it actually holds.
func (s *MemoryStore) ClaimPendingOperations(ctx context.Context, workerID string, ids []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if workerID == "" {
		return nil, ErrInvalidOperationID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	now := time.Now().UTC()
	claimed := make([]string, 0, len(ids))
	for _, id := range ids {
		op, ok := s.operations[id]
		if !ok || op.Status != StatusPending {
			continue
		}
		op.Status = StatusProcessing
		op.WorkerID = workerID
		op.ClaimedAt = &now
		op.UpdatedAt = now
		s.operations[id] = op
		claimed = append(claimed, id)
	}
	return claimed, nil
}

package storage

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ClaimNextOperation claims the oldest claimable operation for a worker. A
// pending operation is claimable when its retry gate has passed; a processing
// operation is claimable only when its claim is older than the lease, which is
// how a consumer that died mid-operation stops stranding its work.
func (s *MemoryStore) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration) (Operation, error) {
	return s.claimNextOperation(ctx, workerID, lease, nil)
}

// ClaimNextOperationForTypes claims the oldest claimable operation whose type
// is in types. An empty list is equivalent to ClaimNextOperation.
func (s *MemoryStore) ClaimNextOperationForTypes(ctx context.Context, workerID string, lease time.Duration, types []string) (Operation, error) {
	return s.claimNextOperation(ctx, workerID, lease, types)
}

func (s *MemoryStore) claimNextOperation(ctx context.Context, workerID string, lease time.Duration, types []string) (Operation, error) {
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
		// A batch parent is a status aggregator nothing executes: its
		// lifecycle is driven by its children reaching terminal states. A
		// claim would mark it processing and then settle it as if it had run.
		if op.OperationType == AsyncRetainType {
			continue
		}
		if len(types) > 0 && !containsString(types, op.OperationType) {
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
	// Deterministic order so two callers never race on which one is oldest. The
	// bank with the oldest pending work is served first, so one bank's backlog
	// cannot starve the others; within a bank the order stays oldest-first.
	bankOldest := map[string]time.Time{}
	for _, op := range s.operations {
		if op.Status != StatusPending {
			continue
		}
		if existing, ok := bankOldest[op.BankID]; !ok || op.CreatedAt.Before(existing) {
			bankOldest[op.BankID] = op.CreatedAt
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := s.operations[candidates[i]], s.operations[candidates[j]]
		leftBank, okLeft := bankOldest[left.BankID]
		rightBank, okRight := bankOldest[right.BankID]
		if okLeft && okRight && !leftBank.Equal(rightBank) {
			return leftBank.Before(rightBank)
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return candidates[i] < candidates[j]
	})

	return s.claimLocked(candidates[0], workerID, now), nil
}

// claimLocked marks a chosen candidate as claimed by workerID. The caller holds
// the lock and has already checked the operation is claimable.
func (s *MemoryStore) claimLocked(opID, workerID string, now time.Time) Operation {
	claimed := s.operations[opID]
	claimed.Status = StatusProcessing
	claimed.WorkerID = workerID
	claimed.ClaimedAt = &now
	claimed.UpdatedAt = now
	s.operations[opID] = claimed
	return claimed
}

// ClaimConsolidationByPriority claims the oldest claimable consolidation
// operation, preferring the banks whose identifier matches a higher-priority
// pattern. It is the in-memory counterpart of the reference's tiered
// consolidation claim: a specific pattern always outranks the catch-all, and
// within a tier the oldest work goes first.
//
// An empty priority map is the reference's "no priority configured" case and
// falls back to a plain consolidation claim.
func (s *MemoryStore) ClaimConsolidationByPriority(ctx context.Context, workerID string, lease time.Duration, priority map[string]int) (Operation, error) {
	if len(priority) == 0 {
		return s.ClaimNextOperationForTypes(ctx, workerID, lease, []string{OperationTypeConsolidate})
	}
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
	candidates := make([]string, 0)
	for id, op := range s.operations {
		if op.OperationType != OperationTypeConsolidate {
			continue
		}
		if !claimable(op, now, lease) {
			continue
		}
		if s.serializationHeldLocked(op, now, lease) {
			continue
		}
		candidates = append(candidates, id)
	}
	if len(candidates) == 0 {
		return Operation{}, ErrOperationNotFound
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := s.operations[candidates[i]], s.operations[candidates[j]]
		leftRank := bankPriority(priority, left.BankID)
		rightRank := bankPriority(priority, right.BankID)
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return candidates[i] < candidates[j]
	})
	return s.claimLocked(candidates[0], workerID, now), nil
}

// bankPriority returns the tier of one bank: the highest priority among the
// specific patterns it matches, or the catch-all ("*") priority when it matches
// no specific pattern, or the reference's implicit tier of one when neither is
// configured. A specific pattern always takes precedence over the catch-all,
// even when the catch-all carries the higher number.
func bankPriority(priority map[string]int, bankID string) int {
	best := 0
	matched := false
	for pattern, value := range priority {
		if pattern == "*" {
			continue
		}
		if likeMatch(pattern, bankID) && (!matched || value > best) {
			best = value
			matched = true
		}
	}
	if matched {
		return best
	}
	if value, ok := priority["*"]; ok {
		return value
	}
	return 1
}

// likeMatch reports whether a bank identifier matches a priority pattern, where
// "*" stands for any run of characters. It is the in-memory form of the SQL
// LIKE the reference converts the pattern to ("*" becomes "%"), anchored at
// both ends.
func likeMatch(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(value, part)
		if index < 0 {
			return false
		}
		value = value[index+len(part):]
	}
	return strings.HasSuffix(value, parts[len(parts)-1])
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

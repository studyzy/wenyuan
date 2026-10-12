package storage

import (
	"context"
	"sort"
	"time"
)

// PutOperation records an asynchronous operation in its initial state.
func (s *MemoryStore) PutOperation(ctx context.Context, op Operation) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op.ID == "" {
		op.ID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}
	now := time.Now().UTC()
	op.CreatedAt = now
	op.UpdatedAt = now
	if op.Run == 0 {
		op.Run = 1
	}
	s.operations[op.ID] = op
	return op, nil
}

// GetOperation returns one operation by identifier.
func (s *MemoryStore) GetOperation(ctx context.Context, opID string) (Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.operations[opID]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	return op, nil
}

// AdvanceOperation moves an operation forward in its lifecycle, rejecting
// illegal transitions.
func (s *MemoryStore) AdvanceOperation(ctx context.Context, opID string, next OperationStatus, errMsg string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	if !validTransition(op.Status, next) {
		return op, ErrOperationConflict
	}
	op.Status = next
	op.Error = errMsg
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return op, nil
}

// RetryOperation starts a new run of a failed operation.
func (s *MemoryStore) RetryOperation(ctx context.Context, opID string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	if op.Status != StatusFailed {
		return op, ErrOperationConflict
	}
	op.Status = StatusPending
	op.Error = ""
	op.Run++
	op.RetryCount++
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return op, nil
}

// ListOperations returns one page of operations matching the query, most
// recently created first, together with the pre-paging total.
func (s *MemoryStore) ListOperations(ctx context.Context, query OperationQuery) (OperationPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]Operation, 0, len(s.operations))
	for _, op := range s.operations {
		if query.BankID != "" && op.BankID != query.BankID {
			continue
		}
		if query.Status != "" && string(op.Status) != query.Status {
			continue
		}
		if query.Type != "" && op.OperationType != query.Type {
			continue
		}
		matched = append(matched, op)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return OperationPage{Operations: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// CancelOperation cancels a non-terminal operation, returning
// ErrOperationConflict when the operation is already terminal.
func (s *MemoryStore) CancelOperation(ctx context.Context, opID string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	if op.Status.Terminal() {
		return op, ErrOperationConflict
	}
	now := time.Now().UTC()
	op.Status = StatusCancelled
	op.UpdatedAt = now
	op.CompletedAt = &now
	s.operations[opID] = op
	return op, nil
}

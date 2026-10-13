package storage

import (
	"context"
	"sort"
	"time"
)

// ListMemories returns one page of a bank's memory units, newest first.
func (s *MemoryStore) ListMemories(ctx context.Context, query MemoryQuery) (MemoryPage, error) {
	if err := ctx.Err(); err != nil {
		return MemoryPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return MemoryPage{}, s.readErr
	}
	matched := make([]MemoryUnit, 0, len(s.units))
	for _, unit := range s.units {
		if unit.BankID != query.BankID {
			continue
		}
		if query.Type != "" && string(unit.Type) != query.Type {
			continue
		}
		matched = append(matched, cloneMemoryUnit(unit))
	}
	sort.SliceStable(matched, func(i, j int) bool {
		left, right := matched[i].MentionedAt, matched[j].MentionedAt
		switch {
		case left == nil && right == nil:
			return matched[i].ID < matched[j].ID
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			if !left.Equal(*right) {
				return left.After(*right)
			}
			return matched[i].ID < matched[j].ID
		}
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return MemoryPage{Memories: matched[start:end], Total: total,
		Limit: query.Limit, Offset: query.Offset}, nil
}

// GetMemory returns one memory unit by identifier.
func (s *MemoryStore) GetMemory(ctx context.Context, bankID, memoryID string) (MemoryUnit, error) {
	if err := ctx.Err(); err != nil {
		return MemoryUnit{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	unit, ok := s.units[memoryID]
	if !ok || unit.BankID != bankID {
		return MemoryUnit{}, ErrMemoryNotFound
	}
	return cloneMemoryUnit(unit), nil
}

// UpdateMemory applies a partial update to one memory unit.
func (s *MemoryStore) UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error) {
	if err := ctx.Err(); err != nil {
		return MemoryUnit{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return MemoryUnit{}, s.writeErr
	}
	unit, ok := s.units[memoryID]
	if !ok || unit.BankID != bankID {
		return MemoryUnit{}, ErrMemoryNotFound
	}
	if patch.Text != nil {
		unit.Text = *patch.Text
	}
	if patch.Tags != nil {
		unit.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.Metadata != nil {
		unit.Metadata = copyAnyMap(*patch.Metadata)
	}
	s.units[memoryID] = unit
	// Every change is recorded, so a memory's history explains its current
	// state rather than only its creation.
	if s.memoryHistory == nil {
		s.memoryHistory = map[string][]MemoryHistoryEntry{}
	}
	s.memoryHistory[memoryID] = append(s.memoryHistory[memoryID], MemoryHistoryEntry{
		ChangedAt: time.Now().UTC(), Reason: "update", Text: unit.Text,
		Tags: append([]string(nil), unit.Tags...),
	})
	return cloneMemoryUnit(unit), nil
}

// MemoryHistory returns the recorded changes of one memory unit, newest first.
func (s *MemoryStore) MemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	unit, ok := s.units[memoryID]
	if !ok || unit.BankID != bankID {
		return nil, ErrMemoryNotFound
	}
	entries := append([]MemoryHistoryEntry(nil), s.memoryHistory[memoryID]...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ChangedAt.After(entries[j].ChangedAt)
	})
	return entries, nil
}

// DeleteObservations removes the observations of a bank, or of one memory.
func (s *MemoryStore) DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	removed := 0
	for id, unit := range s.units {
		if unit.BankID != bankID || unit.Type != TypeObservation {
			continue
		}
		if memoryID != "" {
			sources := s.observationSources[id]
			if !containsString(sources, memoryID) {
				continue
			}
		}
		delete(s.units, id)
		delete(s.unitEntities, id)
		delete(s.observationSources, id)
		removed++
	}
	return removed, nil
}

// EnqueueConsolidation records a consolidation pass for a bank.
func (s *MemoryStore) EnqueueConsolidation(ctx context.Context, bankID string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Operation{}, false, s.writeErr
	}
	// A second consolidation for the same bank while one is outstanding would
	// do the same work twice, so the request is deduplicated.
	for _, op := range s.operations {
		if op.BankID == bankID && op.OperationType == OperationTypeConsolidate && !op.Status.Terminal() {
			return op, true, nil
		}
	}
	now := time.Now().UTC()
	op := Operation{
		ID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	s.operations[op.ID] = op
	return op, false, nil
}

// RecoverConsolidations retries a bank's failed consolidations.
func (s *MemoryStore) RecoverConsolidations(ctx context.Context, bankID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	retried := 0
	for id, op := range s.operations {
		if op.BankID != bankID || op.OperationType != OperationTypeConsolidate || op.Status != StatusFailed {
			continue
		}
		op.Status = StatusPending
		op.Error = ""
		op.NextRetryAt = nil
		op.UpdatedAt = time.Now().UTC()
		s.operations[id] = op
		retried++
	}
	return retried, nil
}

// cloneMemoryUnit copies the mutable fields of a memory unit.
func cloneMemoryUnit(unit MemoryUnit) MemoryUnit {
	out := unit
	out.Tags = append([]string(nil), unit.Tags...)
	out.Embedding = append([]float32(nil), unit.Embedding...)
	out.Metadata = copyAnyMap(unit.Metadata)
	return out
}

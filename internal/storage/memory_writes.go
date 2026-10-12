package storage

import (
	"context"
	"time"
)

// RecordWrite persists a memory write. A bank that does not exist is created
// lazily in the same operation as the write, with a display name equal to its
// identifier, a neutral disposition and an empty mission. A reused operation
// identifier returns ErrOperationConflict without producing a second effect.
func (s *MemoryStore) RecordWrite(ctx context.Context, req WriteRequest) (WriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return WriteResult{}, s.writeErr
	}
	if _, ok := s.banks[req.BankID]; !ok {
		s.createBankLocked(req.BankID)
	}
	if req.OperationID != "" {
		if existing, ok := s.writes[req.OperationID]; ok {
			return existing, ErrOperationConflict
		}
	}
	opID := req.OperationID
	if opID == "" {
		opID = newID()
	}
	memoryID := newID()
	now := time.Now().UTC()
	match := RecallMatch{
		ID:            memoryID,
		Text:          req.Content,
		Type:          string(TypeWorld),
		DocumentID:    req.DocumentID,
		Entities:      req.Entities,
		Scores:        map[string]float64{},
		SourceFactIDs: []string{},
	}
	s.memories[req.BankID] = append(s.memories[req.BankID], match)
	s.recordWriteWatermarkLocked(req.BankID, now)

	result := WriteResult{OperationID: opID, MemoryID: memoryID, Status: StatusCompleted}
	s.writes[opID] = result

	op := Operation{
		ID:        opID,
		BankID:    req.BankID,
		Status:    StatusCompleted,
		Run:       1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if req.Async {
		op.Status = StatusPending
		result.Status = StatusPending
		result.Accepted = true
	}
	s.operations[opID] = op
	return result, nil
}

// createBankLocked lazily creates a bank with neutral defaults, sharing the
// write's transaction.
func (s *MemoryStore) createBankLocked(bankID string) Bank {
	now := time.Now().UTC()
	bank := Bank{
		ID:                    bankID,
		Name:                  bankID,
		DispositionSkepticism: DispositionTraitNeutral,
		DispositionLiteralism: DispositionTraitNeutral,
		DispositionEmpathy:    DispositionTraitNeutral,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	s.banks[bankID] = bank
	s.order = append(s.order, bankID)
	s.invalidateBankLocked(bankID)
	return bank
}

// recordWriteWatermarkLocked advances a bank's last-write watermark after a
// content write, so it sorts most-recently-written first.
func (s *MemoryStore) recordWriteWatermarkLocked(bankID string, at time.Time) {
	bank := s.banks[bankID]
	ts := at
	bank.LastWriteAt = &ts
	bank.UpdatedAt = at
	s.banks[bankID] = bank
	s.invalidateBankLocked(bankID)
}

// Search returns recall matches for a bank after applying simple filters.
func (s *MemoryStore) Search(ctx context.Context, req RecallRequest) (RecallResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return RecallResult{}, s.readErr
	}
	if _, ok := s.banks[req.BankID]; !ok {
		return RecallResult{}, ErrBankNotFound
	}
	matches := make([]RecallMatch, 0)
	for _, match := range s.memories[req.BankID] {
		if req.Query != "" && !containsFold(match.Text, req.Query) {
			continue
		}
		if len(req.Types) > 0 && !containsString(req.Types, match.Type) {
			continue
		}
		matches = append(matches, match)
	}
	limit := req.Limit
	if limit <= 0 || limit > len(matches) {
		limit = len(matches)
	}
	truncated := limit < len(matches)
	return RecallResult{Matches: matches[:limit], Truncated: truncated}, nil
}

// Synthesis returns retrieval context for a reflection.
func (s *MemoryStore) Synthesis(ctx context.Context, req ReflectRequest) (ReflectContext, error) {
	result, err := s.Search(ctx, RecallRequest{BankID: req.BankID, Query: req.Query, Limit: req.Limit})
	if err != nil {
		return ReflectContext{}, err
	}
	return ReflectContext{Matches: result.Matches, Trace: map[string]any{"retrieved": len(result.Matches)}}, nil
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	hl := lower(haystack)
	nl := lower(needle)
	return index(hl, nl)
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

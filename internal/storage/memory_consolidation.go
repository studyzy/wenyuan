package storage

import (
	"context"
	"sort"
	"time"
)

// ConsolidationFacts returns the newest raw facts of a bank that have not yet
// been folded into an observation.
func (s *MemoryStore) ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]ConsolidationFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	var candidates []ConsolidationFact
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.Type == TypeObservation {
			continue
		}
		if _, folded := s.consolidated[unit.ID]; folded {
			continue
		}
		candidates = append(candidates, ConsolidationFact{
			ID: unit.ID, Text: unit.Text,
			OccurredStart: unit.OccurredStart, OccurredEnd: unit.OccurredEnd,
			MentionedAt: unit.MentionedAt,
		})
	}
	sortNewestFirst(candidates, func(f ConsolidationFact) *time.Time { return f.MentionedAt },
		func(a, b ConsolidationFact) bool { return a.ID < b.ID })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// ConsolidationObservations returns a bank's existing observations, newest
// first, with the facts each rests on.
func (s *MemoryStore) ConsolidationObservations(ctx context.Context, bankID string, limit int) ([]ConsolidationObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	var candidates []ConsolidationObservation
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.Type != TypeObservation {
			continue
		}
		sources := append([]string(nil), s.observationSources[unit.ID]...)
		candidates = append(candidates, ConsolidationObservation{
			ID: unit.ID, Text: unit.Text, SourceIDs: sources,
			ProofCount: len(sources), MentionedAt: unit.MentionedAt,
		})
	}
	sortNewestFirst(candidates, func(o ConsolidationObservation) *time.Time { return o.MentionedAt },
		func(a, b ConsolidationObservation) bool { return a.ID < b.ID })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// sortNewestFirst orders entries by their timestamp descending, then by a
// tie-break, so a page is deterministic when several entries share a time.
func sortNewestFirst[T any](entries []T, at func(T) *time.Time, tieBreak func(a, b T) bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := at(entries[i]), at(entries[j])
		switch {
		case left == nil && right == nil:
			return tieBreak(entries[i], entries[j])
		case left == nil:
			return false
		case right == nil:
			return true
		case left.Equal(*right):
			return tieBreak(entries[i], entries[j])
		default:
			return left.After(*right)
		}
	})
}

// CreateObservation records a new observation resting on the given facts.
func (s *MemoryStore) CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	unit, err := s.UpsertUnit(ctx, MemoryUnit{BankID: bankID, Text: text, Type: TypeObservation})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sourceID := range sourceIDs {
		if !containsString(s.observationSources[unit.ID], sourceID) {
			s.observationSources[unit.ID] = append(s.observationSources[unit.ID], sourceID)
		}
	}
	return unit.ID, nil
}

// UpdateObservation rewrites an observation and attaches more evidence.
func (s *MemoryStore) UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	unit, ok := s.units[observationID]
	if !ok || unit.BankID != bankID || unit.Type != TypeObservation {
		return ErrMemoryNotFound
	}
	unit.Text = text
	now := time.Now().UTC()
	unit.MentionedAt = &now
	s.units[observationID] = unit
	for _, sourceID := range sourceIDs {
		if !containsString(s.observationSources[observationID], sourceID) {
			s.observationSources[observationID] = append(s.observationSources[observationID], sourceID)
		}
	}
	return nil
}

// DeleteObservation removes one observation and its evidence links.
func (s *MemoryStore) DeleteObservation(ctx context.Context, bankID, observationID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	unit, ok := s.units[observationID]
	if !ok || unit.BankID != bankID || unit.Type != TypeObservation {
		return nil
	}
	delete(s.units, observationID)
	delete(s.observationSources, observationID)
	return nil
}

// MarkConsolidated records that the given facts have been folded.
func (s *MemoryStore) MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.consolidated == nil {
		s.consolidated = map[string]struct{}{}
	}
	for _, id := range factIDs {
		if unit, ok := s.units[id]; ok && unit.BankID == bankID {
			s.consolidated[id] = struct{}{}
		}
	}
	return nil
}

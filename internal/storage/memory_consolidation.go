package storage

import (
	"context"
	"sort"
	"time"

	"github.com/studyzy/wenyuan/internal/timefmt"
)

// ConsolidationTuning reports the deployment's consolidation knobs, so the pass
// reads the configured batch size and model-call budget off the store rather
// than the package defaults.
func (s *MemoryStore) ConsolidationTuning() ConsolidationTuning {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tuning.Consolidation
}

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
		if unit.BankID != bankID || unit.FactType == TypeObservation {
			continue
		}
		if _, folded := s.consolidated[unit.ID]; folded {
			continue
		}
		// A memory that already failed consolidation is not retried: the
		// reference's candidate query excludes it (consolidation_failed_at IS
		// NULL, pg/reads.py:383-387) so one poison input is not re-fed forever.
		// recovery is the explicit door back in.
		if unit.ConsolidationFailedAt != nil {
			continue
		}
		candidates = append(candidates, ConsolidationFact{
			ID: unit.ID, Text: unit.Text,
			OccurredStart: unit.OccurredStart, OccurredEnd: unit.OccurredEnd,
			MentionedAt: unit.MentionedAt,
			Tags:        canonicalScope(unit.Tags), ObservationScopes: unit.ObservationScopes,
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
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		sources := append([]string(nil), s.observationSources[unit.ID]...)
		candidates = append(candidates, ConsolidationObservation{
			ID: unit.ID, Text: unit.Text, SourceIDs: sources,
			SourceTexts: s.sourceTextsLocked(sources),
			ProofCount:  len(sources), MentionedAt: unit.MentionedAt,
			Scope: canonicalScope(unit.Tags),
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

// ObservationDuplicates embeds an observation text and returns that embedding
// together with the in-scope observations that carry a stored embedding,
// nearest first. It reports embedded=false when the deployment has no embedding
// model, or when the text produced no vector, so the caller falls back to its
// text-similarity floor rather than losing dedup.
func (s *MemoryStore) ObservationDuplicates(ctx context.Context, bankID string, scope []string, text string, limit int) ([]float32, []ConsolidationObservation, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	probe := embedObservation(ctx, s.observationEmbedderRef(), text)
	if len(probe) == 0 {
		return nil, nil, false, nil
	}
	if limit <= 0 {
		limit = 10
	}
	scope = canonicalScope(scope)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, nil, false, s.readErr
	}
	type scored struct {
		observation ConsolidationObservation
		similarity  float64
	}
	var matches []scored
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation || len(unit.Embedding) == 0 {
			continue
		}
		if !sameScope(canonicalScope(unit.Tags), scope) {
			continue
		}
		sources := append([]string(nil), s.observationSources[unit.ID]...)
		matches = append(matches, scored{
			observation: ConsolidationObservation{
				ID: unit.ID, Text: unit.Text, SourceIDs: sources,
				SourceTexts: s.sourceTextsLocked(sources),
				ProofCount:  len(sources), MentionedAt: unit.MentionedAt,
				Scope:     canonicalScope(unit.Tags),
				Embedding: append([]float32(nil), unit.Embedding...),
			},
			similarity: cosineBetween(probe, unit.Embedding),
		})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].similarity != matches[j].similarity {
			return matches[i].similarity > matches[j].similarity
		}
		return matches[i].observation.ID < matches[j].observation.ID
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	observations := make([]ConsolidationObservation, 0, len(matches))
	for _, match := range matches {
		observations = append(observations, match.observation)
	}
	return probe, observations, true, nil
}

// CreateObservation records a new observation resting on the given facts.
func (s *MemoryStore) CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	unit := MemoryUnit{BankID: bankID, Text: text, FactType: TypeObservation, ContextAbsent: true}
	unit.Embedding = embedObservation(ctx, s.observationEmbedderRef(), text)
	stored, err := s.UpsertUnit(ctx, unit)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sourceID := range sourceIDs {
		if !containsString(s.observationSources[stored.ID], sourceID) {
			s.observationSources[stored.ID] = append(s.observationSources[stored.ID], sourceID)
		}
	}
	return stored.ID, nil
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
	if !ok || unit.BankID != bankID || unit.FactType != TypeObservation {
		return ErrMemoryNotFound
	}
	// The pre-update state is kept as a history entry, so what the conclusion
	// said before it was reinforced — and which sources it already rested on —
	// stays readable. The tail is bounded so a frequently-reinforced observation
	// does not grow forever.
	if s.tuning.ObservationHistory {
		s.recordObservationHistoryLocked(unit, sourceIDs)
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

// recordObservationHistoryLocked appends one pre-update snapshot and trims the
// history to its retention bound. The caller holds the write lock.
func (s *MemoryStore) recordObservationHistoryLocked(unit MemoryUnit, newSourceIDs []string) {
	if s.memoryHistory == nil {
		s.memoryHistory = map[string][]MemoryHistoryEntry{}
	}
	text := unit.Text
	tags := append([]string(nil), unit.Tags...)
	if tags == nil {
		tags = []string{}
	}
	s.memoryHistory[unit.ID] = append(s.memoryHistory[unit.ID], MemoryHistoryEntry{
		ChangedAt:             time.Now().UTC(),
		PreviousText:          &text,
		PreviousTags:          tags,
		PreviousOccurredStart: timefmt.FormatTimestampPtr(unit.OccurredStart),
		PreviousOccurredEnd:   timefmt.FormatTimestampPtr(unit.OccurredEnd),
		PreviousMentionedAt:   timefmt.FormatTimestampPtr(unit.MentionedAt),
		NewSourceMemoryIDs:    append([]string(nil), newSourceIDs...),
	})
	bound := s.tuning.ObservationHistoryMax
	if bound <= 0 {
		bound = ObservationHistoryMaxEntries
	}
	if len(s.memoryHistory[unit.ID]) > bound {
		s.memoryHistory[unit.ID] = s.memoryHistory[unit.ID][len(s.memoryHistory[unit.ID])-bound:]
	}
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
	if !ok || unit.BankID != bankID || unit.FactType != TypeObservation {
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

// MarkConsolidationFailed records that a pass gave up on the given facts, so
// the candidates read excludes them until recovery requeues them.
func (s *MemoryStore) MarkConsolidationFailed(ctx context.Context, bankID string, factIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	now := time.Now().UTC()
	for _, id := range factIDs {
		unit, ok := s.units[id]
		if !ok || unit.BankID != bankID {
			continue
		}
		failed := now
		unit.ConsolidationFailedAt = &failed
		s.units[id] = unit
	}
	return nil
}

// DeleteStaleObservations removes the observations that rest on any of the
// given facts and requeues their surviving co-sources, before those facts are
// deleted. An observation that has lost a source is stale.
func (s *MemoryStore) DeleteStaleObservations(ctx context.Context, bankID string, factIDs []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(factIDs) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.deleteStaleObservationsLocked(bankID, factIDs)
}

// deleteStaleObservationsLocked is the body of the sweep, run while the caller
// already holds the write lock so a wider retag can share it.
func (s *MemoryStore) deleteStaleObservationsLocked(bankID string, factIDs []string) (int, error) {
	deleted := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		deleted[id] = true
	}
	doomed := make([]string, 0)
	remaining := make([]string, 0)
	seen := map[string]bool{}
	for id, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		sources := s.observationSources[id]
		hit := false
		for _, source := range sources {
			if deleted[source] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		doomed = append(doomed, id)
		for _, source := range sources {
			if deleted[source] || seen[source] {
				continue
			}
			seen[source] = true
			remaining = append(remaining, source)
		}
	}
	for _, id := range doomed {
		delete(s.units, id)
		delete(s.observationSources, id)
		delete(s.consolidated, id)
		delete(s.memoryHistory, id)
	}
	for _, id := range remaining {
		if unit, ok := s.units[id]; ok && (unit.FactType == TypeWorld || unit.FactType == TypeExperience) {
			unit.ConsolidatedAt = nil
			s.units[id] = unit
			delete(s.consolidated, id)
		}
	}
	return len(doomed), nil
}

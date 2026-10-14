package storage

import "context"

// The store implements the read surface a reflection loop needs, so the loop
// asks for evidence through the same interface every other caller uses rather
// than reaching a table itself.

// SearchMentalModels returns the bank's perspectives matching a query, ranked
// by the caller-facing mental-model search.
func (s *MemoryStore) ReflectMentalModels(ctx context.Context, bankID, query string, maxResults int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	models := selectMentalModels(ReflectQuery{BankID: bankID, Query: query})
	out := make([]ReflectCitation, 0, len(models))
	for _, model := range models {
		if maxResults > 0 && len(out) >= maxResults {
			break
		}
		out = append(out, ReflectCitation{ID: model.ID, Text: model.Text})
	}
	return out, nil
}

// SearchObservations returns the bank's consolidated conclusions matching a
// query.
func (s *MemoryStore) ReflectObservations(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0)
	budget := maxTokens
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		if query != "" && !containsFold(unit.Text, query) {
			continue
		}
		cost := len(unit.Text)/4 + 1
		if budget > 0 && cost > budget {
			break
		}
		budget -= cost
		out = append(out, ReflectCitation{ID: unit.ID, Text: unit.Text})
		if budget <= 0 {
			break
		}
	}
	return out, nil
}

// ReflectMemories returns the bank's raw memories matching a query.
func (s *MemoryStore) ReflectMemories(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0)
	budget := maxTokens
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType == TypeObservation {
			continue
		}
		if query != "" && !containsFold(unit.Text, query) {
			continue
		}
		cost := len(unit.Text)/4 + 1
		if budget > 0 && cost > budget {
			break
		}
		budget -= cost
		out = append(out, ReflectCitation{ID: unit.ID, Text: unit.Text})
		if budget <= 0 {
			break
		}
	}
	return out, nil
}

// ReadMentalModels returns specific perspectives by identifier.
func (s *MemoryStore) ReflectReadModels(ctx context.Context, bankID string, ids []string) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0, len(ids))
	for _, id := range ids {
		for _, model := range s.mentalModels[bankID] {
			if model.ID == id {
				out = append(out, ReflectCitation{ID: model.ID, Text: model.Text})
			}
		}
	}
	return out, nil
}

// Expand returns the source context of specific memories.
func (s *MemoryStore) ReflectExpand(ctx context.Context, bankID string, ids []string) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0, len(ids))
	for _, id := range ids {
		unit, ok := s.units[id]
		if !ok || unit.BankID != bankID {
			continue
		}
		text := unit.Text
		// The surrounding document is the context the fact was extracted from,
		// which is what expanding a memory means.
		if unit.DocumentID != "" {
			if doc, ok := s.documents[unit.DocumentID]; ok && doc.OriginalText != "" {
				text = doc.OriginalText
			}
		}
		out = append(out, ReflectCitation{ID: unit.ID, Text: text, DocumentID: unit.DocumentID, ChunkID: unit.ChunkID})
	}
	return out, nil
}

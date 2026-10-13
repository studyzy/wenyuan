package storage

import (
	"context"
	"sort"
	"time"
)

// ListMentalModels returns the mental models of one bank, newest first.
func (s *MemoryStore) ListMentalModels(ctx context.Context, bankID string, subtype MentalModelSubtype) ([]MentalModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MentalModel, 0)
	for _, model := range s.mentalModelRecords {
		if model.BankID != bankID {
			continue
		}
		if subtype != "" && model.Subtype != subtype {
			continue
		}
		out = append(out, cloneMentalModel(model))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetMentalModel returns one mental model by identifier within a bank.
func (s *MemoryStore) GetMentalModel(ctx context.Context, bankID, id string) (MentalModel, error) {
	if err := ctx.Err(); err != nil {
		return MentalModel{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	model, ok := s.mentalModelRecords[mentalModelKey(bankID, id)]
	if !ok {
		return MentalModel{}, ErrMentalModelNotFound
	}
	return cloneMentalModel(model), nil
}

// PutMentalModel inserts or replaces a mental model.
func (s *MemoryStore) PutMentalModel(ctx context.Context, model MentalModel) (MentalModel, error) {
	if err := ctx.Err(); err != nil {
		return MentalModel{}, err
	}
	if err := validateMentalModel(model); err != nil {
		return MentalModel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return MentalModel{}, s.writeErr
	}
	if s.mentalModelRecords == nil {
		s.mentalModelRecords = map[string]MentalModel{}
	}
	now := time.Now().UTC()
	if existing, ok := s.mentalModelRecords[mentalModelKey(model.BankID, model.ID)]; ok {
		model.CreatedAt = existing.CreatedAt
	}
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.LastUpdated = now
	if model.LastRefreshedAt.IsZero() {
		model.LastRefreshedAt = now
	}
	s.mentalModelRecords[mentalModelKey(model.BankID, model.ID)] = cloneMentalModel(model)
	return cloneMentalModel(model), nil
}

// UpdateMentalModel applies a partial update and returns the stored value.
func (s *MemoryStore) UpdateMentalModel(ctx context.Context, bankID, id string, patch MentalModelPatch) (MentalModel, error) {
	if err := ctx.Err(); err != nil {
		return MentalModel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return MentalModel{}, s.writeErr
	}
	key := mentalModelKey(bankID, id)
	model, ok := s.mentalModelRecords[key]
	if !ok {
		return MentalModel{}, ErrMentalModelNotFound
	}
	applyMentalModelPatch(&model, patch)
	model.LastUpdated = time.Now().UTC()
	s.mentalModelRecords[key] = cloneMentalModel(model)
	return cloneMentalModel(model), nil
}

// DeleteMentalModel removes a mental model.
func (s *MemoryStore) DeleteMentalModel(ctx context.Context, bankID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	key := mentalModelKey(bankID, id)
	if _, ok := s.mentalModelRecords[key]; !ok {
		return ErrMentalModelNotFound
	}
	delete(s.mentalModelRecords, key)
	delete(s.mentalModelHistory, key)
	return nil
}

// ClearMentalModel empties a mental model's content and history while keeping
// the record.
func (s *MemoryStore) ClearMentalModel(ctx context.Context, bankID, id string) (MentalModel, error) {
	if err := ctx.Err(); err != nil {
		return MentalModel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return MentalModel{}, s.writeErr
	}
	key := mentalModelKey(bankID, id)
	model, ok := s.mentalModelRecords[key]
	if !ok {
		return MentalModel{}, ErrMentalModelNotFound
	}
	model.Content = ""
	model.Structured = nil
	model.ReflectResult = nil
	model.Observations = map[string]any{"observations": []any{}}
	now := time.Now().UTC()
	model.LastUpdated = now
	model.LastRefreshedAt = now
	s.mentalModelRecords[key] = cloneMentalModel(model)
	delete(s.mentalModelHistory, key)
	return cloneMentalModel(model), nil
}

// MentalModelHistory returns the recorded refreshes of one mental model,
// newest first.
func (s *MemoryStore) MentalModelHistory(ctx context.Context, bankID, id string) ([]MentalModelHistoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.mentalModelRecords[mentalModelKey(bankID, id)]; !ok {
		return nil, ErrMentalModelNotFound
	}
	entries := append([]MentalModelHistoryEntry(nil), s.mentalModelHistory[mentalModelKey(bankID, id)]...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].RefreshedAt.After(entries[j].RefreshedAt)
	})
	return entries, nil
}

// RecordMentalModelHistory appends one refresh to a mental model's history.
func (s *MemoryStore) RecordMentalModelHistory(ctx context.Context, bankID, id string, entry MentalModelHistoryEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mentalModelKey(bankID, id)
	if _, ok := s.mentalModelRecords[key]; !ok {
		return ErrMentalModelNotFound
	}
	if s.mentalModelHistory == nil {
		s.mentalModelHistory = map[string][]MentalModelHistoryEntry{}
	}
	if entry.RefreshedAt.IsZero() {
		entry.RefreshedAt = time.Now().UTC()
	}
	s.mentalModelHistory[key] = append(s.mentalModelHistory[key], entry)
	return nil
}

// mentalModelKey composes the per-bank identity, because a mental model
// identifier is unique within its bank rather than globally.
func mentalModelKey(bankID, id string) string { return bankID + "\x00" + id }

// validateMentalModel rejects a record the column would refuse.
func validateMentalModel(model MentalModel) error {
	if model.ID == "" || model.BankID == "" {
		return ErrInvalidOperationID
	}
	if !ValidMentalModelSubtype(model.Subtype) {
		return ErrInvalidMentalModelSubtype
	}
	return nil
}

// applyMentalModelPatch applies the non-nil fields of a patch.
func applyMentalModelPatch(model *MentalModel, patch MentalModelPatch) {
	if patch.Name != nil {
		model.Name = *patch.Name
	}
	if patch.Description != nil {
		model.Description = *patch.Description
	}
	if patch.SourceQuery != nil {
		model.SourceQuery = *patch.SourceQuery
	}
	if patch.Content != nil {
		model.Content = *patch.Content
	}
	if patch.Tags != nil {
		model.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.MaxTokens != nil {
		model.MaxTokens = *patch.MaxTokens
	}
	if patch.Trigger != nil {
		model.Trigger = copyAnyMap(*patch.Trigger)
	}
	if patch.Observations != nil {
		model.Observations = copyAnyMap(*patch.Observations)
	}
	if patch.Structured != nil {
		model.Structured = copyAnyMap(*patch.Structured)
	}
	if patch.ReflectResult != nil {
		model.ReflectResult = copyAnyMap(*patch.ReflectResult)
	}
}

// cloneMentalModel deep-copies the mutable fields so a caller cannot mutate
// stored state through the returned value.
func cloneMentalModel(model MentalModel) MentalModel {
	out := model
	out.Tags = append([]string(nil), model.Tags...)
	out.Links = append([]string(nil), model.Links...)
	out.Embedding = append([]float32(nil), model.Embedding...)
	out.Observations = copyAnyMap(model.Observations)
	out.Trigger = copyAnyMap(model.Trigger)
	out.Structured = copyAnyMap(model.Structured)
	out.ReflectResult = copyAnyMap(model.ReflectResult)
	out.History = append([]map[string]any(nil), model.History...)
	return out
}

// copyAnyMap shallow-copies a map so the caller gets an independent handle.
func copyAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

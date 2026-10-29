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
		out = append(out, s.withKnowledgePageFlag(bankID, cloneMentalModel(model)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastRefreshedAt.Equal(out[j].LastRefreshedAt) {
			return out[i].LastRefreshedAt.After(out[j].LastRefreshedAt)
		}
		return out[i].ID > out[j].ID
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
	return s.withKnowledgePageFlag(bankID, cloneMentalModel(model)), nil
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
	// The reference embeds a mental model from its own text on every write, so a
	// rename or content change re-embeds and a create is reachable through the
	// vector arm of knowledge search. A configured embedder owns the field; a
	// caller-supplied vector is kept only when none is configured.
	if generator := s.observationEmbedder; generator != nil {
		if vector := embedMentalModelText(ctx, generator, model.Name, model.Content); len(vector) > 0 {
			if err := ValidateEmbedding(vector); err != nil {
				return MentalModel{}, err
			}
			model.Embedding = vector
		}
	}
	now := time.Now().UTC()
	if existing, ok := s.mentalModelRecords[mentalModelKey(model.BankID, model.ID)]; ok {
		model.CreatedAt = existing.CreatedAt
	}
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.LastUpdated = now
	// The column is NOT NULL DEFAULT now() in the reference's schema, so a row is
	// stamped when it is created. That stamp says "written now", not "read the
	// bank": the refresh's delta window reads last_memory_seen_at instead, which
	// stays unset until a refresh records how far through the bank it looked.
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
	// The reference's update re-embeds from the new name and the stored content,
	// so a rename or content change moves the page's vector too.
	if generator := s.observationEmbedder; generator != nil {
		if vector := embedMentalModelText(ctx, generator, model.Name, model.Content); len(vector) > 0 {
			if err := ValidateEmbedding(vector); err != nil {
				return MentalModel{}, err
			}
			model.Embedding = vector
		}
	}
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
	// The reference's clear resets exactly content, structured_content,
	// last_refreshed_source_query and last_refresh_failed_at; the reflect payload,
	// observations, timestamps and history all survive. The dense vector is
	// rebuilt from the name alone, so the cleared page stops ranking on content it
	// no longer has and stays findable by title.
	model.Content = ""
	model.Structured = nil
	model.LastRefreshedSourceQuery = ""
	model.LastRefreshFailedAt = nil
	model.Embedding = embedMentalModelText(ctx, s.observationEmbedder, model.Name, "")
	s.mentalModelRecords[key] = cloneMentalModel(model)
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
		return entries[i].ChangedAt.After(entries[j].ChangedAt)
	})
	return entries, nil
}

// RecordMentalModelHistory appends one refresh to a mental model's history.
func (s *MemoryStore) RecordMentalModelHistory(ctx context.Context, bankID, id string, entry MentalModelHistoryEntry) error {
	if !s.tuning.MentalModelHistory {
		// The reference's switch for the record: a deployment that turned it off
		// keeps no history, and the read routes answer with none.
		return nil
	}
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
	if entry.ChangedAt.IsZero() {
		entry.ChangedAt = time.Now().UTC()
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
		// A content write is a refresh as far as the clock is concerned - a hand
		// edit and a completed refresh both leave the document current - and it
		// clears the failure park, because the document was just written
		// successfully. The reference advances last_refreshed_at on exactly these
		// two events; the data watermark moves separately, only when a refresh
		// records how far through the bank it read.
		now := time.Now().UTC()
		model.LastRefreshedAt = now
		model.LastRefreshFailedAt = nil
	}
	if patch.LastRefreshedSourceQuery != nil {
		model.LastRefreshedSourceQuery = *patch.LastRefreshedSourceQuery
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
	// The full-refresh clock is stamped only by a refresh that ran in full
	// mode, so a patch states it explicitly rather than advancing it with the
	// content write: a hand edit moves last_refreshed_at and leaves this alone.
	if patch.LastFullRefreshedAt != nil {
		stamp := patch.LastFullRefreshedAt.UTC()
		model.LastFullRefreshedAt = &stamp
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

// PromoteOperationPayload sets one payload member on an operation that is still
// in the expected status. The in-memory store serialises every submit under its
// own lock, so no separate critical section is needed.
func (s *MemoryStore) PromoteOperationPayload(ctx context.Context, bankID, operationID, key string, value any, onlyStatus OperationStatus) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return false, s.writeErr
	}
	op, ok := s.operations[operationID]
	if !ok || op.BankID != bankID || op.Status != onlyStatus {
		return false, nil
	}
	if op.Payload == nil {
		op.Payload = map[string]any{}
	}
	if current, present := op.Payload[key]; present && current == value {
		return false, nil
	}
	op.Payload[key] = value
	op.UpdatedAt = time.Now().UTC()
	s.operations[operationID] = op
	return true, nil
}

// ClearOperationRefreshPark unparks a pending refresh: the automatic marker
// leaves the payload and next_retry_at clears in the same write.
func (s *MemoryStore) ClearOperationRefreshPark(ctx context.Context, bankID, operationID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return false, s.writeErr
	}
	op, ok := s.operations[operationID]
	if !ok || op.BankID != bankID || op.Status != StatusPending {
		return false, nil
	}
	_, parked := op.Payload[AutomaticRefreshPayloadKey]
	if !parked && op.NextRetryAt == nil {
		return false, nil
	}
	delete(op.Payload, AutomaticRefreshPayloadKey)
	op.NextRetryAt = nil
	op.UpdatedAt = time.Now().UTC()
	s.operations[operationID] = op
	return true, nil
}

// withKnowledgePageFlag resolves a read record's is_knowledge_page flag from the
// knowledge tree: a page carries its content in a mental model, so a model any
// page points at is that page's. Called with s.mu held for reading.
func (s *MemoryStore) withKnowledgePageFlag(bankID string, model MentalModel) MentalModel {
	backed := false
	for _, node := range s.knowledgeNodes {
		if node.BankID == bankID && node.MentalModelID == model.ID {
			backed = true
			break
		}
	}
	model.IsKnowledgePage = &backed
	return model
}

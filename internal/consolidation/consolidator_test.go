package consolidation

import (
	"context"
	"errors"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// stubProvider returns a scripted completion so the pass can be exercised
// without a live model.
type stubProvider struct {
	name     string
	response provider.CompletionResult
	err      error
	last     provider.CompletionRequest
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	s.last = req
	return s.response, s.err
}
func (s *stubProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (s *stubProvider) VerifyConnection(context.Context) error { return nil }
func (s *stubProvider) Dimensions() int                        { return 384 }

// stubStore records what the pass applied.
type stubStore struct {
	facts        []Fact
	observations []Observation
	created      []Create
	updated      []Update
	deleted      []string
	marked       []string
}

func (s *stubStore) ConsolidationFacts(context.Context, string, int) ([]Fact, error) {
	return s.facts, nil
}
func (s *stubStore) ConsolidationObservations(context.Context, string, int) ([]Observation, error) {
	return s.observations, nil
}
func (s *stubStore) CreateObservation(_ context.Context, _, text string, sourceIDs []string) (string, error) {
	s.created = append(s.created, Create{Text: text, SourceIDs: sourceIDs})
	return "obs-new", nil
}
func (s *stubStore) UpdateObservation(_ context.Context, _, observationID, text string, sourceIDs []string) error {
	s.updated = append(s.updated, Update{ObservationID: observationID, Text: text, SourceIDs: sourceIDs})
	return nil
}
func (s *stubStore) DeleteObservation(_ context.Context, _, observationID string) error {
	s.deleted = append(s.deleted, observationID)
	return nil
}
func (s *stubStore) MarkConsolidated(_ context.Context, _ string, factIDs []string) error {
	s.marked = append(s.marked, factIDs...)
	return nil
}

// The pass applies a create, an update and a delete, and marks only the facts a
// decision actually consumed.
func TestConsolidatorAppliesDecision(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice owns a Honda"}, {ID: "f2", Text: "Alice sold it"}},
		observations: []Observation{{ID: "o1", Text: "Alice owns a Honda", ProofCount: 1}},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "Alice works late", "source_fact_ids": []any{"f2"}}},
			"updates": []any{map[string]any{"observation_id": "o1", "text": "Alice sold her Honda", "source_fact_ids": []any{"f1"}}},
			"deletes": []any{map[string]any{"observation_id": "o9"}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Created != 1 || result.Updated != 1 || result.Deleted != 1 {
		t.Fatalf("result = %+v, want one of each", result)
	}
	if len(store.created) != 1 || store.created[0].Text != "Alice works late" {
		t.Fatalf("created = %+v", store.created)
	}
	if len(store.updated) != 1 || store.updated[0].ObservationID != "o1" {
		t.Fatalf("updated = %+v", store.updated)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "o9" {
		t.Fatalf("deleted = %+v", store.deleted)
	}
	// Both facts were referenced, so both are marked as folded.
	if len(store.marked) != 2 {
		t.Fatalf("marked = %v, want both facts", store.marked)
	}
}

// An invented source identifier is dropped, so an observation cannot be linked
// to a fact that was never offered.
func TestConsolidatorDropsUnknownSourceIDs(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "an observation", "source_fact_ids": []any{"invented"}}},
		}},
	}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.created) != 1 || len(store.created[0].SourceIDs) != 0 {
		t.Fatalf("created = %+v, want no source ids", store.created)
	}
	// Nothing referenced the real fact, so it stays for the next pass.
	if len(store.marked) != 0 {
		t.Fatalf("marked = %v, want none", store.marked)
	}
}

// With no model configured the pass refuses rather than inventing observations.
func TestConsolidatorRefusesWithoutModel(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}
	model := &stubProvider{name: "none"}
	if _, err := New(model, store).Run(context.Background(), "b1"); !errors.Is(err, ErrNoModel) {
		t.Fatalf("error = %v, want ErrNoModel", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created = %+v, want none", store.created)
	}
}

// A bank with no unconsumed facts makes no model call at all.
func TestConsolidatorSkipsWhenNoFacts(t *testing.T) {
	store := &stubStore{}
	model := &stubProvider{name: "openai"}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Created != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("result = %+v, want nothing applied", result)
	}
	if model.last.Messages != nil {
		t.Fatal("no facts should not reach the model")
	}
}

// A delete entry without a target observation is skipped rather than guessed
// at, because it names nothing to remove.
func TestConsolidatorSkipsUntargetedDelete(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "an observation", "source_fact_ids": []any{"f1"}}},
			"deletes": []any{map[string]any{"reason": "no id supplied"}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Deleted != 0 || len(store.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", store.deleted)
	}
}

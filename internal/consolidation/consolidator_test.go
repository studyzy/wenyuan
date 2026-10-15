package consolidation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// stubProvider returns a scripted completion so the pass can be exercised
// without a live model.
type stubProvider struct {
	mu       sync.Mutex
	name     string
	response provider.CompletionResult
	err      error
	last     provider.CompletionRequest
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	s.mu.Lock()
	s.last = req
	s.mu.Unlock()
	return s.response, s.err
}
func (s *stubProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (s *stubProvider) VerifyConnection(context.Context) error { return nil }
func (s *stubProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// lastRequest returns the most recent completion request, so a test can assert
// on the prompt the model was actually sent.
func (s *stubProvider) lastRequest() provider.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// stubStore records what the pass applied.
type stubStore struct {
	mu            sync.Mutex
	facts         []Fact
	observations  []Observation
	related       []Observation
	askedRelated  bool
	created       []Create
	createdScopes [][]string
	updated       []Update
	deleted       []string
	marked        []string
	failed        []string
	config        map[string]any
	poolLimit     int
}

func (s *stubStore) ConsolidationFacts(context.Context, string, int) ([]Fact, error) {
	return s.facts, nil
}
func (s *stubStore) ConsolidationObservations(_ context.Context, _ string, scope []string, limit int) ([]Observation, error) {
	s.mu.Lock()
	s.poolLimit = limit
	s.mu.Unlock()
	var scoped []Observation
	for _, observation := range s.observations {
		if sameScope(observation.Scope, scope) {
			scoped = append(scoped, observation)
		}
	}
	return scoped, nil
}
func (s *stubStore) CreateObservation(_ context.Context, _, text string, sourceIDs, scope []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, Create{Text: text, SourceIDs: sourceIDs})
	s.createdScopes = append(s.createdScopes, scope)
	return "obs-new", nil
}
func (s *stubStore) UpdateObservation(_ context.Context, _, observationID, text string, sourceIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated = append(s.updated, Update{ObservationID: observationID, Text: text, SourceIDs: sourceIDs})
	return nil
}
func (s *stubStore) DeleteObservation(_ context.Context, _, observationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, observationID)
	return nil
}
func (s *stubStore) MarkConsolidated(_ context.Context, _ string, factIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked = append(s.marked, factIDs...)
	return nil
}
func (s *stubStore) MarkConsolidationFailed(_ context.Context, _ string, factIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, factIDs...)
	return nil
}
func (s *stubStore) RelatedObservations(context.Context, string, []string, []string, int) ([]Observation, error) {
	s.askedRelated = true
	return s.related, nil
}
func (s *stubStore) BankConfig(context.Context, string) (map[string]any, error) {
	return s.config, nil
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

// A create that restates an existing observation's text is dropped, so a twin
// observation does not accumulate.
func TestConsolidatorSkipsDuplicateCreate(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice owns a Honda"}},
		observations: []Observation{{ID: "o1", Text: "Alice owns a Honda"}},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "alice   owns a honda", "source_fact_ids": []any{"f1"}}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want the duplicate create skipped", result)
	}
	if len(store.created) != 0 {
		t.Fatalf("a duplicate must not be written: %+v", store.created)
	}
}

// A create that nearly restates an existing observation is dropped as a
// near-duplicate, not only an exact one.
func TestConsolidatorSkipsNearDuplicateCreate(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice owns a Honda"}},
		observations: []Observation{{ID: "o1", Text: "Alice owns a red Honda car"}},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "Alice owns the red Honda car", "source_fact_ids": []any{"f1"}}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want the near-duplicate skipped", result)
	}
}

// A near-identical twin that the recency-ordered pool did not contain is looked
// up and offered as a merge candidate, and is not added twice.
func TestConsolidatorWidensPoolWithRelatedObservations(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice prefers tea"}},
		observations: []Observation{{ID: "o1", Text: "unrelated conclusion"}},
		related: []Observation{
			{ID: "o2", Text: "Alice prefers tea over coffee", ProofCount: 3},
			{ID: "o1", Text: "unrelated conclusion"},
		},
	}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if !store.askedRelated {
		t.Fatal("the near-duplicate lookup must run")
	}
	// The prompt offered both the pooled and the related observation, once each.
	user := model.last.Messages[len(model.last.Messages)-1].Content
	if strings.Count(user, "Alice prefers tea over coffee") != 1 {
		t.Fatalf("the related observation must be offered exactly once: %s", user)
	}
	if strings.Count(user, "unrelated conclusion") != 1 {
		t.Fatalf("a pooled observation must not be duplicated: %s", user)
	}
}

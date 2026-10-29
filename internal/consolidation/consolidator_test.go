package consolidation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
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
	// scopeCount is what CountObservationsInScope answers: the scope's true
	// observation total, which the cap is measured against.
	scopeCount int
	// factsLimit records the load bound the pass asked for, so a test can
	// assert the batch size the deployment configured.
	factsLimit int
}

func (s *stubStore) ConsolidationFacts(_ context.Context, _ string, limit int) ([]Fact, error) {
	s.mu.Lock()
	s.factsLimit = limit
	excluded := make(map[string]bool, len(s.marked)+len(s.failed))
	for _, id := range s.marked {
		excluded[id] = true
	}
	for _, id := range s.failed {
		// A real store's candidate read excludes failed facts too, so a batch
		// the model could not answer is not re-fed within the same run.
		excluded[id] = true
	}
	var facts []Fact
	for _, fact := range s.facts {
		if !excluded[fact.ID] {
			facts = append(facts, fact)
		}
	}
	if len(facts) > limit {
		facts = facts[:limit]
	}
	s.mu.Unlock()
	return facts, nil
}
func (s *stubStore) CountObservationsInScope(_ context.Context, _ string, scope []string) (int, error) {
	// A positive scopeCount overrides; otherwise the stub counts its own
	// observations at the scope, the way a real store does.
	if s.scopeCount > 0 {
		return s.scopeCount, nil
	}
	count := 0
	for _, observation := range s.observations {
		if sameScope(observation.Scope, scope) {
			count++
		}
	}
	return count, nil
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

// uniqueMarked counts the distinct facts stamped consolidated, so a re-asked
// sub-batch's idempotent re-stamp does not read as a second fact.
func uniqueMarked(marked []string) int {
	seen := make(map[string]bool, len(marked))
	for _, id := range marked {
		seen[id] = true
	}
	return len(seen)
}

// The pass applies a create, an update and a delete, and marks the facts of
// every batch it answered — the whole batch is stamped once the model has
// spoken, whether or not the decision cited each fact.
func TestConsolidatorAppliesDecision(t *testing.T) {
	store := &stubStore{
		facts: []Fact{{ID: "f1", Text: "Alice owns a Honda"}, {ID: "f2", Text: "Alice sold it"}},
		observations: []Observation{
			{ID: "o1", Text: "Alice owns a Honda", ProofCount: 1},
			{ID: "o2", Text: "Alice wants a sedan", ProofCount: 1},
		},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "Alice works late", "source_fact_ids": []any{"f2"}}},
			"updates": []any{map[string]any{"observation_id": "o1", "text": "Alice sold her Honda", "source_fact_ids": []any{"f1"}}},
			"deletes": []any{map[string]any{"observation_id": "o2"}},
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
	if len(store.deleted) != 1 || store.deleted[0] != "o2" {
		t.Fatalf("deleted = %+v", store.deleted)
	}
	// Both facts were referenced, so both are marked as folded.
	if len(store.marked) != 2 {
		t.Fatalf("marked = %v, want both facts", store.marked)
	}
}

// An invented source identifier is dropped, so an observation cannot be linked
// to a fact that was never offered. The batch is still stamped: the model
// answered it, and a single-fact batch is never re-asked.
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
	if len(store.marked) != 1 || store.marked[0] != "f1" {
		t.Fatalf("marked = %v, want the answered batch stamped", store.marked)
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

// tuningStore is a stub store that also carries a deployment's consolidation
// tuning, so a test can assert how the pass reads it.
type tuningStore struct {
	*stubStore
	tuning storage.ConsolidationTuning
}

func (s *tuningStore) ConsolidationTuning() storage.ConsolidationTuning { return s.tuning }

// flakyProvider fails the first failures calls, then answers, so a test can
// assert the attempt budget without a live model.
type flakyProvider struct {
	stubProvider
	failures int
	calls    int
}

func (p *flakyProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	p.calls++
	if p.calls <= p.failures {
		return provider.CompletionResult{}, errors.New("model unavailable")
	}
	return p.stubProvider.Complete(ctx, req)
}

// test-consolidation-load-batch-size: the batch size the deployment configured
// bounds how many memories one load carries, so the pass reads the bank in that
// size rather than pulling the whole backlog at once.
func TestConsolidatorLoadBatchSizeFollowsTuning(t *testing.T) {
	store := &tuningStore{
		stubStore: &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}},
		tuning:    storage.ConsolidationTuning{MemoryBatchSize: 7, MaxAttempts: 1},
	}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if store.factsLimit != 7 {
		t.Fatalf("load batch = %d, want the configured 7", store.factsLimit)
	}
}

// test-consolidation-load-batch-size-default: an unset batch size keeps the
// reference's default rather than an unbounded load.
func TestConsolidatorLoadBatchSizeDefaultsToReference(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if store.factsLimit != storage.DefaultConsolidationMemoryBatchSize {
		t.Fatalf("load batch = %d, want the default %d", store.factsLimit, storage.DefaultConsolidationMemoryBatchSize)
	}
}

// test-consolidation-max-completion-tokens: the configured cap reaches the
// model call; an unset cap leaves the parameter off.
func TestConsolidatorModelCallUsesCompletionCap(t *testing.T) {
	store := &tuningStore{
		stubStore: &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}},
		tuning:    storage.ConsolidationTuning{MaxCompletionTokens: 128, MaxAttempts: 1},
	}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if got := model.lastRequest().MaxTokens; got != 128 {
		t.Fatalf("max tokens = %d, want the configured 128", got)
	}

	unset := &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}
	uncapped := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(uncapped, unset).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if got := uncapped.lastRequest().MaxTokens; got != 0 {
		t.Fatalf("max tokens = %d, want the parameter left unset", got)
	}
}

// test-consolidation-max-attempts: a transient model failure is retried up to
// the configured attempt budget and then succeeds.
func TestConsolidatorRetriesModelCallWithinAttemptBudget(t *testing.T) {
	store := &tuningStore{
		stubStore: &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}},
		tuning:    storage.ConsolidationTuning{MaxAttempts: 3},
	}
	model := &flakyProvider{failures: 1, stubProvider: stubProvider{
		name:     "openai",
		response: provider.CompletionResult{Structured: map[string]any{}},
	}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want a retry then a success", model.calls)
	}
}

// test-consolidation-max-attempts-exhausted: a model that never answers is
// asked exactly the configured number of times, and the batch is then failed.
func TestConsolidatorStopsAtAttemptBudget(t *testing.T) {
	store := &tuningStore{
		stubStore: &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}},
		tuning:    storage.ConsolidationTuning{MaxAttempts: 3},
	}
	model := &flakyProvider{failures: 10}
	model.name = "openai"
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 3 {
		t.Fatalf("model calls = %d, want the configured budget of 3", model.calls)
	}
	if result.Failed != 1 {
		t.Fatalf("failed = %d, want the batch recorded as failed", result.Failed)
	}
}

// Contract reference: engine/memory_engine.py:5139-5179
// (_write_operation_progress) and consolidation/consolidator.py:1585-1586,
// 2169-2175. A pass reports coarse snapshots — an initial "consolidating" 0/N
// with no counters, one per scope carrying the running counters and the
// per-outcome detail, then "refreshing_mental_models" — so an operator polling
// the status route can tell a healthy long-running job from a frozen one.
func TestConsolidatorReportsPhaseSnapshots(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice owns a Honda"}},
		observations: []Observation{},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "Alice owns a Honda", "source_fact_ids": []any{"f1"}}},
		}},
	}
	var snapshots []map[string]any
	consolidator := New(model, store).WithOperation("op-1", func(_ string, snapshot map[string]any) {
		snapshots = append(snapshots, snapshot)
	})
	if _, err := consolidator.Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(snapshots) < 3 {
		t.Fatalf("snapshots = %d, want the initial, per-scope and final ones: %v", len(snapshots), snapshots)
	}

	first := snapshots[0]
	if first["stage"] != "consolidating" || first["processed"] != 0 || first["total"] != 1 {
		t.Fatalf("first snapshot = %v, want consolidating 0/1", first)
	}
	if first["detail"] != nil {
		t.Fatalf("first snapshot detail = %v, want null", first["detail"])
	}
	// The timestamp is the reference's `datetime.now(UTC).isoformat()`: a
	// dict-valued member, so `+00:00` rather than a model field's `Z`.
	if at, _ := first["at"].(string); !strings.HasSuffix(at, "+00:00") {
		t.Fatalf("at = %q, want a +00:00 offset", at)
	}

	scope := snapshots[1]
	detail, ok := scope["detail"].(map[string]int)
	if !ok || detail["observations_created"] != 1 {
		t.Fatalf("scope snapshot detail = %v, want observations_created 1", scope["detail"])
	}

	last := snapshots[len(snapshots)-1]
	if last["stage"] != "refreshing_mental_models" || last["processed"] != 1 || last["total"] != 1 {
		t.Fatalf("last snapshot = %v, want refreshing_mental_models 1/1", last)
	}
}

// A pass with no operation to report to writes nothing, which is what a
// maintenance job or a test runs as.
func TestConsolidatorWithoutOperationReportsNothing(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice owns a Honda"}},
		observations: []Observation{},
	}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).WithOperation("", func(string, map[string]any) {
		t.Fatal("an unattributed pass must not report")
	}).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// The scope cap is measured against the scope's TRUE observation count, not
// the bounded candidate pool: a scope holding more observations than the pool
// reads would otherwise grow past its cap (the reference's count-to-the-end
// fix, engine/memories/base.py:3838-3857, consolidator.py:2555-2560).
func TestScopeCapMeasuredAgainstTheTrueCount(t *testing.T) {
	model := &stubProvider{response: provider.CompletionResult{Structured: map[string]any{
		"creates": []any{map[string]any{"text": "Alice works late", "source_fact_ids": []any{"f1"}}},
	}}}
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice works late"}},
		observations: []Observation{{ID: "o1", Text: "Alice drives", Scope: []string{}}, {ID: "o2", Text: "Alice skis", Scope: []string{}}},
		scopeCount:   5,
		config:       map[string]any{"max_observations_per_scope": 5},
	}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	user := model.lastRequest().Messages[len(model.lastRequest().Messages)-1].Content
	if !strings.Contains(user, "OBSERVATION LIMIT REACHED (5/5)") {
		t.Fatalf("prompt does not report the true-count cap:\n%s", user)
	}
}

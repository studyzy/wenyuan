package extraction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// stubProvider is a provider that returns canned structured output.
type stubProvider struct {
	name       string
	structured any
	err        error
	lastReq    provider.CompletionRequest
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	s.lastReq = req
	if s.err != nil {
		return provider.CompletionResult{}, s.err
	}
	return provider.CompletionResult{Structured: s.structured}, nil
}

func (s *stubProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (s *stubProvider) VerifyConnection(context.Context) error { return nil }
func (s *stubProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// test-no-provider-uses-the-deterministic-fallback: with no model configured
// the extraction segments the text itself, so a deployment without a provider
// keeps storing memories.
func TestNoProviderUsesDeterministicFallback(t *testing.T) {
	extractor := New(nil)
	if extractor.Available() {
		t.Fatal("an extractor with no provider must report itself unavailable")
	}
	result, err := extractor.Extract(context.Background(), "The deployment runs on Tuesdays. It starts at noon.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Extracted {
		t.Fatal("the fallback must not claim a model produced these facts")
	}
	if len(result.Facts) != 2 {
		t.Fatalf("fallback produced %d facts, want 2: %v", len(result.Facts), result.Facts)
	}
	if result.Facts[0].Text != "The deployment runs on Tuesdays." {
		t.Fatalf("first fact = %q", result.Facts[0].Text)
	}
}

// test-none-provider-is-treated-as-unconfigured: the no-model selection is a
// real provider, so availability is decided by name rather than by absence.
func TestNoneProviderIsUnavailable(t *testing.T) {
	extractor := New(&stubProvider{name: "none"})
	if extractor.Available() {
		t.Fatal("the no-model selection must report itself unavailable")
	}
	result, err := extractor.Extract(context.Background(), "One sentence.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Extracted {
		t.Fatal("the no-model selection must take the fallback path")
	}
}

// test-model-facts-are-read: a configured provider's structured output becomes
// facts, including entities and confidence.
func TestModelFactsAreRead(t *testing.T) {
	stub := &stubProvider{
		name: "openai",
		structured: map[string]any{
			"facts": []any{
				map[string]any{
					"text":       "The deployment runs on Tuesdays.",
					"entities":   []any{"Tuesday", "deployment"},
					"confidence": 0.9,
				},
			},
		},
	}
	extractor := New(stub)
	if !extractor.Available() {
		t.Fatal("a configured provider must report itself available")
	}
	result, err := extractor.Extract(context.Background(), "We deploy on Tuesdays.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Extracted || len(result.Facts) != 1 {
		t.Fatalf("result = %+v", result)
	}
	fact := result.Facts[0]
	if fact.Text != "The deployment runs on Tuesdays." {
		t.Fatalf("fact text = %q", fact.Text)
	}
	if len(fact.Entities) != 2 {
		t.Fatalf("entities = %v", fact.Entities)
	}
	if fact.Confidence != 0.9 {
		t.Fatalf("confidence = %v", fact.Confidence)
	}
	// The call must ask for structured output, so a model cannot answer prose.
	if stub.lastReq.ResponseSchema == nil {
		t.Fatal("the extraction call must request a response schema")
	}
	if stub.lastReq.Scope != "retain_extract_facts" {
		t.Fatalf("scope = %q, want retain_extract_facts", stub.lastReq.Scope)
	}
}

// test-facts-without-text-are-dropped: a fact carrying no text is unusable, so
// it is dropped rather than stored empty.
func TestFactsWithoutTextAreDropped(t *testing.T) {
	stub := &stubProvider{
		name: "openai",
		structured: map[string]any{
			"facts": []any{
				map[string]any{"text": "  "},
				map[string]any{"text": "A real fact."},
				map[string]any{"entities": []any{"orphan"}},
			},
		},
	}
	result, err := New(stub).Extract(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Text != "A real fact." {
		t.Fatalf("facts = %+v", result.Facts)
	}
}

// test-confidence-is-clamped: a confidence outside the unit interval is not a
// confidence, so it is clamped rather than stored.
func TestConfidenceIsClamped(t *testing.T) {
	stub := &stubProvider{
		name: "openai",
		structured: map[string]any{
			"facts": []any{
				map[string]any{"text": "high", "confidence": 5},
				map[string]any{"text": "low", "confidence": -3},
			},
		},
	}
	result, err := New(stub).Extract(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	if result.Facts[0].Confidence != 1 {
		t.Fatalf("an over-range confidence must clamp to 1, got %v", result.Facts[0].Confidence)
	}
	if result.Facts[1].Confidence != 0 {
		t.Fatalf("a negative confidence must clamp to 0, got %v", result.Facts[1].Confidence)
	}
}

// test-model-failure-is-returned: a failed call is surfaced rather than
// silently replaced by the fallback, because the caller decides how to react.
func TestModelFailureIsReturned(t *testing.T) {
	stub := &stubProvider{name: "openai", err: errors.New("provider unavailable")}
	_, err := New(stub).Extract(context.Background(), "text")
	if err == nil {
		t.Fatal("a provider failure must be returned")
	}
}

// test-empty-text-is-refused: extraction of nothing is a caller error.
func TestEmptyTextIsRefused(t *testing.T) {
	if _, err := New(nil).Extract(context.Background(), "   "); err == nil {
		t.Fatal("empty text must be refused")
	}
}

// test-facts-are-bounded: a verbose model cannot turn one write into an
// unbounded number of units.
func TestFactsAreBounded(t *testing.T) {
	raw := make([]any, 0, maxFactsPerItem*2)
	for i := 0; i < maxFactsPerItem*2; i++ {
		raw = append(raw, map[string]any{"text": "fact"})
	}
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": raw}}
	result, err := New(stub).Extract(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != maxFactsPerItem {
		t.Fatalf("facts = %d, want the bound %d", len(result.Facts), maxFactsPerItem)
	}
}

// Causal relations are read from the model's answer, keeping only the edges
// that point at an earlier fact in the same list.
func TestCausalRelationsResolveToEarlierFacts(t *testing.T) {
	raw := []any{
		map[string]any{"text": "lost the job"},
		map[string]any{"text": "could not pay rent", "causal_relations": []any{
			map[string]any{"target_index": float64(0)},
		}},
		map[string]any{"text": "moved apartment", "causal_relations": []any{
			map[string]any{"target_index": float64(1)},
			map[string]any{"target_index": float64(9)}, // out of range
			map[string]any{"target_index": float64(2)}, // points at itself
		}},
	}
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": raw}}
	result, err := New(stub).Extract(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 3 {
		t.Fatalf("facts = %d, want 3", len(result.Facts))
	}
	if got := result.Facts[0].CausalRelations; got != nil {
		t.Fatalf("a first fact cannot be caused by anything: %v", got)
	}
	if got := result.Facts[1].CausalRelations; len(got) != 1 || got[0].TargetIndex != 0 {
		t.Fatalf("fact 1 causes = %v, want one edge to 0", got)
	}
	if got := result.Facts[2].CausalRelations; len(got) != 1 || got[0].TargetIndex != 1 {
		t.Fatalf("fact 2 causes = %v, want one edge to 1", got)
	}
}

// A fact may declare at most the contract's cap of causal edges.
func TestCausalRelationsAreCapped(t *testing.T) {
	relations := []any{
		map[string]any{"target_index": float64(0)},
		map[string]any{"target_index": float64(1)},
		map[string]any{"target_index": float64(2)},
		map[string]any{"target_index": float64(3)},
	}
	raw := make([]any, 0, 5)
	for i := 0; i < 4; i++ {
		raw = append(raw, map[string]any{"text": "earlier"})
	}
	raw = append(raw, map[string]any{"text": "effect", "causal_relations": relations})
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": raw}}
	result, err := New(stub).Extract(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.Facts[4].CausalRelations); got != maxCausalRelationsPerFact {
		t.Fatalf("causal relations = %d, want the cap %d", got, maxCausalRelationsPerFact)
	}
}

// A punctuation-only "fact" is dropped, an experience fact_type survives, and
// the model's "assistant" perspective is passed through rather than collapsed
// to world, so the storage mapping can resolve it.
func TestParseFactsFiltersDegenerateAndKeepsType(t *testing.T) {
	structured := map[string]any{"facts": []any{
		map[string]any{"text": "..."},
		map[string]any{"text": "Alice moved to Berlin.", "where": "Berlin", "fact_kind": "event", "fact_type": "experience"},
		map[string]any{"text": "I checked the deployment log.", "fact_type": "assistant"},
	}}
	facts, err := parseFacts(structured, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("degenerate fact must be dropped, got %d facts", len(facts))
	}
	if facts[0].Type != "experience" || facts[0].Where != "Berlin" || facts[0].Kind != "event" {
		t.Fatalf("extended fields not parsed: %+v", facts[0])
	}
	if facts[1].Type != "assistant" {
		t.Fatalf("assistant fact_type = %q, want it kept for the storage mapping", facts[1].Type)
	}
}

// sequenceProvider returns a different structured value per call.
type sequenceProvider struct {
	results []any
	calls   int
}

func (s *sequenceProvider) Name() string { return "openai" }
func (s *sequenceProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	if s.calls >= len(s.results) {
		return provider.CompletionResult{}, nil
	}
	value := s.results[s.calls]
	s.calls++
	return provider.CompletionResult{Structured: value}, nil
}
func (s *sequenceProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (s *sequenceProvider) VerifyConnection(context.Context) error { return nil }
func (s *sequenceProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// A malformed first response triggers exactly one self-correcting round.
func TestExtractRepairsMalformedResponse(t *testing.T) {
	provider := &sequenceProvider{results: []any{
		"not an object",
		map[string]any{"facts": []any{map[string]any{"text": "Alice works at Google."}}},
	}}
	result, err := New(provider).Extract(context.Background(), "Alice works at Google.")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 || provider.calls != 2 {
		t.Fatalf("repair did not recover facts: %+v (calls %d)", result, provider.calls)
	}
}

// causalAnswer is a model answer whose second fact declares a causal edge, used
// to observe whether the causal switch asks for and parses relations.
func causalAnswer() map[string]any {
	return map[string]any{"facts": []any{
		map[string]any{"text": "could not pay rent"},
		map[string]any{"text": "moved apartment", "causal_relations": []any{
			map[string]any{"target_index": float64(0), "relation_type": "caused_by"},
		}},
	}}
}

// causalFactSchema returns the fact object schema the last request carried, or
// nil when the request asked for no schema.
func causalFactSchema(req provider.CompletionRequest) *provider.Schema {
	if req.ResponseSchema == nil || req.ResponseSchema.Properties["facts"] == nil {
		return nil
	}
	return req.ResponseSchema.Properties["facts"].Items
}

// By default the extraction asks the model for causal relations, declares the
// field in the schema, and parses the relations it returns.
func TestCausalExtractionOnByDefault(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: causalAnswer()}
	result, err := New(stub).Extract(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.lastReq.Messages[0].Content, "CAUSAL RELATIONSHIPS") {
		t.Fatal("the default prompt must ask for causal relations")
	}
	fact := causalFactSchema(stub.lastReq)
	if fact == nil || fact.Properties["causal_relations"] == nil {
		t.Fatal("the default schema must declare causal_relations")
	}
	if len(result.Facts) != 2 || len(result.Facts[1].CausalRelations) != 1 {
		t.Fatalf("the default must parse causal relations: %+v", result.Facts)
	}
}

// With the switch off the extraction neither asks for causal relations nor
// parses them: the prompt carries no causal section, the schema declares no
// causal_relations field, and a model that answers with relations anyway yields
// facts with none.
func TestCausalExtractionDisabledDropsAskAndParse(t *testing.T) {
	off := false
	stub := &stubProvider{name: "openai", structured: causalAnswer()}
	result, err := New(stub).ExtractWith(context.Background(), "text", Options{ExtractCausalLinks: &off})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stub.lastReq.Messages[0].Content, "CAUSAL RELATIONSHIPS") {
		t.Fatal("a disabled switch must not ask for causal relations")
	}
	fact := causalFactSchema(stub.lastReq)
	if fact == nil {
		t.Fatal("the response schema is still sent")
	}
	if fact.Properties["causal_relations"] != nil {
		t.Fatal("a disabled switch must not declare causal_relations")
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(result.Facts))
	}
	if result.Facts[1].CausalRelations != nil {
		t.Fatalf("a disabled switch must not parse causal relations: %+v", result.Facts[1].CausalRelations)
	}
}

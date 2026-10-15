package consolidation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// scopedProvider records the fact identifiers of each model call and answers
// with one create per fact, so a test can see how facts were grouped and what
// scope each produced observation was written at.
type scopedProvider struct {
	calls [][]string
}

func (p *scopedProvider) Name() string { return "openai" }
func (p *scopedProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	ids := factIDsInFactSection(req.Messages[len(req.Messages)-1].Content)
	p.calls = append(p.calls, ids)
	creates := make([]any, 0, len(ids))
	for _, id := range ids {
		creates = append(creates, map[string]any{"text": "observation for " + id, "source_fact_ids": []any{id}})
	}
	return provider.CompletionResult{Structured: map[string]any{"creates": creates}}, nil
}
func (p *scopedProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (p *scopedProvider) VerifyConnection(context.Context) error { return nil }
func (p *scopedProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// factIDsInFactSection reads the "[id] text" identifiers from the NEW FACTS
// section of a built prompt.
func factIDsInFactSection(content string) []string {
	section := content
	if index := strings.Index(section, "### Existing observations"); index >= 0 {
		section = section[:index]
	}
	var ids []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		end := strings.Index(line, "]")
		if end <= 1 {
			continue
		}
		ids = append(ids, line[1:end])
	}
	return ids
}

// createdScopeSet is the set of scopes the pass wrote observations at.
func createdScopeSet(store *stubStore) map[string]bool {
	out := map[string]bool{}
	for _, scope := range store.createdScopes {
		out[scopeKey(canonicalTags(scope))] = true
	}
	return out
}

// callsContainingBoth counts the model calls that offered both identifiers, so
// a test can prove two facts did (or did not) fold together.
func callsContainingBoth(model *scopedProvider, a, b string) int {
	count := 0
	for _, call := range model.calls {
		hasA, hasB := false, false
		for _, id := range call {
			if id == a {
				hasA = true
			}
			if id == b {
				hasB = true
			}
		}
		if hasA && hasB {
			count++
		}
	}
	return count
}

// resolveScopes implements the reference's four scope modes over the fact's own
// tags, plus the explicit list form, and treats an unset or unrecognised value
// as the combined default.
func TestResolveScopesModes(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		raw  string
		want [][]string
	}{
		{"combined default", []string{"b", "a"}, "", [][]string{{"a", "b"}}},
		{"combined mode", []string{"a"}, `"combined"`, [][]string{{"a"}}},
		{"untagged", nil, "", [][]string{nil}},
		{"per_tag", []string{"a", "b"}, `"per_tag"`, [][]string{{"a"}, {"b"}}},
		{"per_tag untagged", nil, `"per_tag"`, [][]string{{}}},
		{"all_combinations", []string{"a", "b"}, `"all_combinations"`, [][]string{{"a"}, {"a", "b"}, {"b"}}},
		{"all_combinations untagged", nil, `"all_combinations"`, [][]string{{}}},
		{"all_combinations three tags", []string{"a", "b", "c"}, `"all_combinations"`,
			[][]string{{"a"}, {"a", "b"}, {"a", "b", "c"}, {"a", "c"}, {"b"}, {"b", "c"}, {"c"}}},
		{"shared", []string{"a", "b"}, `"shared"`, [][]string{{}}},
		{"explicit groups", nil, `[["y","x"],["z"]]`, [][]string{{"x", "y"}, {"z"}}},
		{"explicit empty falls back", []string{"a"}, `[]`, [][]string{{"a"}}},
		{"unknown mode is combined", []string{"a"}, `"bogus"`, [][]string{{"a"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveScopes(tc.tags, json.RawMessage(tc.raw))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolveScopes(%v, %s) = %v, want %v", tc.tags, tc.raw, got, tc.want)
			}
		})
	}
}

// The combined default folds each fact only with facts sharing its exact tag
// set, and the observation it produces carries that set.
func TestConsolidationCombinedScopesFactsByTags(t *testing.T) {
	store := &stubStore{facts: []Fact{
		{ID: "f1", Text: "one", Tags: []string{"a"}},
		{ID: "f2", Text: "two", Tags: []string{"b"}},
	}}
	model := &scopedProvider{}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 2 {
		t.Fatalf("calls = %v, want one per scope", model.calls)
	}
	if callsContainingBoth(model, "f1", "f2") != 0 {
		t.Fatalf("differently-scoped facts shared a call: %v", model.calls)
	}
	if scopes := createdScopeSet(store); !scopes["a"] || !scopes["b"] || len(scopes) != 2 {
		t.Fatalf("created scopes = %v, want a and b", scopes)
	}
}

// per_tag folds a fact once per tag: facts sharing a tag land together, facts
// that do not never do, and each observation carries the tag it was folded at.
func TestConsolidationPerTagFansOutOnePassPerTag(t *testing.T) {
	store := &stubStore{facts: []Fact{
		{ID: "f1", Text: "one", Tags: []string{"a", "b"}, ObservationScopes: json.RawMessage(`"per_tag"`)},
		{ID: "f2", Text: "two", Tags: []string{"b", "c"}, ObservationScopes: json.RawMessage(`"per_tag"`)},
	}}
	model := &scopedProvider{}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 3 {
		t.Fatalf("calls = %v, want one per tag", model.calls)
	}
	// f1 and f2 share tag b, so they fold together exactly there.
	if callsContainingBoth(model, "f1", "f2") != 1 {
		t.Fatalf("facts sharing a tag must fold together exactly once: %v", model.calls)
	}
	if scopes := createdScopeSet(store); !scopes["a"] || !scopes["b"] || !scopes["c"] || len(scopes) != 3 {
		t.Fatalf("created scopes = %v, want a, b and c", scopes)
	}
}

// all_combinations folds a fact once per non-empty subset of its tags, so two
// facts meet only in the subset they share.
func TestConsolidationAllCombinationsFoldsEverySubset(t *testing.T) {
	store := &stubStore{facts: []Fact{
		{ID: "f1", Text: "one", Tags: []string{"a", "b"}, ObservationScopes: json.RawMessage(`"all_combinations"`)},
		{ID: "f2", Text: "two", Tags: []string{"a", "c"}, ObservationScopes: json.RawMessage(`"all_combinations"`)},
	}}
	model := &scopedProvider{}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	// Subsets: a, ab, ac, b, c — one pass each.
	if len(model.calls) != 5 {
		t.Fatalf("calls = %v, want one per non-empty subset", model.calls)
	}
	if callsContainingBoth(model, "f1", "f2") != 1 {
		t.Fatalf("facts meeting only in scope a must share exactly one call: %v", model.calls)
	}
	want := map[string]bool{"a": true, "a\x1fb": true, "a\x1fc": true, "b": true, "c": true}
	if scopes := createdScopeSet(store); !reflect.DeepEqual(scopes, want) {
		t.Fatalf("created scopes = %v, want %v", scopes, want)
	}
}

// shared folds every fact into one untagged pass regardless of its own tags,
// and the observation carries the empty scope.
func TestConsolidationSharedFoldsAcrossTags(t *testing.T) {
	store := &stubStore{facts: []Fact{
		{ID: "f1", Text: "one", Tags: []string{"a"}, ObservationScopes: json.RawMessage(`"shared"`)},
		{ID: "f2", Text: "two", Tags: []string{"b"}, ObservationScopes: json.RawMessage(`"shared"`)},
	}}
	model := &scopedProvider{}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 1 || callsContainingBoth(model, "f1", "f2") != 1 {
		t.Fatalf("shared must fold differently-tagged facts together: %v", model.calls)
	}
	if scopes := createdScopeSet(store); !scopes[""] || len(scopes) != 1 {
		t.Fatalf("created scopes = %v, want only the untagged scope", scopes)
	}
}

// A shared fact folds only with other untagged-scope facts: a fact at a
// concrete scope, even one sharing the same tag, folds in its own pass.
func TestConsolidationSharedKeepsOtherScopesApart(t *testing.T) {
	store := &stubStore{facts: []Fact{
		{ID: "f1", Text: "one", Tags: []string{"a"}, ObservationScopes: json.RawMessage(`"shared"`)},
		{ID: "f2", Text: "two", Tags: []string{"a"}},
	}}
	model := &scopedProvider{}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 2 {
		t.Fatalf("calls = %v, want one per scope", model.calls)
	}
	if callsContainingBoth(model, "f1", "f2") != 0 {
		t.Fatalf("the shared and combined scopes must not share a call: %v", model.calls)
	}
	if scopes := createdScopeSet(store); !scopes[""] || !scopes["a"] || len(scopes) != 2 {
		t.Fatalf("created scopes = %v, want the untagged and a scopes", scopes)
	}
}

// A pass offers only the observations of its own scope as merge candidates, so
// the model cannot merge a fact into a differently-scoped observation.
func TestConsolidationOnlyOffersSameScopeObservations(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "one", Tags: []string{"b"}}},
		observations: []Observation{{ID: "o1", Text: "a conclusion", Scope: []string{"a"}}},
	}
	model := &singleResponseProvider{}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	user := model.last.Messages[len(model.last.Messages)-1].Content
	if strings.Contains(user, "o1") {
		t.Fatalf("a differently-scoped observation reached the prompt: %s", user)
	}
	if !strings.Contains(user, "### Existing observations\n\n[]") {
		t.Fatalf("the scope had no observations, so the pool must be empty: %s", user)
	}
}

// singleResponseProvider answers every call with no decision, recording the
// last request so a test can inspect what the model was offered.
type singleResponseProvider struct {
	last provider.CompletionRequest
}

func (p *singleResponseProvider) Name() string { return "openai" }
func (p *singleResponseProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	p.last = req
	return provider.CompletionResult{Structured: map[string]any{}}, nil
}
func (p *singleResponseProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (p *singleResponseProvider) VerifyConnection(context.Context) error { return nil }
func (p *singleResponseProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

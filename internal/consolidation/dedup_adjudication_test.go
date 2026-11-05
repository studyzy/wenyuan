package consolidation

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// foldCreateCall / foldUpdateCall record one conditional fold the pass asked a
// store to perform.
type foldCreateCall struct {
	twinID, expectedText, mergedText string
	sourceIDs                        []string
}

type foldUpdateCall struct {
	updatedID, updatedText, twinID, expectedTwinText, mergedText string
}

// verdictStore is a stub carrying all three optional capabilities the
// adjudication and the transaction guard use: the embedding probe, the
// conditional folds (gated on probe-time text) and the source revalidation.
type verdictStore struct {
	*stubStore
	tuning     storage.ConsolidationTuning
	probe      []float32
	candidates []Observation
	embedded   bool

	foldCreates []foldCreateCall
	foldUpdates []foldUpdateCall
	foldOK      bool

	changed         []string
	revalidateCalls int
	lastReadAt      map[string]string
}

func (s *verdictStore) ConsolidationTuning() storage.ConsolidationTuning { return s.tuning }

func (s *verdictStore) ObservationDuplicates(context.Context, string, []string, string, int) ([]float32, []Observation, bool, error) {
	return s.probe, s.candidates, s.embedded, nil
}

func (s *verdictStore) FoldCreateIntoDuplicate(_ context.Context, _, twinID, expectedText, mergedText string, sourceIDs []string) (bool, error) {
	s.foldCreates = append(s.foldCreates, foldCreateCall{twinID, expectedText, mergedText, sourceIDs})
	return s.foldOK, nil
}

func (s *verdictStore) FoldUpdateIntoDuplicate(_ context.Context, _, updatedID, updatedText, twinID, expectedTwinText, mergedText string) (bool, error) {
	s.foldUpdates = append(s.foldUpdates, foldUpdateCall{updatedID, updatedText, twinID, expectedTwinText, mergedText})
	return s.foldOK, nil
}

func (s *verdictStore) SourcesChangedSinceRead(_ context.Context, _ string, readAt map[string]string) ([]string, error) {
	s.revalidateCalls++
	s.lastReadAt = readAt
	return s.changed, nil
}

// newVerdictStore builds a store that always finds one in-scope twin at cosine
// 1 and whose folds succeed.
func newVerdictStore(candidates []Observation) *verdictStore {
	return &verdictStore{
		stubStore:  &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}},
		tuning:     storage.DefaultConsolidationTuning(),
		probe:      []float32{1, 0},
		candidates: candidates,
		embedded:   true,
		foldOK:     true,
	}
}

func twinCandidate() []Observation {
	return []Observation{{ID: "o1", Text: "Alice owns a red Honda", Embedding: []float32{1, 0}}}
}

// A cosine twin at/above the threshold is only a candidate: when the focused
// verdict says "keep" (a material difference — here a different colour), both
// observations stay. The create is NOT folded just because the embeddings
// matched. The old pass folded on cosine alone, so this is the regression.
func TestDedupKeepVerdictKeepsBothObservations(t *testing.T) {
	store := newVerdictStore(twinCandidate())
	model := &stubProvider{name: "openai", responses: []provider.CompletionResult{
		createDecision("Alice owns a blue Honda"),
		keepVerdictResp(),
	}}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want the create kept on a keep verdict", result)
	}
	if len(store.created) != 1 || store.created[0].Text != "Alice owns a blue Honda" {
		t.Fatalf("created = %+v, want the distinct observation written", store.created)
	}
	if len(store.foldCreates) != 0 {
		t.Fatalf("foldCreates = %+v, want no fold on a keep verdict", store.foldCreates)
	}
	if model.callCount() != 2 {
		t.Fatalf("completions = %d, want the main call plus the focused verdict", model.callCount())
	}
}

// An unusable focused answer — garbage or an unknown action — falls back to
// keep, so no distinct conclusion is ever lost to a malformed response.
func TestDedupUnusableVerdictFallsBackToKeep(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict provider.CompletionResult
	}{
		{"unparseable content", provider.CompletionResult{Content: "not a json object"}},
		{"unknown action", provider.CompletionResult{Structured: map[string]any{"action": "combine", "text": "x"}}},
		{"empty response", provider.CompletionResult{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newVerdictStore(twinCandidate())
			model := &stubProvider{name: "openai", responses: []provider.CompletionResult{
				createDecision("Alice owns a blue Honda"),
				tc.verdict,
			}}
			result, err := New(model, store).Run(context.Background(), "b1")
			if err != nil {
				t.Fatal(err)
			}
			if result.Created != 1 || result.Skipped != 0 {
				t.Fatalf("result = %+v, want keep on an unusable verdict", result)
			}
			if len(store.foldCreates) != 0 {
				t.Fatalf("foldCreates = %+v, want no fold", store.foldCreates)
			}
		})
	}
}

// On a merge verdict the create is folded through the conditional capability
// with the synthesized text, the twin's probe-time text (the concurrency gate)
// and the skipped create's source ids (so its temporal bounds widen the twin).
func TestDedupMergeUsesConditionalFoldWithProbeTimeText(t *testing.T) {
	store := newVerdictStore(twinCandidate())
	const merged = "Alice owns a red Honda and also a blue Honda"
	model := &stubProvider{name: "openai", responses: []provider.CompletionResult{
		createDecision("Alice owns a blue Honda"),
		mergeVerdictResp(merged),
	}}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Created != 0 {
		t.Fatalf("result = %+v, want the create folded", result)
	}
	if len(store.created) != 0 {
		t.Fatalf("created = %+v, want none", store.created)
	}
	// The fold went through the capability, not an ordinary observation write.
	if len(store.updated) != 0 {
		t.Fatalf("updated = %+v, want the conditional fold instead", store.updated)
	}
	if len(store.foldCreates) != 1 {
		t.Fatalf("foldCreates = %+v, want exactly one", store.foldCreates)
	}
	call := store.foldCreates[0]
	if call.twinID != "o1" || call.expectedText != "Alice owns a red Honda" || call.mergedText != merged {
		t.Fatalf("fold = %+v, want twin o1 gated on its probe text with the synthesized text", call)
	}
	if len(call.sourceIDs) != 1 || call.sourceIDs[0] != "f1" {
		t.Fatalf("fold sourceIDs = %v, want the skipped create's f1", call.sourceIDs)
	}
}

// When the twin vanished or was rewritten before the fold (the capability
// reports false), the create is executed after all so nothing is lost.
func TestDedupVanishedTwinFallsBackToCreate(t *testing.T) {
	store := newVerdictStore(twinCandidate())
	store.foldOK = false
	model := &stubProvider{name: "openai", responses: []provider.CompletionResult{
		createDecision("Alice owns a blue Honda"),
		mergeVerdictResp("a merged text"),
	}}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want the create kept when the fold cannot land", result)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %+v, want the create written", store.created)
	}
	if len(store.foldCreates) != 1 {
		t.Fatalf("foldCreates = %+v, want one fold attempt", store.foldCreates)
	}
}

// The UPDATE path folds symmetrically: a rewritten observation that drifted
// within the threshold of a different twin is folded into it and retired,
// gated on both rows' probe-time text.
func TestDedupUpdateMergesIntoTwin(t *testing.T) {
	store := newVerdictStore(twinCandidate())
	// The pool holds the observation the response updates; the probe returns a
	// different in-scope twin (o1), which the update must not self-match.
	store.observations = []Observation{{ID: "o0", Text: "Alice owns a car"}}
	const merged = "Alice owns a red Honda"
	model := &stubProvider{name: "openai", responses: []provider.CompletionResult{
		{Structured: map[string]any{
			"updates": []any{map[string]any{
				"observation_id": "o0", "text": "Alice owns a red Honda now", "source_fact_ids": []any{"f1"},
			}},
		}},
		mergeVerdictResp(merged),
	}}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 {
		t.Fatalf("result = %+v, want one folded update", result)
	}
	if len(store.updated) != 0 {
		t.Fatalf("updated = %+v, want the conditional fold instead", store.updated)
	}
	if len(store.foldUpdates) != 1 {
		t.Fatalf("foldUpdates = %+v, want exactly one", store.foldUpdates)
	}
	call := store.foldUpdates[0]
	if call.updatedID != "o0" || call.twinID != "o1" ||
		call.expectedTwinText != "Alice owns a red Honda" || call.mergedText != merged {
		t.Fatalf("fold = %+v, want o0 folded into o1 with the synthesized text", call)
	}
}

// Several updates naming one observation collapse to a single write and one
// history snapshot: the last text wins and all source ids are unioned in order.
// The old pass wrote each entry in turn, so this is the regression.
func TestConsolidatorDedupesRepeatedUpdateTargets(t *testing.T) {
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "a"}, {ID: "f2", Text: "b"}},
		observations: []Observation{{ID: "o1", Text: "old text"}},
	}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{
		"updates": []any{
			map[string]any{"observation_id": "o1", "text": "first text", "source_fact_ids": []any{"f1"}},
			map[string]any{"observation_id": "o1", "text": "last text", "source_fact_ids": []any{"f2", "f1"}},
		},
	}}}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 {
		t.Fatalf("result = %+v, want a single update", result)
	}
	if len(store.updated) != 1 {
		t.Fatalf("updated = %+v, want exactly one write / history snapshot", store.updated)
	}
	written := store.updated[0]
	if written.Text != "last text" {
		t.Fatalf("text = %q, want the last text", written.Text)
	}
	if len(written.SourceIDs) != 2 || written.SourceIDs[0] != "f1" || written.SourceIDs[1] != "f2" {
		t.Fatalf("sourceIDs = %v, want the ordered union [f1 f2]", written.SourceIDs)
	}
}

// A source fact edited since the pass read it makes the guard drop the whole
// response: no observation writes, no consolidated stamp. The old pass had no
// guard and would have written and stamped from the stale copy.
func TestConsolidatorDiscardsResponseWhenSourceEdited(t *testing.T) {
	store := newVerdictStore(nil)
	store.embedded = false
	store.facts = []Fact{{ID: "f1", Text: "a fact", UpdatedAt: "2026-01-01T00:00:00Z"}}
	store.changed = []string{"f1"}
	// Cap the round at one fetch so a deliberately unstamped batch is not
	// re-read again within this same pass.
	store.config = map[string]any{"consolidation_max_memories_per_round": 1}
	model := &stubProvider{name: "openai", response: createDecision("a conclusion")}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 0 || len(store.updated) != 0 || len(store.deleted) != 0 {
		t.Fatalf("writes = created:%v updated:%v deleted:%v, want none", store.created, store.updated, store.deleted)
	}
	if len(store.marked) != 0 {
		t.Fatalf("marked = %v, want no consolidated stamp", store.marked)
	}
	if result.Created != 0 || result.Skipped != 0 || result.Updated != 0 {
		t.Fatalf("result = %+v, want an empty discounted response", result)
	}
	if store.revalidateCalls != 1 {
		t.Fatalf("revalidateCalls = %d, want one guard call", store.revalidateCalls)
	}
	if store.lastReadAt["f1"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("readAt = %v, want the read-time stamp", store.lastReadAt)
	}
}

// When no source moved, the guard lets the response through: the observation
// is written and the batch is stamped.
func TestConsolidatorAppliesWhenSourcesUnchanged(t *testing.T) {
	store := newVerdictStore(nil)
	store.embedded = false
	store.facts = []Fact{{ID: "f1", Text: "a fact", UpdatedAt: "2026-01-01T00:00:00Z"}}
	store.changed = nil
	model := &stubProvider{name: "openai", response: createDecision("a conclusion")}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || len(store.created) != 1 {
		t.Fatalf("result = %+v, created = %+v, want the create applied", result, store.created)
	}
	if len(store.marked) != 1 || store.marked[0] != "f1" {
		t.Fatalf("marked = %v, want the batch stamped", store.marked)
	}
	if store.revalidateCalls != 1 {
		t.Fatalf("revalidateCalls = %d, want one guard call", store.revalidateCalls)
	}
}

// dedupeUpdates keeps the last text and the first-seen ordered union of source
// ids, preserving the order of each observation's first appearance.
func TestDedupeUpdates(t *testing.T) {
	updates := []Update{
		{ObservationID: "a", Text: "a1", SourceIDs: []string{"f1", "f2"}},
		{ObservationID: "b", Text: "b1", SourceIDs: []string{"f3"}},
		{ObservationID: "a", Text: "a2", SourceIDs: []string{"f2", "f4"}},
	}
	got := dedupeUpdates(updates)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].ObservationID != "a" || got[0].Text != "a2" {
		t.Fatalf("first = %+v, want a with the last text", got[0])
	}
	if len(got[0].SourceIDs) != 3 || got[0].SourceIDs[0] != "f1" || got[0].SourceIDs[1] != "f2" || got[0].SourceIDs[2] != "f4" {
		t.Fatalf("first sourceIDs = %v, want [f1 f2 f4]", got[0].SourceIDs)
	}
	if got[1].ObservationID != "b" {
		t.Fatalf("second = %+v, want b", got[1])
	}
}

// parseDedupVerdict accepts only a well-formed merge/keep action; anything else
// resolves to keep so an unreliable answer never folds distinct facts.
func TestParseDedupVerdict(t *testing.T) {
	for _, tc := range []struct {
		name       string
		structured any
		content    string
		wantAction string
		wantText   string
	}{
		{"structured merge", map[string]any{"action": "merge", "text": "combined"}, "", "merge", "combined"},
		{"structured keep", map[string]any{"action": "keep"}, "", "keep", ""},
		{"case-insensitive merge", map[string]any{"action": "MERGE", "text": "x"}, "", "merge", "x"},
		{"unknown action", map[string]any{"action": "delete"}, "", "keep", ""},
		{"prose merge", nil, "{\"action\":\"merge\",\"text\":\"from prose\"}", "merge", "from prose"},
		{"garbage", nil, "no object here", "keep", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action, text := parseDedupVerdict(tc.structured, tc.content)
			if action != tc.wantAction || text != tc.wantText {
				t.Fatalf("parse = (%q, %q), want (%q, %q)", action, text, tc.wantAction, tc.wantText)
			}
		})
	}
}

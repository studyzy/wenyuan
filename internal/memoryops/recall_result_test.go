package memoryops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ts-0008: an empty result builds empty side-channel collections, never null.
func TestBuildRecallResponseEmptyCollections(t *testing.T) {
	response := BuildRecallResponse(storage.RecallResult{}, RecallRequest{})
	if response.Results == nil {
		t.Fatal("results must be an empty slice")
	}
	if response.Trace != nil {
		t.Fatal("trace must be omitted when not requested")
	}
}

func TestBuildRecallResponseSideChannelsOnlyWhenIncluded(t *testing.T) {
	// Side channels are assembled by the store; an empty result yields no
	// channel even when the store reported none.
	response := BuildRecallResponse(storage.RecallResult{}, RecallRequest{})
	if response.Entities != nil {
		t.Fatalf("entities must be omitted when the store supplies none: %+v", response.Entities)
	}
}

func TestBuildRecallResponseTracePresentWhenRequested(t *testing.T) {
	source := storage.RecallResult{
		Matches: []storage.RecallMatch{{ID: "m1", Text: "t", Scores: map[string]float64{"semantic": 0.5}}},
		Trace: storage.RecallTrace{
			Budget: 4, BudgetUsed: 1, CandidatesFound: 4,
			StageCounts: map[string]int{"semantic": 1, "keyword": 0, "reranker": 0},
		},
	}
	response := BuildRecallResponse(source, RecallRequest{Query: "q", Trace: true})
	if response.Trace == nil {
		t.Fatal("trace must be present when requested")
	}
	query, ok := response.Trace["query"].(map[string]any)
	if !ok || query["query_text"] != "q" {
		t.Fatalf("trace must echo the query: %+v", response.Trace)
	}
	summary, ok := response.Trace["summary"].(map[string]any)
	if !ok {
		t.Fatalf("trace must carry a summary: %+v", response.Trace)
	}
	// The summary is the reference's SearchSummary (engine/search/trace.py:133-148):
	// the visit counts, the search duration and the ordered phase list — there is
	// no `candidates_found` or `stage_counts` member.
	if summary["results_returned"] != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	for _, member := range []string{"total_nodes_visited", "entry_points_found", "budget_used",
		"budget_remaining", "total_duration_seconds", "results_returned", "phase_metrics"} {
		if _, ok := summary[member]; !ok {
			t.Fatalf("summary must carry %q: %+v", member, summary)
		}
	}
	if _, ok := summary["phase_metrics"].([]any); !ok {
		t.Fatalf("phase_metrics must be a list, got %T", summary["phase_metrics"])
	}
}

func TestBuildRecallResponseMapsMatchFields(t *testing.T) {
	source := storage.RecallResult{Matches: []storage.RecallMatch{{
		ID:              "m1",
		Text:            "text one two",
		Type:            "world",
		Context:         "ctx",
		ChunkID:         "c1",
		DocumentID:      "d1",
		Metadata:        map[string]any{"k": "v"},
		SourceMemoryIDs: []string{"f1"},
		Scores:          map[string]float64{"semantic": 0.5},
		Entities:        []string{"e1"},
		Tags:            []string{"t1"},
		Final:           0.9,
	}}}
	response := BuildRecallResponse(source, RecallRequest{MaxTokens: intPtr(100)})
	if len(response.Results) != 1 {
		t.Fatalf("result count = %d", len(response.Results))
	}
	item := response.Results[0]
	if item.ID != "m1" || item.Text != "text one two" || item.Type == nil || *item.Type != "world" {
		t.Fatalf("mapped item = %+v", item)
	}
	if item.Scores.Final != 0.9 {
		t.Fatalf("final score not mapped: %+v", item.Scores)
	}
	if item.Scores.Semantic == nil || *item.Scores.Semantic != 0.5 {
		t.Fatalf("semantic stage not mapped: %+v", item.Scores)
	}
	if item.Metadata["k"] != "v" {
		t.Fatalf("metadata not preserved: %+v", item.Metadata)
	}
}

// The response must serialize the documented snake_case field names.
func TestRecallResponseSerializationFieldNames(t *testing.T) {
	response := BuildRecallResponse(storage.RecallResult{Matches: []storage.RecallMatch{{ID: "m1", Text: "t", Type: "world"}}}, RecallRequest{MaxTokens: intPtr(10)})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, field := range []string{`"results"`, `"id"`, `"text"`, `"type"`, `"scores"`, `"final"`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("serialized response missing %s: %s", field, raw)
		}
	}
}

// Every score stage is on the wire whether or not it surfaced the result: the
// reference keeps nulls for this route, so a client reading `scores.keyword`
// unconditionally never hits a missing key.
func TestRecallScoresCarryEveryStageIncludingNulls(t *testing.T) {
	source := storage.RecallResult{Matches: []storage.RecallMatch{{
		ID: "m1", Text: "t", Type: "world", Final: 0.4,
		Scores: map[string]float64{"semantic": 0.5},
	}}}
	response := BuildRecallResponse(source, RecallRequest{MaxTokens: intPtr(10)})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Results []struct {
			Scores map[string]json.RawMessage `json:"scores"`
		} `json:"results"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("result count = %d", len(decoded.Results))
	}
	scores := decoded.Results[0].Scores
	for _, stage := range []string{"final", "reranker", "semantic", "keyword"} {
		if _, ok := scores[stage]; !ok {
			t.Fatalf("scores must always carry %q: %s", stage, encoded)
		}
	}
	if got := string(scores["semantic"]); got != "0.5" {
		t.Fatalf("semantic = %s, want the reported score", got)
	}
	for _, stage := range []string{"reranker", "keyword"} {
		if got := string(scores[stage]); got != "null" {
			t.Fatalf("%s = %s, want null for a stage that did not surface the result", stage, got)
		}
	}
}

// ts-0005: an oversized fact skips only itself; shorter lower-ranked facts are
// still returned.
func TestSelectByTokenBudgetSkipsOversized(t *testing.T) {
	matches := []storage.RecallMatch{
		{ID: "a", Text: strings.Repeat("word ", 50)},
		{ID: "b", Text: "short one"},
		{ID: "c", Text: "short two"},
	}
	selected := applyTokenCap(matches, 10)
	ids := []string{}
	for _, m := range selected {
		ids = append(ids, m.ID)
	}
	if len(selected) != 2 || ids[0] != "b" || ids[1] != "c" {
		t.Fatalf("oversized fact must skip only itself: %v", ids)
	}
}

// ts-0006: a cap smaller than every fact returns the top fact whole.
func TestSelectByTokenBudgetReturnsTopOverBudget(t *testing.T) {
	matches := []storage.RecallMatch{
		{ID: "a", Text: strings.Repeat("word ", 50)},
		{ID: "b", Text: strings.Repeat("word ", 40)},
	}
	selected := applyTokenCap(matches, 5)
	if len(selected) != 1 || selected[0].ID != "a" {
		t.Fatalf("top fact must be returned over budget: %+v", selected)
	}
}

// ts-0007: a non-positive cap returns no facts.
func TestSelectByTokenBudgetNonPositive(t *testing.T) {
	matches := []storage.RecallMatch{{ID: "a", Text: "text"}}
	if got := applyTokenCap(matches, 0); len(got) != 0 {
		t.Fatalf("non-positive cap must return no facts: %+v", got)
	}
}

// A channel-level token budget clips a chunk and marks it truncated.
func TestCapChunkChannelTruncates(t *testing.T) {
	chunks := map[string]ChunkEntry{
		"c1": {ID: "c1", Text: "one two three four five six"},
	}
	capped := capChunkChannel(chunks, 3)
	entry := capped["c1"]
	if !entry.Truncated {
		t.Fatalf("an over-budget chunk must be marked truncated: %+v", entry)
	}
	if tokenCount(entry.Text) > 3 {
		t.Fatalf("clipped chunk has %d tokens, want <= 3", tokenCount(entry.Text))
	}
}

// recall-ft-4: a source-fact id list is reported only when the caller asked for
// source facts, and a fact that carries no entity postings reports entities as
// null rather than an empty list.
func TestBuildRecallResponseSourceFactIDsGated(t *testing.T) {
	source := storage.RecallResult{Matches: []storage.RecallMatch{{
		ID: "m1", Text: "t", Type: "observation", SourceMemoryIDs: []string{"s1"},
	}}}
	plain := BuildRecallResponse(source, RecallRequest{Query: "q"})
	if len(plain.Results) != 1 {
		t.Fatalf("results = %+v", plain.Results)
	}
	if plain.Results[0].SourceFactIDs != nil {
		t.Fatalf("source_fact_ids must be null unless source facts are included: %+v", plain.Results[0])
	}
	if plain.Results[0].Entities != nil {
		t.Fatalf("entities must be null when the fact carries none: %+v", plain.Results[0])
	}
	included := BuildRecallResponse(source, RecallRequest{Query: "q",
		Include: &IncludeRequest{SourceFacts: &IncludeSourceFactRequest{}}})
	if len(included.Results) != 1 || len(included.Results[0].SourceFactIDs) != 1 {
		t.Fatalf("source_fact_ids must be reported when source facts are included: %+v", included.Results)
	}
}

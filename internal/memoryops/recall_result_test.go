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
	response := BuildRecallResponse(storage.RecallResult{}, RecallRequest{Query: "q", Trace: true})
	if response.Trace == nil {
		t.Fatal("trace must be present when requested")
	}
	if response.Trace["query"] != "q" {
		t.Fatalf("trace must echo the query: %+v", response.Trace)
	}
}

func TestBuildRecallResponseMapsMatchFields(t *testing.T) {
	source := storage.RecallResult{Matches: []storage.RecallMatch{{
		ID:            "m1",
		Text:          "text one two",
		Type:          "world",
		Context:       "ctx",
		ChunkID:       "c1",
		DocumentID:    "d1",
		Metadata:      map[string]any{"k": "v"},
		SourceFactIDs: []string{"f1"},
		Scores:        map[string]float64{"semantic": 0.5},
		Entities:      []string{"e1"},
		Tags:          []string{"t1"},
		Final:         0.9,
	}}}
	response := BuildRecallResponse(source, RecallRequest{MaxTokens: intPtr(100)})
	if len(response.Results) != 1 {
		t.Fatalf("result count = %d", len(response.Results))
	}
	item := response.Results[0]
	if item.ID != "m1" || item.Text != "text one two" || item.Type != "world" {
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
	for _, field := range []string{`"results"`, `"id"`, `"text"`, `"type"`, `"truncated"`, `"scores"`, `"final"`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("serialized response missing %s: %s", field, raw)
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
	selected := applyTokenCap(matches, intPtr(10))
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
	selected := applyTokenCap(matches, intPtr(5))
	if len(selected) != 1 || selected[0].ID != "a" {
		t.Fatalf("top fact must be returned over budget: %+v", selected)
	}
}

// ts-0007: a non-positive cap returns no facts.
func TestSelectByTokenBudgetNonPositive(t *testing.T) {
	matches := []storage.RecallMatch{{ID: "a", Text: "text"}}
	if got := applyTokenCap(matches, intPtr(0)); len(got) != 0 {
		t.Fatalf("non-positive cap must return no facts: %+v", got)
	}
}

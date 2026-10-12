package memoryops

import (
	"encoding/json"
	"testing"
)

// The documented request fields decode into the typed shape.
func TestRecallRequestDecodesDocumentedFields(t *testing.T) {
	body := `{
		"query":"hello",
		"types":["world","observation"],
		"prefer_observations":true,
		"budget":"high",
		"max_tokens":256,
		"trace":true,
		"query_timestamp":"2026-01-02T03:04:05Z",
		"include":{"entities":{"max_tokens":10},"chunks":{"max_tokens":20},"source_facts":{"max_tokens":30,"max_tokens_per_observation":5}},
		"tags":["t1"],
		"tags_match":"all",
		"min_scores":{"semantic":0.5,"final":0.1},
		"temporal_window":{"start":"2026-01-01T00:00:00Z","end":"2026-12-31T00:00:00Z"}
	}`
	var req RecallRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if req.Query != "hello" || req.Budget != "high" || !req.Trace || !req.PreferObservations {
		t.Fatalf("decoded request = %+v", req)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 256 {
		t.Fatalf("max_tokens = %v", req.MaxTokens)
	}
	if req.Include == nil || req.Include.Chunks == nil || req.Include.Chunks.MaxTokens != 20 {
		t.Fatalf("include = %+v", req.Include)
	}
	if req.TemporalWindow == nil || req.TemporalWindow.End == "" {
		t.Fatalf("temporal window = %+v", req.TemporalWindow)
	}
	if len(req.Types) != 2 || len(req.Tags) != 1 || req.TagsMatch != "all" {
		t.Fatalf("list fields = %+v", req)
	}
}

// ts-0012: the compound tag-group shape nests leaf and boolean nodes and
// accepts the legacy list-of-tag-lists form.
func TestTagGroupsFieldAcceptsNestedAndLegacy(t *testing.T) {
	nested := `[{"and":[{"or":[{"tag":"a"},{"tag":"b"}]},{"not":[{"tag":"c"}]}]}]`
	var req RecallRequest
	if err := json.Unmarshal([]byte(`{"query":"q","tag_groups":`+nested+`}`), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.TagGroups.Groups) != 1 || len(req.TagGroups.Groups[0].And) != 2 {
		t.Fatalf("nested groups = %+v", req.TagGroups.Groups)
	}
	var legacy RecallRequest
	if err := json.Unmarshal([]byte(`{"query":"q","tag_groups":[["t1","t2"]]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy.TagGroups.Groups) != 1 || len(legacy.TagGroups.Groups[0].Tags) != 2 {
		t.Fatalf("legacy groups = %+v", legacy.TagGroups.Groups)
	}
}

// The include sub-object presence drives the side-channel defaulting.
func TestBuildRecallQueryIncludeDefaults(t *testing.T) {
	h := &Handlers{}
	query, err := h.buildRecallQuery("b1", RecallRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if !query.IncludeEntities {
		t.Fatal("entities must be included by default")
	}
	if query.IncludeChunks || query.IncludeSourceFacts {
		t.Fatal("chunks and source facts must be excluded by default")
	}
	explicit := &IncludeRequest{Entities: nil, Chunks: &IncludeChunkRequest{}}
	query2, err := h.buildRecallQuery("b1", RecallRequest{Query: "q", Include: explicit})
	if err != nil {
		t.Fatal(err)
	}
	if query2.IncludeEntities {
		t.Fatal("entities must be excluded when explicitly nulled")
	}
	if !query2.IncludeChunks {
		t.Fatal("chunks must be included when supplied")
	}
}

// The budget and tag-match defaults are applied when the request omits them.
func TestBuildRecallQueryDefaults(t *testing.T) {
	h := &Handlers{}
	query, err := h.buildRecallQuery("b1", RecallRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if query.Budget != "mid" {
		t.Fatalf("budget default = %q, want mid", query.Budget)
	}
	if query.TagsMatch != "any" {
		t.Fatalf("tags_match default = %q, want any", query.TagsMatch)
	}
}

// The strategy-boost map is threaded onto the storage query.
func TestBuildRecallQueryThreadsBoosts(t *testing.T) {
	h := &Handlers{RecallStrategyBoosts: map[string]string{"keyword": "high"}}
	query, err := h.buildRecallQuery("b1", RecallRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if query.StrategyBoosts["keyword"] != "high" {
		t.Fatalf("boosts not threaded: %+v", query.StrategyBoosts)
	}
}

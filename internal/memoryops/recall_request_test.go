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
	if req.Query != "hello" || req.Budget != "high" || !req.Trace || !req.PreferObservationsValue(false) {
		t.Fatalf("decoded request = %+v", req)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 256 {
		t.Fatalf("max_tokens = %v", req.MaxTokens)
	}
	if req.Include == nil || req.Include.Chunks == nil || req.Include.Chunks.MaxTokens == nil || *req.Include.Chunks.MaxTokens != 20 {
		t.Fatalf("include = %+v", req.Include)
	}
	if req.TemporalWindow == nil || req.TemporalWindow.End.IsZero() {
		t.Fatalf("temporal window = %+v", req.TemporalWindow)
	}
	if len(req.Types) != 2 || len(req.Tags) != 1 || req.TagsMatch != "all" {
		t.Fatalf("list fields = %+v", req)
	}
}

// ts-0012: the compound tag-group shape nests leaf and boolean nodes and
// accepts the leaf/compound object forms; a bare tag list is not one of them
// (the reference's union has no list branch).
func TestTagGroupsFieldAcceptsNestedAndLegacy(t *testing.T) {
	nested := `[{"and":[{"or":[{"tags":["a"]},{"tags":["b"]}]},{"not":{"tags":["c"]}}]}]`
	var req RecallRequest
	if err := json.Unmarshal([]byte(`{"query":"q","tag_groups":`+nested+`}`), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.TagGroups.Groups) != 1 || len(req.TagGroups.Groups[0].And) != 2 {
		t.Fatalf("nested groups = %+v", req.TagGroups.Groups)
	}
	// A bare tag list is refused, as a union item at each branch.
	var legacy RecallRequest
	if err := json.Unmarshal([]byte(`{"query":"q","tag_groups":[["t1","t2"]]}`), &legacy); err != nil {
		t.Fatalf("the field must decode its own failure rather than the body: %v", err)
	}
	if len(legacy.TagGroups.Groups) != 0 || len(legacy.TagGroups.failures) != 4 {
		t.Fatalf("a bare tag list must be a union failure: groups=%+v failures=%+v",
			legacy.TagGroups.Groups, legacy.TagGroups.failures)
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

// An include object that omits the entities key still includes entities: the
// reference's default-factory makes entities non-null unless explicitly nulled.
func TestRecallIncludeOmitsEntitiesStillIncludes(t *testing.T) {
	var req RecallRequest
	if err := json.Unmarshal([]byte(`{"query":"q","include":{"chunks":{}}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Include.Entities == nil {
		t.Fatal("omitted entities key must default to include")
	}
	query, err := (&Handlers{}).buildRecallQuery("b1", req)
	if err != nil {
		t.Fatal(err)
	}
	if !query.IncludeEntities {
		t.Fatal("include.entities omitted must mean include, like the reference default")
	}
	if !query.IncludeChunks {
		t.Fatal("supplied chunks must be included")
	}

	// An explicit null still disables entity inclusion.
	var nulled RecallRequest
	if err := json.Unmarshal([]byte(`{"query":"q","include":{"entities":null}}`), &nulled); err != nil {
		t.Fatal(err)
	}
	query2, err := (&Handlers{}).buildRecallQuery("b1", nulled)
	if err != nil {
		t.Fatal(err)
	}
	if query2.IncludeEntities {
		t.Fatal("an explicit null entities must exclude entity states")
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

// prefer_observations is nullable in v1.1.0: an omitted (or null) member defers
// to the caller's fallback, while an explicit false is a decision of its own and
// must not be replaced by that fallback.
func TestRecallPreferObservationsResolution(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fallback bool
		want     bool
	}{
		{"omitted defers to the fallback", `{"query":"q"}`, true, true},
		{"omitted with a false fallback", `{"query":"q"}`, false, false},
		{"explicit true wins", `{"query":"q","prefer_observations":true}`, false, true},
		{"explicit false wins over a true fallback", `{"query":"q","prefer_observations":false}`, true, false},
		{"null defers to the fallback", `{"query":"q","prefer_observations":null}`, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req RecallRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			if got := req.PreferObservationsValue(tc.fallback); got != tc.want {
				t.Fatalf("PreferObservationsValue(%v) = %v, want %v", tc.fallback, got, tc.want)
			}
		})
	}
}

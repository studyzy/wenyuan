package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// recallQuerySpy answers recall with the memory store's own result while
// recording the query the tool built, so a test can assert how the tool
// resolved its arguments without going through retrieval behaviour.
type recallQuerySpy struct {
	storage.Store
	got storage.RecallQuery
}

func (s *recallQuerySpy) Recall(ctx context.Context, query storage.RecallQuery) (storage.RecallResult, error) {
	s.got = query
	return s.Store.Recall(ctx, query)
}

// The six include members the recall schema declares reach both the storage
// query and the response envelope's include object, which is where the channel
// budgets are applied.
func TestRecallIncludeArgumentsReachTheQueryAndEnvelope(t *testing.T) {
	request, err := recallRequest(map[string]any{
		"query":                   "x",
		"include_chunks":          true,
		"max_chunk_tokens":        512.0,
		"include_entities":        true,
		"max_entity_tokens":       200.0,
		"include_source_facts":    true,
		"max_source_facts_tokens": 100.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := buildRecallQuery("b1", request)
	if err != nil {
		t.Fatal(err)
	}
	if !query.IncludeChunks || query.MaxChunkTokens != 512 {
		t.Fatalf("chunks = %v/%d, want on/512", query.IncludeChunks, query.MaxChunkTokens)
	}
	if !query.IncludeEntities || !query.IncludeSourceFacts {
		t.Fatalf("entities = %v source_facts = %v, want both on", query.IncludeEntities, query.IncludeSourceFacts)
	}
	if request.Include == nil || request.Include.Chunks == nil || request.Include.Entities == nil || request.Include.SourceFacts == nil {
		t.Fatalf("envelope include = %+v, want every channel present", request.Include)
	}
	if *request.Include.Chunks.MaxTokens != 512 || *request.Include.Entities.MaxTokens != 200 ||
		*request.Include.SourceFacts.MaxTokens != 100 {
		t.Fatalf("envelope budgets = %d/%d/%d, want 512/200/100",
			*request.Include.Chunks.MaxTokens, *request.Include.Entities.MaxTokens, *request.Include.SourceFacts.MaxTokens)
	}

	// An omitted member keeps its channel off, and a channel named without its
	// budget takes the figure the schema states.
	request, err = recallRequest(map[string]any{"query": "x", "include_chunks": true})
	if err != nil {
		t.Fatal(err)
	}
	query, err = buildRecallQuery("b1", request)
	if err != nil {
		t.Fatal(err)
	}
	if !query.IncludeChunks || query.IncludeEntities || query.IncludeSourceFacts {
		t.Fatalf("include flags = %v/%v/%v, want only chunks on",
			query.IncludeChunks, query.IncludeEntities, query.IncludeSourceFacts)
	}
	if query.MaxChunkTokens != defaultRecallChunkTokens {
		t.Fatalf("chunk budget = %d, want the schema default %d", query.MaxChunkTokens, defaultRecallChunkTokens)
	}
	if request.Include == nil || request.Include.Chunks == nil || request.Include.Entities != nil {
		t.Fatalf("envelope include = %+v, want chunks only", request.Include)
	}

	// With no include member at all every channel stays off, as the schema's
	// own defaults state.
	request, err = recallRequest(map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	query, err = buildRecallQuery("b1", request)
	if err != nil {
		t.Fatal(err)
	}
	if query.IncludeChunks || query.IncludeEntities || query.IncludeSourceFacts || query.MaxChunkTokens != 0 {
		t.Fatalf("include flags = %+v, want every channel off", query)
	}
}

// A recall that asks for chunks receives them: the retained text's chunks flow
// through the tool the way the HTTP route serves them, and stay null when the
// caller did not ask.
func TestRecallIncludeChunksReachTheResponse(t *testing.T) {
	server := newServer(t)
	content := strings.Repeat("alpha ", 600)
	if result := callTool(t, server, "1", "sync_retain",
		`{"bank_id":"b1","content":"`+content+`"}`); result["isError"] == true {
		t.Fatalf("sync_retain failed: %v", result)
	}
	asked := decodeTextPayload(t, callTool(t, server, "2", "recall",
		`{"bank_id":"b1","query":"alpha","include_chunks":true}`))
	chunks, ok := asked["chunks"].(map[string]any)
	if !ok || len(chunks) == 0 {
		t.Fatalf("recall with include_chunks = %v, want chunk entries", asked["chunks"])
	}
	for _, entry := range chunks {
		text, _ := entry.(map[string]any)["text"].(string)
		if !strings.Contains(text, "alpha") {
			t.Fatalf("chunk entry = %v, want the retained text", entry)
		}
	}
	unasked := decodeTextPayload(t, callTool(t, server, "3", "recall",
		`{"bank_id":"b1","query":"alpha"}`))
	if unasked["chunks"] != nil || unasked["entities"] != nil || unasked["source_facts"] != nil {
		t.Fatalf("recall without include members = %v, want every channel null", unasked)
	}
}

// A recall that omits prefer_observations falls back to the bank's
// recall_prefer_observations configuration, and an explicit value still wins —
// the resolution the HTTP route applies to the same member.
func TestRecallPreferObservationsResolvesTheBankConfig(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{
		BankID: "b1",
		Config: map[string]any{"recall_prefer_observations": true},
	}); err != nil {
		t.Fatal(err)
	}
	spy := &recallQuerySpy{Store: store}
	server := New(spy, nil)

	if result := callTool(t, server, "1", "recall", `{"bank_id":"b1","query":"x"}`); result["isError"] == true {
		t.Fatalf("recall failed: %v", result)
	}
	if !spy.got.PreferObservations {
		t.Fatal("an omitted prefer_observations must take the bank's configured true")
	}
	if result := callTool(t, server, "2", "recall",
		`{"bank_id":"b1","query":"x","prefer_observations":false}`); result["isError"] == true {
		t.Fatalf("recall failed: %v", result)
	}
	if spy.got.PreferObservations {
		t.Fatal("an explicit prefer_observations=false must win over the bank's true")
	}

	// A bank that configures nothing leaves the tool's own false default.
	plain := &recallQuerySpy{Store: storage.NewMemoryStore("public")}
	plainServer := New(plain, nil)
	if result := callTool(t, plainServer, "1", "recall", `{"bank_id":"b1","query":"x"}`); result["isError"] == true {
		t.Fatalf("recall failed: %v", result)
	}
	if plain.got.PreferObservations {
		t.Fatal("a bank without the configuration must leave the default false")
	}
}

// A retain timestamp the reference's parse_timestamp refuses is reported, not
// silently dropped: an unparseable value names itself, and a placeholder year —
// an open bound, not a date — is refused with the reference's sentence.
func TestRetainRefusesBrokenTimestamps(t *testing.T) {
	cases := []struct {
		timestamp string
		want      string
	}{
		{"not-a-date", "Invalid timestamp format 'not-a-date'. " +
			"Expected ISO format like '2024-01-15T10:30:00' or '2024-01-15T10:30:00Z'"},
		{"0001-01-01T00:00:00Z", "Timestamp year 1 is a placeholder, not a date."},
		{"9999-12-31T23:59:59Z", "Timestamp year 9999 is a placeholder, not a date."},
	}
	for _, tc := range cases {
		for _, tool := range []string{"retain", "sync_retain"} {
			result := callTool(t, newServer(t), "1", tool,
				`{"bank_id":"b1","content":"Alice works at Google.","timestamp":"`+tc.timestamp+`"}`)
			if result["isError"] == true {
				t.Fatalf("%s with %q reported an error result: %v", tool, tc.timestamp, result)
			}
			var payload struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(rawText(t, result)), &payload); err != nil {
				t.Fatalf("%s payload is not JSON: %s", tool, rawText(t, result))
			}
			if payload.Status != "error" || payload.Message != tc.want {
				t.Fatalf("%s with %q = %q, want status error message %q",
					tool, tc.timestamp, rawText(t, result), tc.want)
			}
		}
	}
}

// A timestamp the reference accepts is still stored, including the date-only
// form its parser takes — the refusal must not widen into a rejection of valid
// input.
func TestRetainStillStoresAcceptedTimestamps(t *testing.T) {
	server := newServer(t)
	if result := callTool(t, server, "1", "sync_retain",
		`{"bank_id":"b1","content":"Alice works at Google.","timestamp":"2024-01-15T10:30:00Z"}`); result["isError"] == true {
		t.Fatalf("sync_retain failed: %v", result)
	}
	if got := memoryMentionedAt(t, server); got != "2024-01-15T10:30:00+00:00" {
		t.Fatalf("mentioned_at = %q, want the stored timestamp", got)
	}
	server = newServer(t)
	if result := callTool(t, server, "1", "sync_retain",
		`{"bank_id":"b1","content":"Alice works at Google.","timestamp":"2024-01-15"}`); result["isError"] == true {
		t.Fatalf("sync_retain failed: %v", result)
	}
	if got := memoryMentionedAt(t, server); got != "2024-01-15T00:00:00+00:00" {
		t.Fatalf("date-only mentioned_at = %q, want the UTC midnight reading", got)
	}
}

// memoryMentionedAt retains nothing itself; it reads the bank's first memory's
// mentioned_at from the detail the get_memory tool serves.
func memoryMentionedAt(t *testing.T, server *Server) string {
	t.Helper()
	listed := callTool(t, server, "9", "list_memories", `{"bank_id":"b1"}`)
	content, _ := listed["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Items) == 0 {
		t.Fatalf("list_memories payload = %s (err %v)", text, err)
	}
	detail := decodeTextPayload(t, callTool(t, server, "10", "get_memory",
		`{"bank_id":"b1","memory_id":"`+page.Items[0].ID+`"}`))
	mentioned, _ := detail["mentioned_at"].(string)
	return mentioned
}

// memoryFixtureStore answers memory reads with one mutated unit, so a test can
// drive the editability refusals the update tool checks before it patches.
type memoryFixtureStore struct {
	storage.Store
	mutate func(*storage.MemoryUnit)
}

func (s memoryFixtureStore) GetMemory(ctx context.Context, bankID, memoryID string) (storage.MemoryUnit, error) {
	unit, err := s.Store.GetMemory(ctx, bankID, memoryID)
	if err != nil {
		return storage.MemoryUnit{}, err
	}
	s.mutate(&unit)
	return unit, nil
}

// The update tool applies the whole patch set it advertises: text, context,
// the three dates, the fact type and the entity set, not only the text and
// tags the dispatch used to carry.
func TestUpdateMemoryAppliesTheAdvertisedPatch(t *testing.T) {
	server := newServer(t)
	memoryID := seededMemoryID(t, server)

	result := callTool(t, server, "2", "update_memory", `{"bank_id":"b1","memory_id":"`+memoryID+`",`+
		`"text":"Alice works at Meta.","context":"","occurred_start":"2024-01-01T08:00:00Z",`+
		`"mentioned_at":"2024-01-02T09:00:00Z","fact_type":"experience","entities":[],"resolve_entities":false}`)
	if result["isError"] == true {
		t.Fatalf("update_memory failed: %v", result)
	}
	detail := decodeTextPayload(t, result)
	if detail["text"] != "Alice works at Meta." {
		t.Fatalf("text = %v, want the edited text", detail["text"])
	}
	if detail["context"] != "" {
		t.Fatalf("context = %v, want the empty string cleared", detail["context"])
	}
	if detail["type"] != "experience" {
		t.Fatalf("type = %v, want experience", detail["type"])
	}
	for member, want := range map[string]string{
		"occurred_start": "2024-01-01T08:00:00+00:00",
		"mentioned_at":   "2024-01-02T09:00:00+00:00",
	} {
		if detail[member] != want {
			t.Fatalf("%s = %v, want %q", member, detail[member], want)
		}
	}

	// A member the request omits stays as it stands.
	result = callTool(t, server, "3", "update_memory", `{"bank_id":"b1","memory_id":"`+memoryID+`"}`)
	if result["isError"] == true {
		t.Fatalf("update_memory failed: %v", result)
	}
	if detail := decodeTextPayload(t, result); detail["text"] != "Alice works at Meta." {
		t.Fatalf("text after a no-op = %v, want it unchanged", detail["text"])
	}
}

// The update tool refuses what the HTTP PATCH refuses, with the HTTP PATCH's
// sentences, instead of silently ignoring the member.
func TestUpdateMemoryRefusals(t *testing.T) {
	server := newServer(t)
	memoryID := seededMemoryID(t, server)
	cases := []struct {
		args string
		want string
	}{
		{`{"bank_id":"b1","memory_id":"` + memoryID + `","text":"   "}`, "text must not be empty."},
		{`{"bank_id":"b1","memory_id":"` + memoryID + `","fact_type":"observation"}`,
			"fact_type must be 'world' or 'experience'."},
		{`{"bank_id":"b1","memory_id":"` + memoryID + `","occurred_start":"not-a-date"}`,
			"Invalid isoformat string: 'not-a-date'"},
	}
	for _, tc := range cases {
		result := callTool(t, server, "1", "update_memory", tc.args)
		if result["isError"] == true {
			t.Fatalf("update_memory %s reported an error result: %v", tc.args, result)
		}
		if got := rawText(t, result); got != `{"error": "`+tc.want+`"}` {
			t.Fatalf("update_memory refusal = %s, want %q", got, tc.want)
		}
	}

	// A derived unit cannot be curated, and an invalidated one cannot be edited.
	observation := memoryFixtureStore{Store: server.store, mutate: func(unit *storage.MemoryUnit) {
		unit.FactType = storage.TypeObservation
	}}
	result := callTool(t, New(observation, nil), "1", "update_memory",
		`{"bank_id":"b1","memory_id":"`+memoryID+`","text":"x"}`)
	if got := rawText(t, result); !strings.Contains(got,
		"only world/experience facts can be curated") {
		t.Fatalf("update_memory on an observation = %s, want the curation refusal", got)
	}
	invalidated := memoryFixtureStore{Store: server.store, mutate: func(unit *storage.MemoryUnit) {
		unit.InvalidatedAt = &time.Time{}
	}}
	result = callTool(t, New(invalidated, nil), "1", "update_memory",
		`{"bank_id":"b1","memory_id":"`+memoryID+`","text":"x"}`)
	if got := rawText(t, result); got != `{"error": "Cannot edit an invalidated memory; revert it to 'valid' first."}` {
		t.Fatalf("update_memory on an invalidated memory = %s, want the edit refusal", got)
	}
}

// seededMemoryID retains one memory and returns its identifier.
func seededMemoryID(t *testing.T, server *Server) string {
	t.Helper()
	if result := callTool(t, server, "1", "sync_retain",
		`{"bank_id":"b1","content":"Alice works at Google."}`); result["isError"] == true {
		t.Fatalf("sync_retain failed: %v", result)
	}
	listed := callTool(t, server, "2", "list_memories", `{"bank_id":"b1"}`)
	content, _ := listed["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Items) == 0 {
		t.Fatalf("list_memories payload = %s (err %v)", text, err)
	}
	return page.Items[0].ID
}

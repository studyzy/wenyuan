package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// memberOrder returns the source-level member names of a JSON object literal, in
// document order. The tools/list payload is compared as bytes, so reading the
// order back out of the serialized form is what proves the encoder did not
// reorder anything.
func memberOrder(t *testing.T, body string) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(body))
	if _, err := decoder.Token(); err != nil {
		t.Fatalf("decode object: %v", err)
	}
	var names []string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode key: %v", err)
		}
		names = append(names, key.(string))
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			t.Fatalf("decode value: %v", err)
		}
	}
	return names
}

// equalOrder reports whether two member lists match.
func equalOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// A tools/list entry carries the reference's members in the reference's order:
// the SDK's Tool model declares name, title, description, inputSchema,
// outputSchema, annotations, _meta, and pydantic serializes a model in
// declaration order. A Go map would have sent them alphabetically.
func TestToolEntriesCarryTheSDKMemberOrder(t *testing.T) {
	entries := fastmcpToolEntries(toolDefinitions(true), true)
	if len(entries) != 39 {
		t.Fatalf("entries = %d, want the full roster", len(entries))
	}
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations", "_meta"}
		if got := memberOrder(t, string(encoded)); !equalOrder(got, want) {
			t.Fatalf("entry members = %v, want %v", got, want)
		}
	}
}

// Inside an entry, every JSON-schema node is ordered the way pydantic emits it:
// an object node lists additionalProperties, properties, required, type, and a
// union property lists anyOf, default, description, title.
func TestToolSchemaNodesCarryPydanticOrder(t *testing.T) {
	var retain orderedObject
	for _, entry := range fastmcpToolEntries(toolDefinitions(true), true) {
		if entry[0].value == "retain" {
			retain = entry
		}
	}
	encoded, err := json.Marshal(retain)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, want := memberOrder(t, string(decoded["inputSchema"])),
		[]string{"additionalProperties", "properties", "required", "type"}; !equalOrder(got, want) {
		t.Fatalf("inputSchema members = %v, want %v", got, want)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(decoded["outputSchema"], &output); err != nil {
		t.Fatal(err)
	}
	if got, want := memberOrder(t, string(decoded["outputSchema"])),
		[]string{"additionalProperties", "type"}; !equalOrder(got, want) {
		t.Fatalf("outputSchema members = %v, want %v", got, want)
	}
}

// The title is the one the SDK derives from the name: underscores become spaces
// and every word is title-cased, emitted because a client that has no fallback
// would otherwise drop the tool.
func TestToolTitlesAreTheSDKsDerivedTitles(t *testing.T) {
	cases := map[string]string{
		"retain":                  "Retain",
		"sync_retain":             "Sync Retain",
		"get_knowledge_base_tree": "Get Knowledge Base Tree",
		"list_banks":              "List Banks",
	}
	for name, want := range cases {
		if got := toolTitle(name); got != want {
			t.Errorf("toolTitle(%q) = %q, want %q", name, got, want)
		}
	}
}

// A tool's annotation object follows ToolAnnotations' declaration order, not the
// schema order the surrounding entry uses.
func TestToolAnnotationsCarryTheirOwnOrder(t *testing.T) {
	annotations := toolAnnotations("delete_bank")
	encoded, err := json.Marshal(orderedAnnotations(annotations))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"readOnlyHint", "destructiveHint", "openWorldHint"}
	if got := memberOrder(t, string(encoded)); !equalOrder(got, want) {
		t.Fatalf("annotation members = %v, want %v", got, want)
	}
}

// A tool that does not declare an explicit null schema keyword must not carry
// one, and the one bare-regexp risk in the payload — the description prose — is
// emitted untouched, so a client reading the catalogue sees the reference's
// bytes.
func TestToolEntriesRoundTripWithoutExtraMembers(t *testing.T) {
	entry := fastmcpToolEntries(toolDefinitions(true), false)
	if len(entry) == 0 {
		t.Fatal("no entries")
	}
	encoded, err := json.Marshal(entry[0])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"name", "title", "description", "inputSchema", "outputSchema", "_meta"}
	if got := memberOrder(t, string(encoded)); !equalOrder(got, want) {
		t.Fatalf("members without annotations = %v, want %v", got, want)
	}
}

// The payload the dispatch answers has no Go-map ordering left in it: a map
// would have sorted `_meta` ahead of `description`, which is the marker. The
// entries are read back from the wire bytes, not from a decoded value, because a
// decode into a map would lose the order under test.
func TestToolsListDispatchCarriesOrderedEntries(t *testing.T) {
	server := newServer(t)
	path := "/mcp"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpSessionHeader, handshakeSession(t, server, path))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	wire := sseData(t, rec)
	var envelope struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(wire), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Result.Tools) != 39 {
		t.Fatalf("tools = %d, want the full roster", len(envelope.Result.Tools))
	}
	for _, entry := range envelope.Result.Tools {
		want := []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations", "_meta"}
		if got := memberOrder(t, string(entry)); !equalOrder(got, want) {
			t.Fatalf("served entry members = %v, want %v", got, want)
		}
	}
}

// A trigger's `tag_groups` is advertised as an array of plain objects, never as
// a `$ref` into a recursive definition: TagGroup nests itself, and some LLM
// providers reject a recursive tool schema and fail the whole request, which is
// why the reference declares `list[dict[str, Any]]` here
// (mcp_tools.py::MentalModelTriggerInput.tag_groups, #5013).
func TestTriggerTagGroupsIsAPlainObjectArray(t *testing.T) {
	for _, name := range []string{"create_mental_model", "update_mental_model", "create_knowledge_page", "update_knowledge_node"} {
		for _, def := range toolDefinitions(true) {
			if def.Name != name {
				continue
			}
			properties, _ := def.InputSchema["properties"].(map[string]any)
			trigger, _ := properties["trigger"].(map[string]any)
			branches, _ := trigger["anyOf"].([]any)
			if len(branches) == 0 {
				t.Fatalf("%s: trigger has no union", name)
			}
			node, _ := branches[0].(map[string]any)
			triggerProperties, _ := node["properties"].(map[string]any)
			tagGroups, _ := triggerProperties["tag_groups"].(map[string]any)
			encoded, err := json.Marshal(tagGroups)
			if err != nil {
				t.Fatal(err)
			}
			body := string(encoded)
			if strings.Contains(body, "$ref") || strings.Contains(body, "$defs") {
				t.Fatalf("%s: tag_groups is recursive: %s", name, body)
			}
			if !strings.Contains(body, `"items":{"additionalProperties":true,"type":"object"}`) {
				t.Fatalf("%s: tag_groups items are not a plain object: %s", name, body)
			}
		}
	}
}

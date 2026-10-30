package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A trigger the reference's model refuses is refused on the MCP path too: the
// tool argument model validates before the tool runs, so nothing is written.
// An unvalidated trigger used to be stored as sent and only fail on the next
// refresh.
func TestMCPTriggerIsValidatedBeforeItIsStored(t *testing.T) {
	server := newServer(t)
	store := server.store
	cases := []struct {
		tool string
		args string
		want string
	}{
		{"create_mental_model",
			`{"bank_id":"b1","name":"n","source_query":"q","trigger":{"refresh_cron":"not a cron"}}`,
			"refresh_cron is not a valid cron expression: 'not a cron'"},
		{"create_mental_model",
			`{"bank_id":"b1","name":"n","source_query":"q","trigger":{"full_refresh_cron":"61 * * * *"}}`,
			"full_refresh_cron is not a valid cron expression: '61 * * * *'"},
		{"create_mental_model",
			`{"bank_id":"b1","name":"n","source_query":"q","trigger":{"bogus":1}}`,
			"trigger.bogus: Extra inputs are not permitted"},
		{"create_mental_model",
			`{"bank_id":"b1","name":"n","source_query":"q","trigger":{"mode":"sideways"}}`,
			"Input should be 'full' or 'delta'"},
	}
	for _, tc := range cases {
		result := callTool(t, server, "1", tc.tool, tc.args)
		if got := rawText(t, result); !strings.Contains(got, tc.want) {
			t.Errorf("%s %s: text = %q, want it to carry %q", tc.tool, tc.args, got, tc.want)
		}
	}
	models, err := store.ListMentalModels(t.Context(), "b1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %d, want none: a refused trigger must not be stored", len(models))
	}
}

// Supplying one of the two schedule members drops the stored other, as the
// reference's merge does, and a patch that states BOTH is refused by the
// supplied-trigger validator — the only cross-member rule the reference has.
func TestMCPUUpdateTriggerMergesLikeTheReference(t *testing.T) {
	server := newServer(t)
	created := callTool(t, server, "1", "create_mental_model",
		`{"bank_id":"b1","name":"n","source_query":"q","tags_match":"any","trigger":{"refresh_after_consolidation":true}}`)
	if created["isError"] == true {
		t.Fatalf("create failed: %v", created)
	}
	models, err := server.store.ListMentalModels(t.Context(), "b1", "")
	if err != nil || len(models) != 1 {
		t.Fatalf("models = %v, %v", models, err)
	}
	// Stating a cron replaces the stored consolidation trigger rather than
	// being refused: a merged result is not a conflict.
	replaced := callTool(t, server, "1", "update_mental_model",
		`{"bank_id":"b1","mental_model_id":"`+models[0].ID+`","trigger":{"refresh_cron":"0 3 * * *"}}`)
	if replaced["isError"] == true {
		t.Fatalf("a cron patch was refused: %v", replaced)
	}
	stored, err := server.store.GetMentalModel(t.Context(), "b1", models[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Trigger["refresh_cron"] != "0 3 * * *" {
		t.Fatalf("stored trigger = %v, want the cron", stored.Trigger)
	}
	if _, present := stored.Trigger["refresh_after_consolidation"]; present {
		t.Fatalf("stored trigger = %v, want the superseded consolidation flag dropped", stored.Trigger)
	}
	// A patch that states both in one object is contradictory and is refused
	// before anything is written.
	refused := callTool(t, server, "1", "update_mental_model",
		`{"bank_id":"b1","mental_model_id":"`+models[0].ID+
			`","trigger":{"refresh_cron":"0 3 * * *","refresh_after_consolidation":true}}`)
	if !strings.Contains(rawText(t, refused), "mutually exclusive") {
		t.Fatalf("text = %q, want the mutual-exclusion refusal", rawText(t, refused))
	}
}

// The legacy flat tags_match shorthand// The legacy flat tags_match shorthand the mental-model tools still accept is
// checked against the same five modes the reference's enum declares, and the
// sentence names the rejected value.
func TestMCPTagsMatchShorthandIsValidated(t *testing.T) {
	server := newServer(t)
	result := callTool(t, server, "1", "create_mental_model",
		`{"bank_id":"b1","name":"n","source_query":"q","tags_match":"any-strict"}`)
	want := "tags_match must be one of any, all, any_strict, all_strict, exact, got 'any-strict'"
	if got := rawText(t, result); !strings.Contains(got, want) {
		t.Fatalf("text = %q, want %q", got, want)
	}
	accepted := callTool(t, server, "1", "create_mental_model",
		`{"bank_id":"b1","name":"n","source_query":"q","tags_match":"all_strict"}`)
	if accepted["isError"] == true {
		t.Fatalf("a valid shorthand was refused: %v", accepted)
	}
}

// The transport strips an argument the tool does not declare, so the extra
// fields an LLM adds to a call do not turn it into a failure.
func TestMCPCallStripsUndeclaredArguments(t *testing.T) {
	server := newServer(t)
	result := callTool(t, server, "1", "list_mental_models",
		`{"bank_id":"b1","explanation":"because","limit":5}`)
	if result["isError"] == true {
		t.Fatalf("a declared call with an extra field failed: %v", result)
	}
	if got := rawText(t, result); !strings.Contains(got, `"total": 0`) {
		t.Fatalf("text = %q, want an empty page", got)
	}
}

// A list or object argument that arrived as a JSON string is parsed back to the
// kind its schema declares, which is how an LLM bridge serializes arguments.
func TestMCPCallCoercesJSONStringArguments(t *testing.T) {
	server := newServer(t)
	if _, err := server.store.PutDirective(t.Context(), storage.Directive{
		ID: "00000000-0000-4000-8000-000000000001", BankID: "b1",
		Name: "n", Content: "c", Priority: 1, IsActive: true, Tags: []string{"alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	filtered := callTool(t, server, "1", "list_directives", `{"bank_id":"b1","tags":"[\"alpha\"]"}`)
	if got := rawText(t, filtered); !strings.Contains(got, `"total": 1`) {
		t.Fatalf("string-encoded tags: text = %q, want the tagged directive", got)
	}
	unmatched := callTool(t, server, "1", "list_directives", `{"bank_id":"b1","tags":"[\"beta\"]"}`)
	if got := rawText(t, unmatched); !strings.Contains(got, `"total": 0`) {
		t.Fatalf("string-encoded tags: text = %q, want no directive", got)
	}
}

// A string that is not JSON of the declared kind is left exactly as sent, so the
// tool's own validation still refuses it instead of the transport quietly
// dropping the argument.
func TestMCPCallLeavesUncoercibleStringsAlone(t *testing.T) {
	if got := normalizeToolArguments(
		map[string]any{"properties": map[string]any{"tags": map[string]any{"type": "array"}}},
		map[string]any{"tags": "not-json"},
	); got["tags"] != "not-json" {
		t.Fatalf("tags = %v, want the original string", got["tags"])
	}
	if got := normalizeToolArguments(
		map[string]any{"properties": map[string]any{"tags": map[string]any{"type": "array"}}},
		map[string]any{"tags": `{"a":1}`},
	); got["tags"] != `{"a":1}` {
		t.Fatalf("tags = %v, want the original string", got["tags"])
	}
	native := []any{"a"}
	if got := normalizeToolArguments(
		map[string]any{"properties": map[string]any{"tags": map[string]any{"type": "array"}}},
		map[string]any{"tags": native},
	); got["tags"] == nil {
		t.Fatalf("a native list was dropped: %v", got["tags"])
	}
}

// An unknown directive names the id in the reference's sentence, as the tool's
// own `_ToolError` does, rather than forwarding the store's wording.
func TestMCPDeleteDirectiveNamesTheMissingID(t *testing.T) {
	result := callTool(t, newServer(t), "1", "delete_directive",
		`{"bank_id":"b1","directive_id":"00000000-0000-4000-8000-000000000002"}`)
	if result["isError"] == true {
		t.Fatalf("a missing directive is a normal negative answer: %v", result)
	}
	want := `{"error": "Directive '00000000-0000-4000-8000-000000000002' not found"}`
	if got := rawText(t, result); got != want {
		t.Fatalf("text = %s, want %s", got, want)
	}
}

// A mental-model tool reached through an unknown bank answers the model's own
// not-found, because the reference looks the model up inside the bank and cannot
// tell the two apart.
func TestMCPMentalModelToolsInAnUnknownBank(t *testing.T) {
	server := newServer(t)
	got := callTool(t, server, "1", "get_mental_model", `{"bank_id":"nobank","mental_model_id":"mm-x"}`)
	if want := `{"error": "Mental model 'mm-x' not found in bank 'nobank'"}`; rawText(t, got) != want {
		t.Fatalf("get_mental_model text = %s, want %s", rawText(t, got), want)
	}
	got = callTool(t, server, "1", "refresh_mental_model", `{"bank_id":"nobank","mental_model_id":"mm-x"}`)
	if want := `{"error": "Mental model mm-x not found in bank nobank"}`; rawText(t, got) != want {
		t.Fatalf("refresh_mental_model text = %s, want %s", rawText(t, got), want)
	}
}

// refresh_mental_model records the model's name on the operation, which is what
// the operation-status read reports back.
func TestMCPRefreshRecordsTheModelName(t *testing.T) {
	server := newServer(t)
	created := callTool(t, server, "1", "create_mental_model", `{"bank_id":"b1","name":"Prefs","source_query":"q"}`)
	var ack struct {
		MentalModelID string `json:"mental_model_id"`
	}
	if err := json.Unmarshal([]byte(rawText(t, created)), &ack); err != nil {
		t.Fatal(err)
	}
	result := callTool(t, server, "1", "refresh_mental_model",
		`{"bank_id":"b1","mental_model_id":"`+ack.MentalModelID+`"}`)
	var refreshed struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(rawText(t, result)), &refreshed); err != nil {
		t.Fatalf("refresh answer = %s", rawText(t, result))
	}
	stored, err := server.store.GetOperation(t.Context(), "b1", refreshed.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResultMetadata["name"] != "Prefs" {
		t.Fatalf("operation name = %v, want the model's name", stored.ResultMetadata["name"])
	}
}

// list_banks hands the engine's rows through: the HTTP route's projection is not
// the tool's contract.
func TestMCPListBanksReturnsEngineRows(t *testing.T) {
	result := callTool(t, newServer(t), "1", "list_banks", `{}`)
	var page struct {
		Banks []map[string]any `json:"banks"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal([]byte(rawText(t, result)), &page); err != nil {
		t.Fatalf("list_banks answer = %s", rawText(t, result))
	}
	if page.Total != 1 || len(page.Banks) != 1 {
		t.Fatalf("page = %+v, want the one bank", page)
	}
	row := page.Banks[0]
	for _, member := range []string{
		"bank_id", "name", "disposition", "mission", "created_at", "updated_at",
		"fact_count", "last_document_at", "last_write_at", "matched_aliases", "display_alias",
	} {
		if _, present := row[member]; !present {
			t.Errorf("row is missing %q: %v", member, row)
		}
	}
}

// create_bank's `bank_id` is its own required argument, so the single-bank mount
// keeps it instead of stripping it as the mount's target.
func TestMCPCreateBankKeepsItsRequiredBankIDOnEveryMount(t *testing.T) {
	for _, multiBank := range []bool{true, false} {
		for _, def := range toolDefinitions(multiBank) {
			if def.Name != "create_bank" {
				continue
			}
			properties, _ := def.InputSchema["properties"].(map[string]any)
			if _, present := properties["bank_id"]; !present {
				t.Fatalf("multiBank=%v: create_bank lost its bank_id argument", multiBank)
			}
		}
	}
}

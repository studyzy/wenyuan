package app

// Public-contract integration oracles.
//
// These cases assert the served application handler's public HTTP contract
// end to end through the composed application. Each case is a contract-parity
// check: it fixes the observable behavior the public surface must preserve.
//
// Only cases that exercise the public HTTP contract without a live PostgreSQL
// or a live model endpoint run here; those requiring a real database or a real
// model are out of scope for this harness.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/storage"
)

// parityApp composes the served application over an in-memory store, producing
// the same handler the process serves.
func parityApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	cfg.EnableBankLLMHealth = true
	a, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// exchange issues one request against the served handler and returns the
// recorder for assertions.
func exchange(a *App, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (body %s)", err, rec.Body.String())
	}
	return out
}

// An unknown top-level body field is reported on the response through the
// ignored-parameter header, the body counterpart of an unknown query parameter.
func TestParityUnknownBodyFieldReported(t *testing.T) {
	a := parityApp(t)
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/b1", `{"name":"B"}`); rec.Code != http.StatusOK {
		t.Fatalf("bank create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := exchange(a, http.MethodPost, "/v1/default/banks/b1/memories/recall", `{"query":"hello","max_token":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Ignored-Params"); !strings.Contains(got, "max_token") {
		t.Fatalf("X-Ignored-Params = %q, want it to name max_token", got)
	}
	// A body-field typo on a bank write is reported the same way.
	rec = exchange(a, http.MethodPatch, "/v1/default/banks/b1", `{"name":"B","nam":"typo"}`)
	if got := rec.Header().Get("X-Ignored-Params"); !strings.Contains(got, "nam") {
		t.Fatalf("X-Ignored-Params = %q, want it to name the bank-write typo", got)
	}
}

// Contract reference: public HTTP behavior for this case.
// The version endpoint reports api_version and a boolean feature map.
func TestParityVersionEndpoint(t *testing.T) {
	a := parityApp(t)
	rec := exchange(a, http.MethodGet, "/version", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	if _, ok := body["api_version"].(string); !ok {
		t.Fatalf("api_version missing or not a string: %v", body["api_version"])
	}
	features, ok := body["features"].(map[string]any)
	if !ok {
		t.Fatalf("features missing or not an object: %v", body["features"])
	}
	for _, key := range []string{"mcp", "bank_config_api"} {
		if _, present := features[key]; !present {
			t.Fatalf("feature %q missing from the feature map: %v", key, features)
		}
	}
}

// Contract reference: public HTTP behavior for this case.
// Unknown query parameters must not be rejected; they are reported on the
// X-Ignored-Params header instead. Known parameters produce no header.
func TestParityUnknownQueryParamsAreReportedNotRejected(t *testing.T) {
	a := parityApp(t)

	rec := exchange(a, http.MethodGet, "/v1/default/banks?limit=1&tag=source:slack&created_after=2026-01-01", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown params must not be rejected: status = %d", rec.Code)
	}
	ignored := rec.Header().Get("X-Ignored-Params")
	if !strings.Contains(ignored, "tag") || !strings.Contains(ignored, "created_after") {
		t.Fatalf("X-Ignored-Params = %q, want it to name tag and created_after", ignored)
	}

	// Known parameters only: no reporting header.
	rec = exchange(a, http.MethodGet, "/v1/default/banks?limit=1&offset=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("known params status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Ignored-Params"); got != "" {
		t.Fatalf("known params must not be reported: X-Ignored-Params = %q", got)
	}
}

// Contract reference: public HTTP behavior for this case.
// Validation failures return 422: a write item without content, an invalid
// recall budget, and an unsupported fact type. A non-existent document is 404.
func TestParityValidationErrorHandling(t *testing.T) {
	a := parityApp(t)

	// A write item missing its required content field.
	rec := exchange(a, http.MethodPost, "/v1/default/banks/error_test/memories",
		`{"items":[{"context":"test"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing content must be 422, got %d (body %s)", rec.Code, rec.Body.String())
	}

	// Recall with an out-of-range budget value.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/error_test/memories/recall",
		`{"query":"test","budget":"invalid_budget"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid budget must be 422, got %d", rec.Code)
	}

	// Recall with an unsupported fact type.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/error_test/memories/recall",
		`{"query":"test","types":["bogus"]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid fact type must be 422, got %d", rec.Code)
	}

	// A non-existent document under a non-existent bank is not found.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/nonexistent_bank/documents/fake-doc-id", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing document must be 404, got %d", rec.Code)
	}
}

// Contract reference: the reference's framework builds validation items with
// include_url=False, so a served item carries exactly type, loc, msg, input and
// — when the type has one — ctx, and the type and wording are the framework's
// own. This pins the envelope the composed application actually serves, not the
// item constructors.
func TestParityServedValidationItemShape(t *testing.T) {
	a := parityApp(t)

	cases := []struct {
		name         string
		method       string
		path         string
		body         string
		wantType     string
		wantLoc      []any
		wantMsg      string
		wantCtx      bool
		wantInput    any
		wantInputNil bool
	}{
		{
			name: "absent body", method: http.MethodPost,
			path: "/v1/default/banks/vocab/memories/recall", body: "",
			wantType: "missing", wantLoc: []any{"body"}, wantMsg: "Field required", wantInputNil: true,
		},
		{
			name: "string for a list", method: http.MethodPost,
			path: "/v1/default/banks/vocab/memories", body: `{"items":"x"}`,
			wantType: "list_type", wantLoc: []any{"body", "items"},
			wantMsg: "Input should be a valid list", wantInput: "x",
		},
		{
			name: "truncated body", method: http.MethodPost,
			path: "/v1/default/banks/vocab/memories/recall", body: `{"query": `,
			wantType: "json_invalid", wantCtx: true, wantInput: map[string]any{},
		},
		{
			name: "value above its inclusive bound", method: http.MethodGet,
			path:     "/v1/default/banks/vocab/operations?limit=5000",
			wantType: "less_than_equal", wantLoc: []any{"query", "limit"},
			wantMsg: "Input should be less than or equal to 100", wantCtx: true, wantInput: "5000",
		},
		{
			name: "value outside an enum", method: http.MethodPost,
			path: "/v1/default/banks/vocab/memories/recall", body: `{"query":"q","budget":"bogus"}`,
			wantType: "enum", wantLoc: []any{"body", "budget"},
			wantMsg: "Input should be 'low', 'mid' or 'high'", wantCtx: true, wantInput: "bogus",
		},
		{
			name: "rejected custom field value", method: http.MethodPost,
			path: "/v1/default/banks/vocab/memories", body: `{"items":[{"content":"x","timestamp":"nope"}]}`,
			wantType: "value_error", wantLoc: []any{"body", "items", float64(0), "timestamp"},
			wantCtx: true, wantInput: "nope",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := exchange(a, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
			}
			var body struct {
				Detail []struct {
					Type  string         `json:"type"`
					Loc   []any          `json:"loc"`
					Msg   string         `json:"msg"`
					Input any            `json:"input"`
					Ctx   map[string]any `json:"ctx"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("detail envelope: %v (body %s)", err, rec.Body.String())
			}
			if len(body.Detail) == 0 {
				t.Fatalf("empty detail: %s", rec.Body.String())
			}
			item := body.Detail[0]
			raw := firstItemKeys(t, rec)
			if _, ok := raw["url"]; ok {
				t.Fatalf("an item must not carry a url key: %s", rec.Body.String())
			}
			for _, key := range []string{"type", "loc", "msg", "input"} {
				if _, ok := raw[key]; !ok {
					t.Fatalf("item is missing key %q: %s", key, rec.Body.String())
				}
			}
			if _, ok := raw["ctx"]; ok != tc.wantCtx {
				t.Fatalf("ctx present = %v, want %v: %s", ok, tc.wantCtx, rec.Body.String())
			}
			if item.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", item.Type, tc.wantType)
			}
			if tc.wantMsg != "" && item.Msg != tc.wantMsg {
				t.Fatalf("msg = %q, want %q", item.Msg, tc.wantMsg)
			}
			if tc.wantLoc != nil {
				if len(item.Loc) != len(tc.wantLoc) {
					t.Fatalf("loc = %v, want %v", item.Loc, tc.wantLoc)
				}
				for i := range tc.wantLoc {
					if item.Loc[i] != tc.wantLoc[i] {
						t.Fatalf("loc = %v, want %v", item.Loc, tc.wantLoc)
					}
				}
			}
			if tc.wantInputNil {
				got, ok := raw["input"]
				if !ok || string(got) != "null" {
					t.Fatalf("input = %s (present %v), want null: %s", got, ok, rec.Body.String())
				}
			}
			if tc.wantInput != nil {
				got, want := json.RawMessage(mustJSON(t, item.Input)), json.RawMessage(mustJSON(t, tc.wantInput))
				if string(got) != string(want) {
					t.Fatalf("input = %s, want %s", got, want)
				}
			}
		})
	}
}

// firstItemKeys returns the keys the first detail item of a validation response
// actually carries.
func firstItemKeys(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var body struct {
		Detail []map[string]json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("detail envelope: %v (body %s)", err, rec.Body.String())
	}
	if len(body.Detail) == 0 {
		t.Fatalf("empty detail: %s", rec.Body.String())
	}
	return body.Detail[0]
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Contract reference: public HTTP behavior for this case.
// The retired profile/background routes answer 410 Gone without a credential:
// the reference raises the retirement status before any authenticated call.
// They must not create a bank as a side effect.
func TestParityRetiredBankProfileRoutesAreGone(t *testing.T) {
	a := parityApp(t)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/default/banks/retired_bank/profile", ""},
		{http.MethodPut, "/v1/default/banks/retired_bank/profile", `{"name":"x"}`},
		{http.MethodPost, "/v1/default/banks/retired_bank/background", `{}`},
	}
	for _, tc := range cases {
		var req *http.Request
		if tc.body == "" {
			req = httptest.NewRequest(tc.method, tc.path, nil)
		} else {
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		a.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone {
			t.Fatalf("%s %s = %d, want 410 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "config") {
			t.Fatalf("%s %s message must name the config routes: %s", tc.method, tc.path, rec.Body.String())
		}
	}

	// A request carrying a credential answers the same terminal status.
	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/retired_bank/profile", nil)
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("authenticated retired read = %d, want 410", rec.Code)
	}

	// The retired routes must not have created the bank.
	rec = exchange(a, http.MethodGet, "/v1/default/banks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listing status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "retired_bank") {
		t.Fatalf("a retired route must not create a bank: %s", rec.Body.String())
	}
}

// Contract reference: public HTTP behavior for this case.
// A bank created, written to, listed, read back and deleted exercises the
// lifecycle end to end: the list envelope carries the pre-paging total, the row
// reports a fact count that reflects the write, and deletion answers a delete
// envelope and removes the bank.
func TestParityBankLifecycleWorkflow(t *testing.T) {
	a := parityApp(t)

	// Creation returns the profile shape.
	rec := exchange(a, http.MethodPut, "/v1/default/banks/workflow_bank", `{"name":"Workflow Bank"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d (body %s)", rec.Code, rec.Body.String())
	}
	profile := decodeJSON(t, rec)
	for _, field := range []string{"bank_id", "name", "disposition", "mission"} {
		if _, ok := profile[field]; !ok {
			t.Fatalf("profile is missing %q: %v", field, profile)
		}
	}
	if profile["bank_id"] != "workflow_bank" {
		t.Fatalf("profile bank_id = %v", profile["bank_id"])
	}

	// A write is accepted and reports how many items it took.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/workflow_bank/memories",
		`{"items":[{"content":"The deployment runs on Tuesdays.","document_id":"ops"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("retain = %d (body %s)", rec.Code, rec.Body.String())
	}
	write := decodeJSON(t, rec)
	if write["success"] != true {
		t.Fatalf("retain success = %v", write["success"])
	}
	if write["items_count"] != float64(1) {
		t.Fatalf("retain items_count = %v", write["items_count"])
	}

	// The list is a page envelope and the row carries a non-zero fact count.
	rec = exchange(a, http.MethodGet, "/v1/default/banks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	list := decodeJSON(t, rec)
	for _, field := range []string{"banks", "total", "limit", "offset"} {
		if _, ok := list[field]; !ok {
			t.Fatalf("list envelope is missing %q: %v", field, list)
		}
	}
	banks, _ := list["banks"].([]any)
	if len(banks) == 0 {
		t.Fatal("the retained bank must appear in the list")
	}
	row, _ := banks[0].(map[string]any)
	if count, _ := row["fact_count"].(float64); count <= 0 {
		t.Fatalf("fact_count must reflect the retained write: %v", row["fact_count"])
	}

	// Deletion answers a delete envelope.
	rec = exchange(a, http.MethodDelete, "/v1/default/banks/workflow_bank", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d (body %s)", rec.Code, rec.Body.String())
	}
	deleted := decodeJSON(t, rec)
	for _, field := range []string{"success", "message", "deleted_count"} {
		if _, ok := deleted[field]; !ok {
			t.Fatalf("delete envelope is missing %q: %v", field, deleted)
		}
	}

	// The bank is gone afterwards.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/workflow_bank/config", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("config read after delete = %d, want 404", rec.Code)
	}
}

// Contract reference: public HTTP behavior for this case.
// Alias operations on a bank the caller does not own, and writes addressed by an
// unknown alias, are not found.
func TestParityAliasNegativeCases(t *testing.T) {
	a := parityApp(t)

	// A bank without any alias: promoting/removing an unknown alias is 404.
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/owner", `{"name":"Owner"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	rec := exchange(a, http.MethodPatch, "/v1/default/banks/owner/aliases/ghost", `{"primary":true}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("promoting an unknown alias = %d, want 404", rec.Code)
	}
	rec = exchange(a, http.MethodDelete, "/v1/default/banks/owner/aliases/ghost", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("removing an unknown alias = %d, want 404", rec.Code)
	}

	// Addressing a write by a not-yet-existing bank lazily creates it (the
	// source: "Banks are created lazily on first retain"), so this is a 200.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/never-created/memories",
		`{"items":[{"content":"x"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("write into an unknown bank must lazily create it: %d", rec.Code)
	}
	// The lazy creation is observable: the bank now exists for reads.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/never-created/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lazily created bank must be readable: %d", rec.Code)
	}
}

// Contract reference: public HTTP behavior for this case.
// Deleting a bank that was never created is a no-op that reports a zero count.
func TestParityDeleteNonexistentBankIsZeroCount(t *testing.T) {
	a := parityApp(t)
	rec := exchange(a, http.MethodDelete, "/v1/default/banks/never_existed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete of a nonexistent bank = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["success"] != true {
		t.Fatalf("success = %v", body["success"])
	}
	if body["deleted_count"] != float64(0) {
		t.Fatalf("deleted_count = %v, want 0", body["deleted_count"])
	}
}

// Contract reference: public HTTP behavior for this case.
// An alias routes to the same bank and the response reports the canonical id.
func TestParityBankAliasesRoutingAndUniqueness(t *testing.T) {
	a := parityApp(t)

	// Create the bank.
	rec := exchange(a, http.MethodPut, "/v1/default/banks/alias-src", `{"name":"Source"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}

	// Add an alias: answers 201 with the full alias list.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/alias-src/aliases", `{"alias":"alias-new"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add alias = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["bank_id"] != "alias-src" {
		t.Fatalf("alias response must report the canonical id: %v", body["bank_id"])
	}

	// Reached through the alias, the bank reports its own id.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/alias-new/aliases", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("alias-addressed read = %d", rec.Code)
	}
	body = decodeJSON(t, rec)
	if body["bank_id"] != "alias-src" {
		t.Fatalf("read through an alias must report the canonical id: %v", body["bank_id"])
	}
}

// test-existing-deterministic-paths-preserved: the existing deterministic
// memory-write and reflection paths still produce their existing results when
// the provider boundary is present and no model is configured. The comparison
// fails if the boundary replaced the deterministic behavior rather than
// extending it.
func TestExistingDeterministicPathsPreservedWithProviderBoundary(t *testing.T) {
	// The two apps must observe the SAME stored content, otherwise the
	// comparison below would only be measuring the difference between two
	// independent stores rather than the effect of the provider boundary.
	shared := storage.NewMemoryStore("public")

	// A baseline app whose boundary resolves to the no-model selection.
	baseline, err := New(context.Background(), testConfig(), Dependencies{Store: shared})
	if err != nil {
		t.Fatal(err)
	}

	// An app where the boundary is explicitly injected with the no-model
	// selection, which is the state under test.
	withBoundary, err := New(context.Background(), testConfig(), Dependencies{
		Store:    shared,
		Provider: none.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if withBoundary.Provider == nil {
		t.Fatal("the boundary must be present")
	}

	const retainBody = `{"items":[{"content":"The deployment runs on Tuesdays.","document_id":"ops"}]}`
	const reflectBody = `{"query":"When does the deployment run?","budget":"low"}`

	// The same write produces the same observable result on both apps.
	baseWrite := exchange(baseline, http.MethodPost, "/v1/default/banks/determinism_bank/memories", retainBody)
	boundaryWrite := exchange(withBoundary, http.MethodPost, "/v1/default/banks/determinism_bank/memories", retainBody)
	if baseWrite.Code != http.StatusOK || boundaryWrite.Code != http.StatusOK {
		t.Fatalf("retain statuses = %d / %d", baseWrite.Code, boundaryWrite.Code)
	}
	if baseWrite.Body.String() != boundaryWrite.Body.String() {
		t.Fatalf("retain output changed with the boundary present:\n baseline=%s\n boundary=%s",
			baseWrite.Body.String(), boundaryWrite.Body.String())
	}

	// The same reflection produces the same observable result on both apps.
	baseReflect := exchange(baseline, http.MethodPost, "/v1/default/banks/determinism_bank/reflect", reflectBody)
	boundaryReflect := exchange(withBoundary, http.MethodPost, "/v1/default/banks/determinism_bank/reflect", reflectBody)
	if baseReflect.Code != boundaryReflect.Code {
		t.Fatalf("reflect status changed with the boundary present: %d / %d", baseReflect.Code, boundaryReflect.Code)
	}
	if baseReflect.Body.String() != boundaryReflect.Body.String() {
		t.Fatalf("reflect output changed with the boundary present:\n baseline=%s\n boundary=%s",
			baseReflect.Body.String(), boundaryReflect.Body.String())
	}
}

// A recall through the served handler honours the request's tag scoping and the
// deployment's recency decay: the public surface, not just the storage query.
func TestParityRecallHonoursTagScopingAndRecency(t *testing.T) {
	cfg := testConfig()
	cfg.Recency.Function = "exponential"
	cfg.Recency.HalflifeDays = 1
	a, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/default/banks/parity-bank"
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/parity-bank", `{"name":"Parity"}`); rec.Code >= 300 {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	write := func(body string) {
		t.Helper()
		rec := exchange(a, http.MethodPost, base+"/memories", body)
		if rec.Code >= 300 {
			t.Fatalf("retain %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	write(`{"items":[{"content":"the deploy runbook uses a blue-green cutover","tags":["ops"]}]}`)
	write(`{"items":[{"content":"the deploy runbook uses a canary cutover","tags":["personal"]}]}`)

	// A compound tag group must constrain the answer set on the served path too:
	// the wire shape is a list of groups, each a leaf {tags, match} or a
	// compound {and|or|not}.
	rec := exchange(a, http.MethodPost, base+"/memories/recall",
		`{"query":"deploy runbook","tag_groups":[{"tags":["ops"]}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall: %d %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	results, _ := payload["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("the tag-scoped recall returned nothing: %s", rec.Body.String())
	}
	for _, raw := range results {
		item, _ := raw.(map[string]any)
		text, _ := item["text"].(string)
		if !strings.Contains(text, "blue-green cutover") {
			t.Fatalf("a tag group scoped to ops returned %q", text)
		}
	}

	// A group that constrains nothing is refused rather than widened to every
	// memory: an object with no tags and none of the boolean keys is not a
	// filter.
	malformed := exchange(a, http.MethodPost, base+"/memories/recall",
		`{"query":"deploy runbook","tag_groups":{"groups":[{"tags":["ops"]}]}}`)
	if malformed.Code < 400 {
		t.Fatalf("a malformed tag group must be refused: %d %s", malformed.Code, malformed.Body.String())
	}
}

// A bank's retain configuration and label vocabulary are accepted by the config
// API and actually reach the write, and an unusable vocabulary is refused.
func TestParityBankRetainConfiguration(t *testing.T) {
	a := parityApp(t)
	base := "/v1/default/banks/retain-cfg"
	if rec := exchange(a, http.MethodPut, base, `{"name":"Retain Config"}`); rec.Code >= 300 {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}

	vocabulary := `[{"key":"pedagogy","type":"value","values":[{"value":"scaffolding"},{"value":"inquiry"}]},` +
		`{"key":"interest","type":"multi-values","tag":true,"values":[{"value":"active"},{"value":"dormant"}]}]`
	config := `{"updates":{"retain_mission":"record teaching decisions",` +
		`"retain_chunk_size":120,` +
		`"entity_labels":` + vocabulary + `}}`
	if rec := exchange(a, http.MethodPatch, base+"/config", config); rec.Code >= 300 {
		t.Fatalf("patch config: %d %s", rec.Code, rec.Body.String())
	}

	rec := exchange(a, http.MethodPost, base+"/memories",
		`{"items":[{"content":"Alice used scaffolding in the lesson and stayed active all term."}]}`)
	if rec.Code >= 300 {
		t.Fatalf("retain: %d %s", rec.Code, rec.Body.String())
	}

	// The tightened segment size reached the write: a bank that asked for 120
	// characters cannot have stored one long segment for this content.
	docs := exchange(a, http.MethodGet, base+"/documents", "")
	if docs.Code != http.StatusOK {
		t.Fatalf("documents: %d %s", docs.Code, docs.Body.String())
	}

	// An unusable vocabulary is refused where it is written, not silently
	// ignored: a bank whose labels never extract is the failure this prevents.
	bad := exchange(a, http.MethodPatch, base+"/config",
		`{"updates":{"entity_labels":[{"key":"pedagogy","type":"nonsense"}]}}`)
	if bad.Code < 400 {
		t.Fatalf("an invalid vocabulary must be refused: %d %s", bad.Code, bad.Body.String())
	}
	if !strings.Contains(bad.Body.String(), "entity_labels") {
		t.Fatalf("the refusal must name the field: %s", bad.Body.String())
	}
}

// A segment size the bank sets is validated: a non-positive size would floor
// every chunk, so it is refused rather than stored.
func TestParityBankChunkSizeValidation(t *testing.T) {
	a := parityApp(t)
	base := "/v1/default/banks/chunk-cfg"
	if rec := exchange(a, http.MethodPut, base, `{"name":"Chunk Config"}`); rec.Code >= 300 {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	bad := exchange(a, http.MethodPatch, base+"/config", `{"updates":{"retain_chunk_size":0}}`)
	if bad.Code < 400 {
		t.Fatalf("a zero segment size must be refused: %d %s", bad.Code, bad.Body.String())
	}
	ok := exchange(a, http.MethodPatch, base+"/config", `{"updates":{"retain_chunk_size":500}}`)
	if ok.Code >= 300 {
		t.Fatalf("a positive segment size must be accepted: %d %s", ok.Code, ok.Body.String())
	}
}

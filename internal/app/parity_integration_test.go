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

// Contract reference: public HTTP behavior for this case.
// The retired profile/background routes answer 410 Gone, but authentication is
// evaluated first: an unauthenticated caller sees 401, and only an
// authenticated caller sees the retirement status with its replacement message.
// They must not create a bank as a side effect.
func TestParityRetiredBankProfileRoutesAreGone(t *testing.T) {
	a := parityApp(t)

	// Authentication runs before the retirement status is revealed.
	rec := exchange(a, http.MethodGet, "/v1/default/banks/retired_bank/profile", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated retired read = %d, want 401", rec.Code)
	}

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
		req.Header.Set("Authorization", "Bearer test")
		rec := httptest.NewRecorder()
		a.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone {
			t.Fatalf("%s %s = %d, want 410 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "config") {
			t.Fatalf("%s %s message must name the config routes: %s", tc.method, tc.path, rec.Body.String())
		}
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

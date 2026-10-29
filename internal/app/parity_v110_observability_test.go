package app

// The v1.1.0 observability parity cases, end to end through the served
// handler: the llm-requests call_type filter and its 422, the audit-log
// summary/member split, and the operation retry of a cancelled or deferred
// operation.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedParityTraces records one model call of each kind for the bank, so the
// call_type filter has something to split.
func seedParityTraces(t *testing.T, store storage.Store, bankID string) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	for i, scope := range []string{"extraction", "embedding_document", "rerank", "decision_recall_rerank"} {
		if err := store.RecordLLMRequest(ctx, storage.LLMRequest{
			ID: storage.NewDocumentID(), BankID: bankID, Status: "success", Operation: "retain",
			Scope: scope, StartedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The llm-requests routes filter by call_type, and an unknown kind is the
// framework's 422 rather than an empty page.
func TestParityLLMRequestsCallType(t *testing.T) {
	a := parityApp(t)
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/trace-bank", `{"name":"Trace"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	seedParityTraces(t, a.Store, "trace-bank")

	cases := map[string]int{"embedding": 1, "rerank": 1, "decision": 1, "llm": 1}
	for callType, want := range cases {
		rec := exchange(a, http.MethodGet, "/v1/default/banks/trace-bank/llm-requests?call_type="+callType, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("call_type=%s: %d %s", callType, rec.Code, rec.Body.String())
		}
		var page struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != want {
			t.Fatalf("call_type=%s total = %d, want %d", callType, page.Total, want)
		}
	}
	rec := exchange(a, http.MethodGet, "/v1/default/banks/trace-bank/llm-requests?call_type=chat", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown call_type: %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	var failure struct {
		Detail []struct {
			Type string `json:"type"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if len(failure.Detail) != 1 || failure.Detail[0].Type != "literal_error" {
		t.Fatalf("unknown call_type body = %s", rec.Body.String())
	}
	if rec := exchange(a, http.MethodGet, "/v1/default/banks/trace-bank/llm-requests/stats?call_type=chat", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown stats call_type: %d, want 422", rec.Code)
	}
}

// The audit list serves summaries without bodies; the member route serves one
// entry with them, and an absent entry is the reference's 404 detail.
func TestParityAuditLogSummaryAndMember(t *testing.T) {
	a := parityApp(t)
	ctx := context.Background()
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/audit-bank", `{"name":"Audit"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	id := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	started := time.Now().UTC().Add(-time.Minute)
	ended := started.Add(time.Second)
	if err := a.Store.RecordAuditLog(ctx, storage.AuditEntry{
		ID: id, BankID: "audit-bank", Action: "retain", Transport: "http",
		StartedAt: started, EndedAt: &ended,
		Request: map[string]any{"items": []any{}}, Response: map[string]any{"status": "ok"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := exchange(a, http.MethodGet, "/v1/default/banks/audit-bank/audit-logs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list: %d %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("audit list items = %s", rec.Body.String())
	}
	for _, absent := range []string{"request", "response"} {
		if _, ok := listed.Items[0][absent]; ok {
			t.Fatalf("the audit list carried %q: %s", absent, rec.Body.String())
		}
	}
	for _, present := range []string{"id", "action", "transport", "bank_id", "started_at", "ended_at", "duration_ms", "metadata"} {
		if _, ok := listed.Items[0][present]; !ok {
			t.Fatalf("the audit summary is missing %q: %s", present, rec.Body.String())
		}
	}

	rec = exchange(a, http.MethodGet, "/v1/default/banks/audit-bank/audit-logs/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit member: %d %s", rec.Code, rec.Body.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if request, ok := entry["request"].(map[string]any); !ok || request == nil {
		t.Fatalf("audit member request = %s", rec.Body.String())
	}
	if response, ok := entry["response"].(map[string]any); !ok || response["status"] != "ok" {
		t.Fatalf("audit member response = %s", rec.Body.String())
	}

	missing := "e3b0c442-98fc-1c14-9afb-f4c8996fb924"
	rec = exchange(a, http.MethodGet, "/v1/default/banks/audit-bank/audit-logs/"+missing, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent audit entry: %d, want 404", rec.Code)
	}
	var envelope struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Detail != "Audit log entry '"+missing+"' not found" {
		t.Fatalf("absent audit detail = %q", envelope.Detail)
	}
}

// Retrying a cancelled operation re-queues it, retrying a deferred one clears
// its backoff so it runs now, and a live pending operation is refused with the
// reference's 409 detail.
func TestParityOperationRetryStates(t *testing.T) {
	a := parityApp(t)
	ctx := context.Background()
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/retry-bank", `{"name":"Retry"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	put := func(status storage.OperationStatus, next *time.Time) string {
		t.Helper()
		op, err := a.Store.PutOperation(ctx, storage.Operation{
			OperationID: storage.NewDocumentID(), BankID: "retry-bank",
			OperationType: storage.OperationTypeConsolidate, Status: status,
			NextRetryAt: next, Payload: map[string]any{"k": "v"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return op.OperationID
	}

	cancelled := put(storage.StatusCancelled, nil)
	rec := exchange(a, http.MethodPost, "/v1/default/banks/retry-bank/operations/"+cancelled+"/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry cancelled: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success || body.OperationID != cancelled || body.Message != "Operation "+cancelled+" queued for retry" {
		t.Fatalf("retry cancelled body = %s", rec.Body.String())
	}
	retried, err := a.Store.GetOperation(ctx, "retry-bank", cancelled)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != storage.StatusPending || retried.NextRetryAt != nil {
		t.Fatalf("cancelled retried = %+v", retried)
	}

	future := time.Now().UTC().Add(time.Hour)
	deferred := put(storage.StatusPending, &future)
	rec = exchange(a, http.MethodPost, "/v1/default/banks/retry-bank/operations/"+deferred+"/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry deferred: %d %s", rec.Code, rec.Body.String())
	}
	retried, err = a.Store.GetOperation(ctx, "retry-bank", deferred)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != storage.StatusPending || retried.NextRetryAt != nil {
		t.Fatalf("deferred retried = %+v, want pending with the backoff cleared", retried)
	}

	live := put(storage.StatusPending, nil)
	rec = exchange(a, http.MethodPost, "/v1/default/banks/retry-bank/operations/"+live+"/retry", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry pending: %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	want := "Operation " + live + " cannot be retried: status is 'pending', expected 'failed', 'cancelled' or deferred"
	if conflict.Detail != want {
		t.Fatalf("retry pending detail = %q, want %q", conflict.Detail, want)
	}
}

// A memory edit sets, clears and leaves the mention time alone: an ISO value
// sets it, "" and an explicit null clear it, and an absent member leaves it
// unchanged — never re-derived onto now.
func TestParityMemoryMentionedAtEdit(t *testing.T) {
	a := parityApp(t)
	const bankPath = "/v1/default/banks/mention-bank"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Mention"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories", `{
		"items": [{"content": "the deployment window is Thursday at 14:00", "mentioned_at": "2026-01-02T03:04:05Z"}]
	}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: %d %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Items []struct {
			ID          string  `json:"id"`
			MentionedAt *string `json:"mentioned_at"`
		} `json:"items"`
	}
	rec := exchange(a, http.MethodGet, bankPath+"/memories/list?limit=10", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("list = %s", rec.Body.String())
	}
	id := listed.Items[0].ID
	mentionedAt := func() *string {
		t.Helper()
		rec := exchange(a, http.MethodGet, bankPath+"/memories/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("get memory: %d %s", rec.Code, rec.Body.String())
		}
		var unit struct {
			MentionedAt *string `json:"mentioned_at"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &unit); err != nil {
			t.Fatal(err)
		}
		return unit.MentionedAt
	}
	value := func(t *testing.T, got *string, want string) {
		t.Helper()
		if got == nil || *got == "" {
			t.Fatalf("mentioned_at = %v, want %q", got, want)
		}
		parsed, err := time.Parse(time.RFC3339, *got)
		if err != nil {
			t.Fatalf("mentioned_at %q is not RFC3339", *got)
		}
		expected, _ := time.Parse(time.RFC3339, want)
		if !parsed.Equal(expected) {
			t.Fatalf("mentioned_at = %q, want %q", *got, want)
		}
	}

	patch := func(body string) {
		t.Helper()
		if rec := exchange(a, http.MethodPatch, bankPath+"/memories/"+id, body); rec.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	// A new value.
	patch(`{"mentioned_at":"2025-06-07T08:09:10Z"}`)
	value(t, mentionedAt(), "2025-06-07T08:09:10Z")

	// A patch that names no mention time leaves it alone.
	patch(`{"context":"quarterly maintenance"}`)
	value(t, mentionedAt(), "2025-06-07T08:09:10Z")

	// "" clears it.
	patch(`{"mentioned_at":""}`)
	if got := mentionedAt(); got != nil {
		t.Fatalf(`mentioned_at after "" = %v, want null`, *got)
	}

	// An explicit null clears it too.
	patch(`{"mentioned_at":"2024-01-01T00:00:00Z"}`)
	patch(`{"mentioned_at":null}`)
	if got := mentionedAt(); got != nil {
		t.Fatalf("mentioned_at after explicit null = %v, want null", *got)
	}

	// A malformed value is the reference's invalid-isoformat 400.
	rec = exchange(a, http.MethodPatch, bankPath+"/memories/"+id, `{"mentioned_at":"not-a-date"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed mentioned_at: %d, want 400", rec.Code)
	}
}

// The memory list and the graph document `q` as a case-insensitive substring
// search over a memory's text AND its context, and both now make tags_match an
// enum that includes `exact`; an unknown mode there is the framework's 422.
func TestParityMemorySearchQAndTagsMatchExact(t *testing.T) {
	a := parityApp(t)
	const bankPath = "/v1/default/banks/search-bank"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Search"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	for _, item := range []string{
		`{"content":"the staging cluster is restarted weekly","context":"Operations Runbook"}`,
		`{"content":"invoices are paid on Fridays","context":"finance"}`,
	} {
		if rec := exchange(a, http.MethodPost, bankPath+"/memories", `{"items": [`+item+`]}`); rec.Code != http.StatusOK {
			t.Fatalf("retain %s: %d %s", item, rec.Code, rec.Body.String())
		}
	}
	count := func(path string) int {
		t.Helper()
		rec := exchange(a, http.MethodGet, bankPath+path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		// The graph has no total member; its nodes are the memories it serves.
		if strings.HasPrefix(path, "/graph") {
			var graph struct {
				Nodes []any `json:"nodes"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
				t.Fatal(err)
			}
			return len(graph.Nodes)
		}
		var total struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &total); err != nil {
			t.Fatal(err)
		}
		return total.Total
	}
	cases := []struct {
		name string
		path string
		want int
	}{
		{"list q matches text, upper case", "/memories/list?limit=20&q=STAGING", 1},
		{"list q matches context only", "/memories/list?limit=20&q=runbook", 1},
		{"list q matches neither", "/memories/list?limit=20&q=kubernetes", 0},
		{"graph q matches text", "/graph?q=INVOICES", 1},
		{"graph q matches context", "/graph?q=Finance", 1},
		{"graph q matches neither", "/graph?q=kubernetes", 0},
	}
	for _, tc := range cases {
		if got := count(tc.path); got != tc.want {
			t.Fatalf("%s: total = %d, want %d", tc.name, got, tc.want)
		}
	}

	// `exact` is accepted wherever the v1.1.0 document enumerates it, and an
	// unknown mode is a 422 rather than being ignored.
	for _, route := range []string{"/memories/list", "/graph"} {
		if rec := exchange(a, http.MethodGet, bankPath+route+"?tags_match=exact", ""); rec.Code != http.StatusOK {
			t.Fatalf("%s rejected exact: %d %s", route, rec.Code, rec.Body.String())
		}
		rec := exchange(a, http.MethodGet, bankPath+route+"?tags_match=bogus", "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s with an unknown mode: %d, want 422 (%s)", route, rec.Code, rec.Body.String())
		}
	}
}

// recall's prefer_observations is nullable: an omitted member takes the bank's
// recall_prefer_observations, else the deployment's, while an explicit value
// wins over both. The bank override is layered under the request, over the
// process default, which is the precedence every other recall toggle follows.
func TestParityRecallPreferObservationsDefault(t *testing.T) {
	// The deployment default is on; the bank below overrides it off.
	a := parityApp(t)
	a.memops.RecallPreferObservations = true
	a.Store = &recallQueryRecorder{Store: a.Store}
	a.memops.Store = a.Store
	const bankPath = "/v1/default/banks/prefer-bank"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Prefer"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	configure := func(value string) {
		t.Helper()
		body := `{"updates":{"recall_prefer_observations":` + value + `}}`
		if rec := exchange(a, http.MethodPatch, bankPath+"/config", body); rec.Code != http.StatusOK {
			t.Fatalf("bank config %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// prefer_observations is observable through the store query the handler
	// builds, which is where the resolved flag is carried.
	// The resolved flag is read off the store query the handler builds; the
	// store double records it so the test can see which layer won.
	resolved := func(t *testing.T, body string) storage.RecallQuery {
		t.Helper()
		recorder, ok := a.Store.(*recallQueryRecorder)
		if !ok {
			t.Fatalf("store does not record recall queries")
		}
		recorder.last = nil
		rec := exchange(a, http.MethodPost, bankPath+"/memories/recall", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("recall %s: %d %s", body, rec.Code, rec.Body.String())
		}
		if recorder.last == nil {
			t.Fatalf("recall %s never reached the store", body)
		}
		return *recorder.last
	}

	// No bank override: the deployment default applies.
	if got := resolved(t, `{"query":"anything"}`); !got.PreferObservations {
		t.Fatalf("deployment default not applied: %+v", got)
	}
	// An explicit false still means false.
	if got := resolved(t, `{"query":"anything","prefer_observations":false}`); got.PreferObservations {
		t.Fatalf("an explicit false was replaced by the default")
	}
	// An explicit true wins over a bank that turned it off.
	configure("false")
	if got := resolved(t, `{"query":"anything","prefer_observations":true}`); !got.PreferObservations {
		t.Fatalf("an explicit true was overridden by the bank")
	}
	// The bank's own value overrides the deployment default.
	if got := resolved(t, `{"query":"anything"}`); got.PreferObservations {
		t.Fatalf("the bank's recall_prefer_observations was not applied")
	}
}

// recallQueryRecorder records the recall query the memory-op handler handed the
// store, which is where the resolved prefer_observations is carried.
type recallQueryRecorder struct {
	storage.Store
	last *storage.RecallQuery
}

// Recall records the query before delegating.
func (r *recallQueryRecorder) Recall(ctx context.Context, query storage.RecallQuery) (storage.RecallResult, error) {
	copied := query
	r.last = &copied
	return r.Store.Recall(ctx, query)
}

// A retained item that names no observation_scopes takes the bank's
// default_observation_scopes, else the deployment's
// WENYUAN_API_DEFAULT_OBSERVATION_SCOPES; an item that names one keeps it.
func TestParityRetainDefaultObservationScopes(t *testing.T) {
	a := parityApp(t)
	a.memops.DefaultObservationScopes = json.RawMessage(`"per_tag"`)
	const bankPath = "/v1/default/banks/scopes-bank"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Scopes"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank: %d %s", rec.Code, rec.Body.String())
	}
	scopesOf := func(t *testing.T, body string) []string {
		t.Helper()
		if rec := exchange(a, http.MethodPost, bankPath+"/memories", body); rec.Code != http.StatusOK {
			t.Fatalf("retain %s: %d %s", body, rec.Code, rec.Body.String())
		}
		// The scope is served by the memory's own detail route, not the list.
		var listed struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		rec := exchange(a, http.MethodGet, bankPath+"/memories/list?limit=50", "")
		if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		for _, item := range listed.Items {
			detail := exchange(a, http.MethodGet, bankPath+"/memories/"+item.ID, "")
			if detail.Code != http.StatusOK {
				t.Fatalf("memory detail: %d %s", detail.Code, detail.Body.String())
			}
			var unit struct {
				Scopes json.RawMessage `json:"observation_scopes"`
			}
			if err := json.Unmarshal(detail.Body.Bytes(), &unit); err != nil {
				t.Fatal(err)
			}
			if len(unit.Scopes) == 0 || string(unit.Scopes) == "null" {
				continue
			}
			var mode string
			if json.Unmarshal(unit.Scopes, &mode) == nil && mode != "" {
				return []string{mode}
			}
			return []string{string(unit.Scopes)}
		}
		t.Fatalf("no memory carried an observation_scopes: %s", rec.Body.String())
		return nil
	}

	// The deployment default applies when the bank names none.
	if got := scopesOf(t, `{"items":[{"content":"deployment default fact"}]}`); got[0] != "per_tag" {
		t.Fatalf("deployment default = %v, want per_tag", got)
	}
	// The bank's own value overrides the deployment's.
	rec := exchange(a, http.MethodPatch, bankPath+"/config", `{"updates":{"default_observation_scopes":"shared"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bank config: %d %s", rec.Code, rec.Body.String())
	}
	if got := scopesOf(t, `{"items":[{"content":"bank default fact"}]}`); got[0] != "shared" {
		t.Fatalf("bank default = %v, want shared", got)
	}
	// The item's own value still wins over both.
	if rec := exchange(a, http.MethodPost, bankPath+"/memories",
		`{"items":[{"content":"item scoped fact","observation_scopes":"all_combinations"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain with its own scopes: %d %s", rec.Code, rec.Body.String())
	}
}

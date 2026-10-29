package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// auditEntryID is the entry the member route serves.
const auditEntryID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

// scopedReportingHandler seeds a bank with one audit entry and wraps the
// family's routes in the real tag-scope middleware, so a keyed request resolves
// a scope and an unkeyed one carries none.
func scopedReportingHandler(t *testing.T) http.Handler {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAuditLog(context.Background(), storage.AuditEntry{
		ID: auditEntryID, BankID: bank, Action: "retain", Transport: "http",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return scopedHandler(t, store)
}

// The reference refuses a tag-scoped caller on every whole-bank route in the
// reporting family with its own sentence, while a request with no scope is
// untouched and answers exactly as before.
func TestReportingRefusesBankWideRoutesToScopedCallers(t *testing.T) {
	handler := scopedReportingHandler(t)
	base := "/v1/default/banks/" + bank
	cases := []struct {
		method string
		path   string
		body   string
		what   string
	}{
		{http.MethodGet, base + "/stats", "", "read the bank's statistics"},
		{http.MethodGet, base + "/audit-logs", "", "read the bank's audit log"},
		{http.MethodGet, base + "/audit-logs/stats", "", "read the bank's audit log"},
		{http.MethodGet, base + "/audit-logs/" + auditEntryID, "", "read the bank's audit log"},
		{http.MethodGet, base + "/llm-requests", "", "read the bank's LLM requests"},
		{http.MethodGet, base + "/llm-requests/stats", "", "read the bank's LLM requests"},
		{http.MethodDelete, base + "/observations", "", "clear the bank's observations"},
		{http.MethodPost, base + "/consolidation/recover", "", "retry the bank's failed consolidation"},
		{http.MethodPost, base + "/consolidate", "", "run consolidation on the bank"},
	}
	for _, tc := range cases {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			scoped := sendKeyedBody(handler, "dan", tc.method, tc.path, tc.body)
			if scoped.Code != http.StatusForbidden {
				t.Fatalf("scoped = %d, want 403 (body %s)", scoped.Code, scoped.Body.String())
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(scoped.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if want := "A tag-scoped caller can't " + tc.what; envelope.Detail != want {
				t.Fatalf("detail = %q, want %q", envelope.Detail, want)
			}

			unscoped := sendKeyedBody(handler, "", tc.method, tc.path, tc.body)
			if unscoped.Code != http.StatusOK {
				t.Fatalf("unscoped = %d, want 200 (body %s)", unscoped.Code, unscoped.Body.String())
			}
			if strings.Contains(unscoped.Body.String(), "A tag-scoped caller") {
				t.Fatalf("unscoped body carries a refusal: %s", unscoped.Body.String())
			}
		})
	}
}

// The reference orders the single-entry audit read: the identifier's uuid
// parse, then the bank profile, then the bank-wide refusal, and only then the
// entry's own SELECT (memory_engine.py get_audit_log) — so a tag-scoped caller
// is refused whether or not the entry exists, an identifier that does not parse
// is the entry 404 before any scope is consulted, and the unknown bank is that
// same 404, since upstream maps every None the engine returns to it.
func TestAuditLogMemberRefusalRunsBeforeTheLookup(t *testing.T) {
	handler := scopedReportingHandler(t)
	base := "/v1/default/banks/" + bank

	scoped := sendKeyed(handler, "dan", http.MethodGet, base+"/audit-logs/e3b0c442-98fc-1c14-9afb-f4c8996fb924")
	if scoped.Code != http.StatusForbidden {
		t.Fatalf("scoped caller with a missing entry = %d, want 403: %s",
			scoped.Code, scoped.Body.String())
	}
	if got, want := strings.TrimSpace(scoped.Body.String()),
		`{"detail":"A tag-scoped caller can't read the bank's audit log"}`; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}

	scoped = sendKeyed(handler, "dan", http.MethodGet, base+"/audit-logs/not-a-uuid")
	if scoped.Code != http.StatusNotFound {
		t.Fatalf("scoped caller with a malformed id = %d, want 404: %s",
			scoped.Code, scoped.Body.String())
	}
	if want := "Audit log entry 'not-a-uuid' not found"; !strings.Contains(scoped.Body.String(), want) {
		t.Fatalf("body = %s, want %q", scoped.Body.String(), want)
	}

	// Without a scope the unknown bank is still the entry's own 404, named as
	// the reference names it.
	mux := serveRoutes(t, storage.NewMemoryStore("public"))
	unscoped := send(mux, http.MethodGet, "/v1/default/banks/no-such-bank/audit-logs/"+auditEntryID, "")
	if unscoped.Code != http.StatusNotFound {
		t.Fatalf("unknown bank = %d, want 404: %s", unscoped.Code, unscoped.Body.String())
	}
	if want := "Audit log entry '" + auditEntryID + "' not found"; !strings.Contains(unscoped.Body.String(), want) {
		t.Fatalf("body = %s, want %q", unscoped.Body.String(), want)
	}
}

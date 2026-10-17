package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// scopedReportingHandler seeds a bank and wraps the family's routes in the real
// tag-scope middleware, so a keyed request resolves a scope and an unkeyed one
// carries none.
func scopedReportingHandler(t *testing.T) http.Handler {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
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

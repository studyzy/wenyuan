package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// bankWideMux binds the family's routes with path values set, as the router
// does, over a store holding one bank.
func bankWideMux(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "b1")
			r.SetPathValue(OperationIDPathParam, "11111111-1111-1111-1111-111111111111")
			handler(w, r)
		})
	})
	return mux
}

// The reference refuses a tag-scoped caller on the whole-bank operation routes
// and the bank-wide clear, with its own sentence, while an unscoped request is
// untouched.
func TestMemoryOpsRefusesBankWideRoutesToScopedCallers(t *testing.T) {
	mux := bankWideMux(t)
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
	handler := httpx.Chain(mux, httpx.OperationValidationMiddleware(host))

	base := "/v1/default/banks/b1"
	cases := []struct {
		method string
		path   string
		what   string
	}{
		{http.MethodGet, base + "/operations", "list the bank's operations"},
		{http.MethodDelete, base + "/operations/11111111-1111-1111-1111-111111111111", "cancel the bank's operations"},
		{http.MethodPost, base + "/operations/11111111-1111-1111-1111-111111111111/retry", "retry the bank's operations"},
		{http.MethodDelete, base + "/operations/11111111-1111-1111-1111-111111111111/delete", "delete the bank's operations"},
		{http.MethodDelete, base + "/memories", "delete or clear the bank"},
	}
	for _, tc := range cases {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			scoped := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-API-Key", "dan")
			handler.ServeHTTP(scoped, req)
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

			unscoped := httptest.NewRecorder()
			handler.ServeHTTP(unscoped, httptest.NewRequest(tc.method, tc.path, nil))
			if unscoped.Code == http.StatusForbidden || strings.Contains(unscoped.Body.String(), "A tag-scoped caller") {
				t.Fatalf("unscoped = %d refused: %s", unscoped.Code, unscoped.Body.String())
			}
		})
	}
}

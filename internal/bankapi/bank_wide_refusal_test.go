package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference refuses a tag-scoped caller on every whole-bank bank-lifecycle
// route (mission/config, aliases, delete) with its own sentence, while a
// request with no scope answers exactly as before.
func TestBankAPIRefusesBankWideRoutesToScopedCallers(t *testing.T) {
	base := "/v1/default/banks/b1"
	newHandler := func(t *testing.T) http.Handler {
		store := storage.NewMemoryStore("public")
		mustCreate(t, store, storage.Bank{BankID: "b1", Name: "Bank"})
		if _, err := store.AddAlias(t.Context(), "b1", "a1", false); err != nil {
			t.Fatal(err)
		}
		mux := newMux(NewHandlers(store).WithConfigAPI(true))
		host := httpx.NewExtensionHost()
		host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
		return httpx.Chain(mux, httpx.OperationValidationMiddleware(host))
	}

	cases := []struct {
		method       string
		path         string
		body         string
		what         string
		wantUnscoped int
	}{
		{http.MethodPut, base, `{}`, "change the bank's mission or config", http.StatusOK},
		{http.MethodPatch, base, `{}`, "change the bank's mission or config", http.StatusOK},
		{http.MethodDelete, base, "", "delete or clear the bank", http.StatusOK},
		{http.MethodPost, base + "/aliases", `{"alias":"a2"}`, "change the bank's aliases", http.StatusCreated},
		{http.MethodPatch, base + "/aliases/a1", `{"primary":true}`, "change the bank's aliases", http.StatusOK},
		{http.MethodDelete, base + "/aliases/a1", "", "change the bank's aliases", http.StatusOK},
		{http.MethodPatch, base + "/config", `{"updates":{}}`, "change the bank's config", http.StatusOK},
		{http.MethodDelete, base + "/config", "", "reset the bank's config", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			scoped := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-API-Key", "dan")
			newHandler(t).ServeHTTP(scoped, req)
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
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			newHandler(t).ServeHTTP(unscoped, req)
			if unscoped.Code != tc.wantUnscoped {
				t.Fatalf("unscoped = %d, want %d (body %s)", unscoped.Code, tc.wantUnscoped, unscoped.Body.String())
			}
		})
	}
}

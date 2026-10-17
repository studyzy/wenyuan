package webhooks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// The reference refuses a tag-scoped caller on the whole-bank webhook routes
// with its own sentence, while a request with no scope answers as before.
func TestWebhooksRefuseBankWideRoutesToScopedCallers(t *testing.T) {
	base := "/v1/default/banks/b1"
	const webhookID = "11111111-1111-1111-1111-111111111111"
	newHandler := func(t *testing.T) http.Handler {
		mux, _ := newServer(t)
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
		{http.MethodGet, base + "/webhooks", "", "list the bank's webhooks", http.StatusOK},
		{http.MethodPost, base + "/webhooks", `{"url":"https://example.test/hook"}`, "change the bank's webhooks", http.StatusCreated},
		{http.MethodPatch, base + "/webhooks/" + webhookID, `{"enabled":false}`, "change the bank's webhooks", http.StatusNotFound},
		{http.MethodDelete, base + "/webhooks/" + webhookID, "", "change the bank's webhooks", http.StatusNotFound},
		{http.MethodGet, base + "/webhooks/" + webhookID + "/deliveries", "", "read the bank's webhook deliveries", http.StatusNotFound},
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

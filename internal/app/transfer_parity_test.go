package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// test-transfer-routes-mounted: the whole transfer family resolves through the
// composed handler, and the retired synchronous read answers the terminal gone
// status rather than being absent.
func TestTransferRouteFamilyIsMounted(t *testing.T) {
	app, err := New(context.Background(), config.FromEnv(func(string) (string, bool) { return "", false }), Dependencies{
		Store: storage.NewMemoryStore("public"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "route-bank", Name: "route-bank"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"export", http.MethodGet, "/v1/default/banks/route-bank/export", http.StatusOK},
		{"retired document read", http.MethodGet, "/v1/default/banks/route-bank/document-transfer", http.StatusGone},
		{"transfer export submit", http.MethodPost, "/v1/default/banks/route-bank/transfer/export", http.StatusAccepted},
		{"clone without target", http.MethodPost, "/v1/default/banks/route-bank/clone", http.StatusBadRequest},
		{"attachment miss", http.MethodGet, "/v1/default/banks/route-bank/attachments/none", http.StatusNotFound},
		// The standard mux normalizes a dot-segment before the handler runs, so
		// the traversal never reaches the transfer code. Either the mux
		// redirects it away or the handler reports not-found; both are safe.
		{"download traversal", http.MethodGet, "/v1/default/files/download/../etc/passwd", http.StatusMovedPermanently},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			app.Handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// test-existing-routes-unchanged: mounting the transfer family must not change
// the behavior of the routes that already existed.
func TestExistingRoutesUnchangedAfterTransferMount(t *testing.T) {
	app, err := New(context.Background(), config.FromEnv(func(string) (string, bool) { return "", false }), Dependencies{
		Store: storage.NewMemoryStore("public"),
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"banks collection", http.MethodGet, "/v1/default/banks", http.StatusOK},
		// The retired routes are auth-gated before they report gone, so an
		// unauthenticated call is rejected first. Assert that ordering holds.
		{"retired profile read unauthenticated", http.MethodGet, "/v1/default/banks/any/profile", http.StatusUnauthorized},
		{"retired background unauthenticated", http.MethodPost, "/v1/default/banks/any/background", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			app.Handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}

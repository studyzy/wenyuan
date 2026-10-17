package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// test-bank-template-routes: /import and /export are the JSON bank-template
// pair, not the archive importer and the ZIP exporter.
func TestBankTemplateRoutesAreJSON(t *testing.T) {
	app, err := New(context.Background(), testConfig(), Dependencies{
		Store: storage.NewMemoryStore("public"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "tmpl-bank", Name: "tmpl-bank"}); err != nil {
		t.Fatal(err)
	}

	export := httptest.NewRecorder()
	app.Handler.ServeHTTP(export, httptest.NewRequest(http.MethodGet, "/v1/default/banks/tmpl-bank/export", nil))
	if export.Code != http.StatusOK {
		t.Fatalf("export = %d (body %s)", export.Code, export.Body.String())
	}
	if got := export.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("export must be JSON, got content type %q", got)
	}
	var manifest map[string]any
	if err := json.Unmarshal(export.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("export body is not a manifest: %v", err)
	}
	if manifest["version"] != "1" {
		t.Fatalf("export manifest = %v, want version 1", manifest)
	}

	importRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/tmpl-bank/import",
		strings.NewReader(`{"version":"1","directives":[{"name":"tone","content":"be nice"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.Handler.ServeHTTP(importRec, req)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import = %d (body %s)", importRec.Code, importRec.Body.String())
	}
	var result struct {
		DirectivesCreated []string `json:"directives_created"`
	}
	if err := json.Unmarshal(importRec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.DirectivesCreated) != 1 || result.DirectivesCreated[0] != "tone" {
		t.Fatalf("import created directives = %v", result.DirectivesCreated)
	}
}

// test-transfer-routes-mounted: the whole transfer family resolves through the
// composed handler, and the retired synchronous read answers the terminal gone
// status rather than being absent.
func TestTransferRouteFamilyIsMounted(t *testing.T) {
	app, err := New(context.Background(), testConfig(), Dependencies{
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
		{"clone without target", http.MethodPost, "/v1/default/banks/route-bank/clone", http.StatusUnprocessableEntity},
		{"attachment miss", http.MethodGet, "/v1/default/banks/route-bank/attachments/none", http.StatusNotFound},
		// A dot segment is routed as written; the transfer handler rejects the
		// parent-directory key as not-found, as the reference does, rather than
		// the mux redirecting the traversal away.
		{"download traversal", http.MethodGet, "/v1/default/files/download/../etc/passwd", http.StatusNotFound},
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
	app, err := New(context.Background(), testConfig(), Dependencies{
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
		// The retired routes raise the gone status before any authenticated
		// call, so an unauthenticated caller still learns the route is gone.
		{"retired profile read unauthenticated", http.MethodGet, "/v1/default/banks/any/profile", http.StatusGone},
		{"retired background unauthenticated", http.MethodPost, "/v1/default/banks/any/background", http.StatusGone},
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

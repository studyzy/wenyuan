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
		// call, so an unauthenticated caller still learns the route is gone. A
		// write route still validates its body first — the reference's framework
		// runs the model before the handler — so a bodyless write is the 422 for the
		// missing member rather than the retirement status.
		{"retired profile read unauthenticated", http.MethodGet, "/v1/default/banks/any/profile", http.StatusGone},
		{"retired background unauthenticated", http.MethodPost, "/v1/default/banks/any/background", http.StatusUnprocessableEntity},
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

// test-export-download-url-resolves: the archive URL an export publishes is
// fetched exactly as published. A bank identifier the storage key encodes
// leaves the key holding an escape of its own, and the download route splits
// the path it receives rather than decoding it, so a URL built from the raw key
// addresses a key nothing is stored under and answers not-found.
func TestExportDownloadURLResolvesForEncodedBankID(t *testing.T) {
	ctx := context.Background()
	app, err := New(ctx, testConfig(), Dependencies{
		Store: storage.NewMemoryStore("public"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const bankID = "coding-agent::wenyuan"
	if _, err := app.Store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatal(err)
	}

	// The key is the one an export of this bank stashes its archive under, so
	// its bank segment carries the escapes the bank identifier was encoded to.
	key := storage.ExportStorageKey(bankID, "op-1")
	if key == "banks/"+"coding-agent::wenyuan"+"/exports/op-1/transfer.zip" {
		t.Fatalf("the key must encode the bank segment to exercise this case: %q", key)
	}
	if err := app.Store.PutStoredFile(ctx, storage.StoredFile{
		Key:         key,
		ContentType: "application/zip",
		Data:        []byte("zip-bytes"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, downloadURLFor(key), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("the published download URL must resolve: %s = %d, want %d (body %s)",
			downloadURLFor(key), rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Body.String(); got != "zip-bytes" {
		t.Fatalf("body = %q, want the stored bytes", got)
	}
}

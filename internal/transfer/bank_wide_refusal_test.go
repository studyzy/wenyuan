package transfer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// assertScopedRefusal fails unless the response is the reference's 403 with the
// exact sentence for the refused operation.
func assertScopedRefusal(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("scoped = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if want := "A tag-scoped caller can't " + what; envelope.Detail != want {
		t.Fatalf("detail = %q, want %q", envelope.Detail, want)
	}
}

// scopedTransferServe runs one transfer handler through the real tag-scope
// middleware with the given API key, so a keyed request resolves a scope and an
// unkeyed one carries none.
func scopedTransferServe(t *testing.T, apiKey, method, target string, body *bytes.Buffer, contentType string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := routeRequest(method, target, body, map[string]string{"bank_id": "b1"})
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
	wrapped := httpx.OperationValidationMiddleware(host)(handler)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	return rec
}

// The reference refuses a tag-scoped caller on every bank-wide transfer route
// with its own sentence, while a request with no scope answers as before.
func TestTransferRefusesBankWideRoutesToScopedCallers(t *testing.T) {
	manifest := `{"version":"1","bank":{"reflect_mission":"help"}}`

	cases := []struct {
		name         string
		method       string
		target       string
		body         string
		multipart    bool
		what         string
		wantUnscoped int
		pick         func(*Handlers) http.HandlerFunc
	}{
		{
			name: "export-template", method: http.MethodGet, target: "/v1/default/banks/b1/export",
			what: "export the bank template", wantUnscoped: http.StatusOK,
			pick: func(h *Handlers) http.HandlerFunc { return h.ExportTemplate },
		},
		{
			name: "import-template", method: http.MethodPost, target: "/v1/default/banks/b1/import", body: manifest,
			what: "import a bank template", wantUnscoped: http.StatusOK,
			pick: func(h *Handlers) http.HandlerFunc { return h.ImportTemplate },
		},
		{
			name: "document-transfer-export", method: http.MethodPost, target: "/v1/default/banks/b1/document-transfer/export",
			what: "export documents", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitDocumentTransferExport },
		},
		{
			name: "document-transfer-import", method: http.MethodPost, target: "/v1/default/banks/b1/document-transfer", multipart: true,
			what: "import documents", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitDocumentImport },
		},
		{
			name: "transfer-export-bank", method: http.MethodPost, target: "/v1/default/banks/b1/transfer/export?include_data=true",
			what: "export the bank", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitTransferExport },
		},
		{
			name: "transfer-export-documents", method: http.MethodPost, target: "/v1/default/banks/b1/transfer/export?include_bank_config=false&include_history=false&document_id=doc-1",
			what: "export documents", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitTransferExport },
		},
		{
			name: "transfer-import-restore", method: http.MethodPost, target: "/v1/default/banks/b1/transfer/import?mode=restore&include_data=true&target_bank_id=restore-target", multipart: true,
			what: "import into the bank", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitTransferImport },
		},
		{
			name: "transfer-import-merge", method: http.MethodPost, target: "/v1/default/banks/b1/transfer/import?mode=merge", multipart: true,
			what: "import documents", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitTransferImport },
		},
		{
			name: "clone", method: http.MethodPost, target: "/v1/default/banks/b1/clone?target_bank_id=clone-target&include_data=true",
			what: "clone the bank", wantUnscoped: http.StatusAccepted,
			pick: func(h *Handlers) http.HandlerFunc { return h.SubmitClone },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scopedHandlers, scopedStore := newTestHandlers(t)
			seedBank(t, scopedStore, "b1")
			var scopedBody *bytes.Buffer
			contentType := "application/json"
			if tc.multipart {
				scopedBody, contentType = multipartArchive(t, archiveBytes(t, "b1"))
			} else if tc.body != "" {
				scopedBody = bytes.NewBufferString(tc.body)
			}
			scoped := scopedTransferServe(t, "dan", tc.method, tc.target, scopedBody, contentType, tc.pick(scopedHandlers))
			assertScopedRefusal(t, scoped, tc.what)

			unscopedHandlers, unscopedStore := newTestHandlers(t)
			seedBank(t, unscopedStore, "b1")
			var unscopedBody *bytes.Buffer
			if tc.multipart {
				unscopedBody, contentType = multipartArchive(t, archiveBytes(t, "b1"))
			} else if tc.body != "" {
				unscopedBody = bytes.NewBufferString(tc.body)
			}
			unscoped := scopedTransferServe(t, "", tc.method, tc.target, unscopedBody, contentType, tc.pick(unscopedHandlers))
			if unscoped.Code != tc.wantUnscoped {
				t.Fatalf("unscoped = %d, want %d (body %s)", unscoped.Code, tc.wantUnscoped, unscoped.Body.String())
			}
		})
	}
}

// A template import's validate-only path does not touch the bank, so the
// reference leaves it open to a scoped caller.
func TestTransferTemplateDryRunIsNotRefused(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "b1")
	body := bytes.NewBufferString(`{"version":"1","bank":{"reflect_mission":"help"}}`)
	rec := scopedTransferServe(t, "dan", http.MethodPost, "/v1/default/banks/b1/import?dry_run=true", body, "application/json", h.ImportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run import = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

package memoryops

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cmp-0003: the file route rejects a per-file field placed at the request level.
func TestFileRouteRejectsRequestLevelFields(t *testing.T) {
	mux, _ := newServer(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "a.txt")
	_, _ = part.Write([]byte("body"))
	// Per-file fields belong inside the `request` JSON's files_metadata, so a
	// root-level one there is the route's 400 JSON refusal.
	_ = w.WriteField("request", `{"document_id":"should-be-nested"}`)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "'files_metadata' entry") {
		t.Fatalf("the refusal must name where the fields belong: %s", rec.Body.String())
	}
}

// ts-0017: the file route requires at least one uploaded file.
func TestFileRouteRequiresFiles(t *testing.T) {
	mux, _ := newServer(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("request", "{}")
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestFileRouteRejectsNonMultipart(t *testing.T) {
	mux, _ := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// A body that cannot carry the route's required parts is the framework's
	// structured validation error, not a 400 sentence.
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0010 / err-0004: a duplicate document identifier within one async strategy
// group is the reference's plain-detail 400 sentence, not a structured 422.
func TestDuplicateDocumentIDRejected(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","document_id":"d1"},{"content":"b","document_id":"d1"}],"async":true}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := "Batch contains duplicate document_ids: ['d1']. Each content item in an async batch must have a unique " +
		"document_id to avoid races between the parallel workers that process them. To fold several items into one " +
		"document, send the batch synchronously (async=false), which processes them sequentially."
	if resp["detail"] != want {
		t.Fatalf("detail = %v", resp["detail"])
	}
}

// The synchronous path folds several items for one document into it
// sequentially, so a client sending a conversation's turns together is valid.
func TestDuplicateDocumentIDAllowedSynchronously(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","document_id":"d1"},{"content":"b","document_id":"d1"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// A document identifier repeated across two strategies is not a duplicate: the
// reference checks each strategy group's own items.
func TestDuplicateDocumentIDAcrossStrategiesAllowed(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","document_id":"d1","strategy":"one"},{"content":"b","document_id":"d1","strategy":"two"}],"async":true}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// err-0008 / beh-0003: combining an operation identifier with items that resolve
// to more than one strategy is the reference's plain-detail 400 sentence.
func TestMultiStrategyWithOperationIDRejected(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","strategy":"one"},{"content":"b","strategy":"two"}],"async":true,"operation_id":"11111111-1111-1111-1111-111111111111"}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["detail"] != "operation_id requires all retain items to resolve to a single strategy" {
		t.Fatalf("detail = %v", resp["detail"])
	}
}

// A synchronous write ignores a supplied operation identifier even with several
// strategies: the reference only enforces the single-strategy rule on the async
// path.
func TestMultiStrategyWithOperationIDAllowedSynchronously(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","strategy":"one"},{"content":"b","strategy":"two"}],"operation_id":"11111111-1111-1111-1111-111111111111"}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// err-0006: a malformed operation identifier on the write is a client error.
func TestMalformedOperationIDOnWriteRejected(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a"}],"async":true,"operation_id":"nope"}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// The reference validates the write's operation_id with `uuid.UUID(value)`, so
// the lax spellings are accepted and stored in their canonical form.
func TestOperationIDOnWriteAcceptsLaxSpellings(t *testing.T) {
	const canonical = "11111111-1111-1111-1111-111111111111"
	cases := map[string]string{
		"32 hex digits": "11111111111111111111111111111111",
		"braced":        "{" + canonical + "}",
		"urn":           "urn:uuid:" + canonical,
		"uppercase":     "11111111-1111-1111-1111-111111111111",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := canonicalOperationID(raw)
			if !ok {
				t.Fatalf("%q rejected", raw)
			}
			if got != canonical {
				t.Fatalf("canonical = %q, want %q", got, canonical)
			}
		})
	}
	if _, ok := canonicalOperationID("nope"); ok {
		t.Fatal("a non-uuid must be rejected")
	}
}

// ts-0003 / err-0006: a non-integer paging value on the list route is a client
// error.
func TestOperationListBadPagingRejected(t *testing.T) {
	mux, _ := newServer(t)
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations?limit=abc")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// err-0002: a malformed operation identifier on the cancel route is a 400 with
// the reference's plain detail message, matching the delete route.
func TestCancelMalformedIDRejected(t *testing.T) {
	mux, _ := newServer(t)
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/nope")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["detail"] != "Invalid operation_id format: nope" {
		t.Fatalf("detail = %v", body["detail"])
	}
}

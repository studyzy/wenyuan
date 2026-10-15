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
	_ = w.WriteField("document_id", "should-be-nested")
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0017: the file route requires at least one uploaded file.
func TestFileRouteRequiresFiles(t *testing.T) {
	mux, _ := newServer(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("unrelated", "x")
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0010 / err-0004: an omission-free list of valid items passes; a duplicate
// document identifier within one request is a client error.
func TestDuplicateDocumentIDRejected(t *testing.T) {
	mux, _ := newServer(t)
	// The queued path fans items out to parallel workers with no per-document
	// gate, so a duplicate document id there is a client error.
	body := `{"items":[{"content":"a","document_id":"d1"},{"content":"b","document_id":"d1"}],"async":true}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
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

// err-0008 / beh-0003: combining an operation identifier with items that resolve
// to more than one strategy is a client error.
func TestMultiStrategyWithOperationIDRejected(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","strategy":"one"},{"content":"b","strategy":"two"}],"async":true,"operation_id":"11111111-1111-1111-1111-111111111111"}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusUnprocessableEntity {
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

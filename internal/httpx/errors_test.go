package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorEnvelopeUsesDetailField(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, NewHTTPError(KindNotFound, DetailMissingBank))
	if rec.Code != KindNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body[FieldDetail] != DetailMissingBank {
		t.Fatalf("envelope detail = %v", body[FieldDetail])
	}
}

func TestWriteErrorCarriesRetryAfterHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	err := NewHTTPError(KindUnavailable, DetailWritesDown).WithHeader(HeaderRetryAfter, "5")
	WriteError(rec, err)
	if rec.Code != KindUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(HeaderRetryAfter); got != "5" {
		t.Fatalf("Retry-After = %q", got)
	}
}

func TestWriteValidationFailureShape(t *testing.T) {
	rec := httptest.NewRecorder()
	failure := &ValidationFailure{Errors: []ValidationError{{
		Loc:   []string{"body", "skepticism"},
		Msg:   "must be between 1 and 5",
		Type:  "value_error",
		Input: 9,
	}}}
	WriteValidationFailure(rec, failure)
	if rec.Code != KindUnprocessable {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	var body struct {
		Detail []ValidationError `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Detail) != 1 {
		t.Fatalf("expected one error item, got %d", len(body.Detail))
	}
	item := body.Detail[0]
	if item.Loc[1] != "skepticism" || item.Msg == "" || item.Type == "" || item.Input == nil {
		t.Fatalf("error-item missing fields: %+v", item)
	}
}

// Explicit null object fields stay on the wire: the reference's route class
// never applies exclude_none, so nothing drops them.
func TestNullFieldsAreKept(t *testing.T) {
	payload := map[string]any{
		"document_id": "doc-1",
		"metadata":    nil,
		"tags":        []string{"a", "b"},
	}
	kept, err := encodeJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), `"metadata":null`) {
		t.Fatalf("null field must be present: %s", kept)
	}
	// Map key order is not stable; compare decoded maps instead.
	var decodedKept map[string]any
	if err := json.Unmarshal(kept, &decodedKept); err != nil {
		t.Fatal(err)
	}
	if _, present := decodedKept["metadata"]; !present {
		t.Fatalf("metadata must be present: %s", kept)
	}
	if _, present := decodedKept["document_id"]; !present {
		t.Fatalf("non-null field must survive: %s", kept)
	}
}

func TestWriteJSONKeepsNullsInHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"document_id": "d", "metadata": nil})
	})
	handler := Chain(mux, DisconnectCancelMiddleware())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thing", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["metadata"]; !present {
		t.Fatalf("metadata must stay on the wire: %s", rec.Body.String())
	}
}

// A 503 that names no delay of its own still advertises one: the reference
// never serves the status without the header (api/http.py:367-371, :5801-5806),
// and the storage-error mappers that build most of this service's 503s have no
// configured value to reach for. The default is the reference's
// `StoreWriteUnavailable.retry_after`.
func TestWriteErrorGivesEvery503ARetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, NewHTTPError(KindUnavailable, DetailWritesDown))
	if rec.Code != KindUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(HeaderRetryAfter); got != "30" {
		t.Fatalf("Retry-After = %q, want the reference's default 30", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, the header must not disturb the envelope", got)
	}
}

// A site that chose its own delay keeps it: the default only fills a gap.
func TestWriteErrorKeepsAnExplicitRetryAfter(t *testing.T) {
	for _, value := range []string{"5", "0", "120"} {
		rec := httptest.NewRecorder()
		err := NewHTTPError(KindUnavailable, DetailWritesDown).WithHeader(HeaderRetryAfter, value)
		WriteError(rec, err)
		if got := rec.Header().Get(HeaderRetryAfter); got != value {
			t.Fatalf("Retry-After = %q, want the site's own %q", got, value)
		}
	}
}

// The pairing is a property of the 503, not of the 503-capable routes: no other
// status gains the header, so a client cannot be told to retry a 400 or a 500.
func TestWriteErrorDoesNotAddRetryAfterToOtherStatuses(t *testing.T) {
	for _, status := range []int{KindBadRequest, KindForbidden, KindNotFound, KindConflict,
		KindUnprocessable, KindInternal, KindTimeout, KindClientClosed} {
		rec := httptest.NewRecorder()
		WriteError(rec, NewHTTPError(status, "detail"))
		if got := rec.Header().Get(HeaderRetryAfter); got != "" {
			t.Fatalf("status %d carried Retry-After %q", status, got)
		}
	}
}

// WriteErrorFor is the path every handler actually takes; the naming wrapper it
// applies must not drop the header.
func TestWriteErrorForGives503ARetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/documents", nil)
	WriteErrorFor(rec, req, NewHTTPError(KindUnavailable, DetailWritesDown))
	if rec.Code != KindUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(HeaderRetryAfter); got != "30" {
		t.Fatalf("Retry-After = %q", got)
	}
}

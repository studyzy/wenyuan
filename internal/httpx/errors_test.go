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
	WriteError(rec, NewHTTPError(KindNotFound, DetailMissingBank), EncodeOptions{})
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
	WriteError(rec, err, EncodeOptions{})
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
	WriteValidationFailure(rec, failure, EncodeOptions{})
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

// ts-0020: with dropping enabled the null object field is absent; with it
// disabled the field is present with a null value.
func TestNullFieldDroppingModes(t *testing.T) {
	payload := map[string]any{
		"document_id": "doc-1",
		"metadata":    nil,
		"tags":        []string{"a", "b"},
	}
	kept, err := encodeJSON(payload, EncodeOptions{DropNullFields: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), `"metadata":null`) {
		t.Fatalf("without dropping the null field must be present: %s", kept)
	}
	// Map key order is not stable; compare decoded maps instead.
	var decodedKept map[string]any
	if err := json.Unmarshal(kept, &decodedKept); err != nil {
		t.Fatal(err)
	}
	if _, present := decodedKept["metadata"]; !present {
		t.Fatalf("metadata must be present when dropping is disabled: %s", kept)
	}

	dropped, err := encodeJSON(payload, EncodeOptions{DropNullFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var decodedDropped map[string]any
	if err := json.Unmarshal(dropped, &decodedDropped); err != nil {
		t.Fatal(err)
	}
	if _, present := decodedDropped["metadata"]; present {
		t.Fatalf("metadata must be absent when dropping is enabled: %s", dropped)
	}
	if _, present := decodedDropped["document_id"]; !present {
		t.Fatalf("non-null field must survive dropping: %s", dropped)
	}
}

func TestWriteJSONUsesRequestOptionsInHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"document_id": "d", "metadata": nil})
	})
	handler := Chain(mux, NullDropMiddleware(true))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thing", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["metadata"]; present {
		t.Fatalf("metadata should have been dropped: %s", rec.Body.String())
	}
}

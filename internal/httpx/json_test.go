package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONEncodesStatusAndContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]string{"id": "b1"}, EncodeOptions{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "b1" {
		t.Fatalf("body = %v", body)
	}
}

func TestDropNullPreservesArrayElements(t *testing.T) {
	payload := map[string]any{
		"scores": []any{1, nil, 3},
		"note":   nil,
	}
	encoded, err := encodeJSON(payload, EncodeOptions{DropNullFields: true})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["note"]; present {
		t.Fatalf("object null must be dropped: %s", encoded)
	}
	scores, ok := body["scores"].([]any)
	if !ok || len(scores) != 3 {
		t.Fatalf("array elements must be preserved: %s", encoded)
	}
}

func TestWriteErrorUnwrapsUnknownErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, nil, EncodeOptions{})
	if rec.Code != KindInternal {
		t.Fatalf("nil error must produce internal status, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body[FieldDetail] != DetailInternal {
		t.Fatalf("generic detail expected, got %q", body[FieldDetail])
	}
}

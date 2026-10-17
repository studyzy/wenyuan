package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONEncodesStatusAndContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]string{"id": "b1"})
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

func TestWriteErrorUnwrapsUnknownErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, nil)
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

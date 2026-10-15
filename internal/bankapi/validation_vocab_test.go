package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// vocabItem is one structured 422 item in the framework's own shape: the five
// keys it emits, with the framework's documentation link deliberately absent.
type vocabItem struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx"`
}

func decodeVocab(t *testing.T, rec *httptest.ResponseRecorder) []vocabItem {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var rawBody struct {
		Detail []map[string]json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawBody); err != nil {
		t.Fatalf("decode detail: %v (body %s)", err, rec.Body.String())
	}
	for _, item := range rawBody.Detail {
		if _, ok := item["url"]; ok {
			t.Fatalf("an item must not carry a url key: %s", rec.Body.String())
		}
	}
	var body struct {
		Detail []vocabItem `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode detail: %v (body %s)", err, rec.Body.String())
	}
	if len(body.Detail) == 0 {
		t.Fatalf("empty detail: %s", rec.Body.String())
	}
	return body.Detail
}

// A trait above its inclusive upper bound reports the framework's
// `less_than_equal` item carrying the bound it violated.
func TestBankTraitAboveMaximumReportsLessThanEqual(t *testing.T) {
	mux, _ := newTestServer(t)
	item := decodeVocab(t, doJSON(t, mux, http.MethodPut, "/v1/default/banks/bt",
		`{"disposition_skepticism":9}`))[0]
	if item.Type != "less_than_equal" {
		t.Fatalf("type = %q, want less_than_equal", item.Type)
	}
	if item.Msg != "Input should be less than or equal to 5" {
		t.Fatalf("msg = %q", item.Msg)
	}
	if le, ok := item.Ctx["le"].(float64); !ok || le != 5 {
		t.Fatalf("ctx = %#v, want le 5", item.Ctx)
	}
	if len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "disposition_skepticism" {
		t.Fatalf("loc = %v", item.Loc)
	}
}

// A trait below its inclusive lower bound reports `greater_than_equal`.
func TestBankTraitBelowMinimumReportsGreaterThanEqual(t *testing.T) {
	mux, _ := newTestServer(t)
	item := decodeVocab(t, doJSON(t, mux, http.MethodPut, "/v1/default/banks/bt",
		`{"disposition_empathy":0}`))[0]
	if item.Type != "greater_than_equal" {
		t.Fatalf("type = %q, want greater_than_equal", item.Type)
	}
	if item.Msg != "Input should be greater than or equal to 1" {
		t.Fatalf("msg = %q", item.Msg)
	}
	if ge, ok := item.Ctx["ge"].(float64); !ok || ge != 1 {
		t.Fatalf("ctx = %#v, want ge 1", item.Ctx)
	}
}

// A config write that omits the required updates object reports the framework's
// `missing` item with the body it was missing from as its input.
func TestBankConfigMissingUpdatesReportsMissingItem(t *testing.T) {
	mux, _ := configServer(t)
	item := decodeVocab(t, doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{}`))[0]
	if item.Type != "missing" {
		t.Fatalf("type = %q, want missing", item.Type)
	}
	if len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "updates" {
		t.Fatalf("loc = %v, want [body updates]", item.Loc)
	}
	if input, ok := item.Input.(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("input = %#v, want an empty object", item.Input)
	}
}

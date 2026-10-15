package memoryops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// vocabItem is one structured 422 item with the keys the reference's framework
// emits: a type, the field path (list positions decoded as numbers), the
// message, the offending input and the type-specific context. The framework's
// documentation link is deliberately absent: it builds its items with
// include_url=False.
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

// A body that omits the required items list reports the framework's `missing`
// item, whose input is the object the field was missing from.
func TestRetainMissingItemsIsMissingItem(t *testing.T) {
	mux, _ := newServer(t)
	item := decodeVocab(t, post(t, mux, "/v1/default/banks/b1/memories", `{}`))[0]
	if item.Type != "missing" {
		t.Fatalf("type = %q, want missing", item.Type)
	}
	if len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "items" {
		t.Fatalf("loc = %v, want [body items]", item.Loc)
	}
	if item.Msg != "Field required" {
		t.Fatalf("msg = %q, want Field required", item.Msg)
	}
	input, ok := item.Input.(map[string]any)
	if !ok || len(input) != 0 {
		t.Fatalf("input = %#v, want an empty object", item.Input)
	}
}

// An out-of-range integer names the violated inclusive bound in the item's
// context, with the framework's own wording.
func TestRecallOperationLimitOutOfRangeReportsBound(t *testing.T) {
	mux, _ := newServer(t)
	rec := get(t, mux, "/v1/default/banks/b1/operations?limit=5000")
	item := decodeVocab(t, rec)[0]
	if item.Type != "less_than_equal" {
		t.Fatalf("type = %q, want less_than_equal", item.Type)
	}
	if item.Msg != "Input should be less than or equal to 1000" {
		t.Fatalf("msg = %q", item.Msg)
	}
	if le, ok := item.Ctx["le"].(float64); !ok || le != 1000 {
		t.Fatalf("ctx = %#v, want ge bound 1000", item.Ctx)
	}
}

// A value outside a named enum reports the `enum` item carrying the expected
// members.
func TestRecallBadBudgetReportsEnumItem(t *testing.T) {
	mux, _ := newServer(t)
	item := decodeVocab(t, post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"hello","budget":"bogus"}`))[0]
	if item.Type != "enum" {
		t.Fatalf("type = %q, want enum", item.Type)
	}
	if item.Msg != "Input should be 'low', 'mid' or 'high'" {
		t.Fatalf("msg = %q", item.Msg)
	}
	if expected, _ := item.Ctx["expected"].(string); expected != "'low', 'mid' or 'high'" {
		t.Fatalf("ctx.expected = %v", item.Ctx["expected"])
	}
	if item.Input != "bogus" {
		t.Fatalf("input = %#v", item.Input)
	}
}

// A wrongly typed scalar names the framework's scalar-parse type at the field
// path.
func TestRecallBadScalarTypeReportsIntParsing(t *testing.T) {
	mux, _ := newServer(t)
	item := decodeVocab(t, post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"hello","max_tokens":"nope"}`))[0]
	if item.Type != "int_parsing" {
		t.Fatalf("type = %q, want int_parsing", item.Type)
	}
	if len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "max_tokens" {
		t.Fatalf("loc = %v, want [body max_tokens]", item.Loc)
	}
	if item.Msg != "Input should be a valid integer, unable to parse string as an integer" {
		t.Fatalf("msg = %q", item.Msg)
	}
}

// A string field given a non-string names `string_type` at the field path.
func TestRecallBadStringTypeReportsStringType(t *testing.T) {
	mux, _ := newServer(t)
	item := decodeVocab(t, post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":5}`))[0]
	if item.Type != "string_type" {
		t.Fatalf("type = %q, want string_type", item.Type)
	}
	if item.Msg != "Input should be a valid string" {
		t.Fatalf("msg = %q", item.Msg)
	}
	if len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "query" {
		t.Fatalf("loc = %v, want [body query]", item.Loc)
	}
}

// A field-level custom validator reports a `value_error` at the field's own
// path, with the position in the item list as a number.
func TestRetainBadTimestampReportsFieldLevelValueError(t *testing.T) {
	mux, _ := newServer(t)
	item := decodeVocab(t, post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","timestamp":"not-a-time"}]}`))[0]
	if item.Type != "value_error" {
		t.Fatalf("type = %q, want value_error", item.Type)
	}
	want := []any{"body", "items", float64(0), "timestamp"}
	if len(item.Loc) != len(want) {
		t.Fatalf("loc = %v, want %v", item.Loc, want)
	}
	for i := range want {
		if item.Loc[i] != want[i] {
			t.Fatalf("loc = %v, want %v", item.Loc, want)
		}
	}
	if !strings.HasPrefix(item.Msg, "Value error, Invalid timestamp/event_date format:") {
		t.Fatalf("msg = %q", item.Msg)
	}
	if item.Input != "not-a-time" {
		t.Fatalf("input = %#v", item.Input)
	}
}

// The observation-scope form validator reports at its own path too.
func TestRetainBadObservationScopesReportsFieldLevelValueError(t *testing.T) {
	mux, _ := newServer(t)
	item := decodeVocab(t, post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","observation_scopes":5}]}`))[0]
	if item.Type != "value_error" {
		t.Fatalf("type = %q, want value_error", item.Type)
	}
	want := []any{"body", "items", float64(0), "observation_scopes"}
	if len(item.Loc) != len(want) {
		t.Fatalf("loc = %v, want %v", item.Loc, want)
	}
	for i := range want {
		if item.Loc[i] != want[i] {
			t.Fatalf("loc = %v, want %v", item.Loc, want)
		}
	}
}

// get issues a GET against the served route table.
func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

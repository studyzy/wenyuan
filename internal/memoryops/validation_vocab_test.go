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
	if item.Msg != "Input should be less than or equal to 100" {
		t.Fatalf("msg = %q", item.Msg)
	}
	if le, ok := item.Ctx["le"].(float64); !ok || le != 100 {
		t.Fatalf("ctx = %#v, want le bound 100", item.Ctx)
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

// The observation-scope field is a union of the named modes and a list of tag
// groups, and the reference reports both branches' failures — the literal set
// first, then the list, whose own elements are named per position. These are the
// bodies the live reference serves for the same requests.
func TestRetainBadObservationScopesReportsBothUnionBranches(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		value string
		want  string
	}{
		{
			`"bogus"`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":"bogus","ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]"],` +
				`"msg":"Input should be a valid list","input":"bogus"}]}`,
		},
		{
			`5`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":5,"ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]"],` +
				`"msg":"Input should be a valid list","input":5}]}`,
		},
		{
			`true`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":true,"ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]"],` +
				`"msg":"Input should be a valid list","input":true}]}`,
		},
		{
			`{}`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":{},"ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]"],` +
				`"msg":"Input should be a valid list","input":{}}]}`,
		},
		{
			`["a","b"]`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":["a","b"],"ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]",0],` +
				`"msg":"Input should be a valid list","input":"a"},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]",1],` +
				`"msg":"Input should be a valid list","input":"b"}]}`,
		},
		{
			`[["a"],5]`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":[["a"],5],"ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]",1],` +
				`"msg":"Input should be a valid list","input":5}]}`,
		},
		{
			`[["a",5]]`,
			`{"detail":[` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":[["a",5]],"ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"string_type","loc":["body","items",0,"observation_scopes","list[list[str]]",0,1],` +
				`"msg":"Input should be a valid string","input":5}]}`,
		},
	}
	for _, tc := range cases {
		body := `{"items":[{"content":"x","observation_scopes":` + tc.value + `}]}`
		rec := post(t, mux, "/v1/default/banks/b1/memories", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("scopes=%s: status = %d, body = %s", tc.value, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
			t.Errorf("scopes=%s:\n got %s\nwant %s", tc.value, got, tc.want)
		}
	}
}

// Both accepted observation-scope forms still pass validation.
func TestRetainAcceptsBothObservationScopeForms(t *testing.T) {
	mux, _ := newServer(t)
	for _, value := range []string{`"per_tag"`, `[["a"]]`, `[]`, `[[]]`} {
		body := `{"items":[{"content":"hello world","observation_scopes":` + value + `}]}`
		if rec := post(t, mux, "/v1/default/banks/b1/memories", body); rec.Code != http.StatusOK {
			t.Fatalf("scopes=%s: status = %d, body = %s", value, rec.Code, rec.Body.String())
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

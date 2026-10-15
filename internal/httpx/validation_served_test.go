package httpx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// validationVocabArea registers the two handler shapes the validation
// vocabulary reaches the wire through: one that decodes a JSON body through the
// shared decoder, and one that reports semantic failures (a bound, an enum, a
// missing field, a custom validator) the way a handler does.
type validationVocabArea struct{}

// vocabBody is the target the decoder fills. Every field is required by the
// vocabulary test's own validation, not by the decoder.
type vocabBody struct {
	Name  string         `json:"name"`
	Count int            `json:"count"`
	Flag  bool           `json:"flag"`
	Ratio float64        `json:"ratio"`
	List  []string       `json:"list"`
	Dict  map[string]any `json:"dict"`
}

func (validationVocabArea) Mount(r RouteRegistrar) {
	r.Handle(http.MethodPost, "/decode", decodeVocabBody)
	r.Handle(http.MethodPost, "/validate", validateVocabBody)
	r.Handle(http.MethodPost, "/bound", func(w http.ResponseWriter, req *http.Request) {
		WriteValidationFor(w, req, &ValidationFailure{Errors: []ValidationError{
			GreaterThanEqual([]string{"body", "limit"}, 0, 1),
			LessThanEqual([]string{"body", "limit"}, 5000, 1000),
		}})
	})
	r.Handle(http.MethodPost, "/enum", func(w http.ResponseWriter, req *http.Request) {
		WriteValidationFor(w, req, &ValidationFailure{Errors: []ValidationError{
			Enum([]string{"body", "budget"}, "bogus", []string{"low", "mid", "high"}),
			Literal([]string{"body", "tags_match"}, "bogus", []string{"any", "all"}),
			ValueError([]string{"body", "query"}, "   ", "query must contain at least one word character"),
		}})
	})
}

// decodeVocabBody reports whatever the shared decoder rejects, so the served
// item is the one a real handler would produce.
func decodeVocabBody(w http.ResponseWriter, req *http.Request) {
	var target vocabBody
	if err := DecodeBody(req, &target); err != nil {
		WriteErrorFor(w, req, err)
		return
	}
	WriteJSONFor(w, req, http.StatusOK, map[string]any{"ok": true})
}

// validateVocabBody reports the failures a handler derives after decoding: a
// required field the framework reports as missing, and a rejected custom value.
func validateVocabBody(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		WriteErrorFor(w, req, err)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		WriteValidationFor(w, req, &ValidationFailure{Errors: []ValidationError{
			Missing([]string{"body"}, nil),
		}})
		return
	}
	var present map[string]any
	if err := json.Unmarshal(body, &present); err != nil {
		WriteErrorFor(w, req, BodyDecodeFailure(err, body))
		return
	}
	if _, ok := present["name"]; !ok {
		WriteValidationFor(w, req, &ValidationFailure{Errors: []ValidationError{
			Missing([]string{"body", "name"}, present),
		}})
		return
	}
	WriteJSONFor(w, req, http.StatusOK, map[string]any{"ok": true})
}

// vocabServed builds the served router the vocabulary table is asserted
// against: the real middleware chain and the real JSON writer, so the bytes a
// client reads are the bytes under test rather than a marshalled constructor.
func vocabServed(t *testing.T) http.Handler {
	t.Helper()
	router := NewRouter(RouterOptions{})
	router.RegisterArea(validationVocabArea{}.Mount)
	return router.Service()
}

// vocabCase pins one served item against the reference's strings: the type, the
// field path, the message, the input, the type-specific context, and the absence
// of the framework's documentation link (the reference builds its items with
// include_url=False, so no item carries a url key).
type vocabCase struct {
	name    string
	route   string
	body    string
	index   int
	want    map[string]any
	hasCtx  bool
	ctxWant map[string]any
}

func TestServedValidationVocabulary(t *testing.T) {
	cases := []vocabCase{
		{
			name:  "empty body is a missing required body",
			route: "/validate",
			body:  "",
			want: map[string]any{
				"type": "missing", "loc": []any{"body"}, "msg": "Field required", "input": nil,
			},
		},
		{
			name:  "absent required field is missing with the body as input",
			route: "/validate",
			body:  `{"count":1}`,
			want: map[string]any{
				"type": "missing", "loc": []any{"body", "name"}, "msg": "Field required",
				"input": map[string]any{"count": float64(1)},
			},
		},
		{
			name:  "truncated json is json_invalid at a positional loc",
			route: "/decode",
			body:  `{"name": `,
			want: map[string]any{
				"type": "json_invalid", "loc": []any{"body", float64(9)}, "msg": "JSON decode error",
				"input": map[string]any{},
			},
			hasCtx: true,
		},
		{
			name:  "string for an integer is int_parsing",
			route: "/decode",
			body:  `{"count":"many"}`,
			want: map[string]any{
				"type": "int_parsing", "loc": []any{"body", "count"},
				"msg": "Input should be a valid integer, unable to parse string as an integer", "input": "many",
			},
		},
		{
			name:  "object for an integer is int_type",
			route: "/decode",
			body:  `{"count":{}}`,
			want: map[string]any{
				"type": "int_type", "loc": []any{"body", "count"},
				"msg": "Input should be a valid integer", "input": map[string]any{},
			},
		},
		{
			name:  "fraction for an integer is int_from_float",
			route: "/decode",
			body:  `{"count":1.5}`,
			want: map[string]any{
				"type": "int_from_float", "loc": []any{"body", "count"},
				"msg": "Input should be a valid integer, got a number with a fractional part", "input": 1.5,
			},
		},
		{
			name:  "string for a boolean is bool_parsing",
			route: "/decode",
			body:  `{"flag":"maybe"}`,
			want: map[string]any{
				"type": "bool_parsing", "loc": []any{"body", "flag"},
				"msg": "Input should be a valid boolean, unable to interpret input", "input": "maybe",
			},
		},
		{
			name:  "array for a boolean is bool_type",
			route: "/decode",
			body:  `{"flag":[]}`,
			want: map[string]any{
				"type": "bool_type", "loc": []any{"body", "flag"},
				"msg": "Input should be a valid boolean", "input": []any{},
			},
		},
		{
			name:  "number for a string is string_type",
			route: "/decode",
			body:  `{"name":7}`,
			want: map[string]any{
				"type": "string_type", "loc": []any{"body", "name"},
				"msg": "Input should be a valid string", "input": float64(7),
			},
		},
		{
			name:  "string for a number is float_parsing",
			route: "/decode",
			body:  `{"ratio":"z"}`,
			want: map[string]any{
				"type": "float_parsing", "loc": []any{"body", "ratio"},
				"msg": "Input should be a valid number, unable to parse string as a number", "input": "z",
			},
		},
		{
			name:  "object for a number is float_type",
			route: "/decode",
			body:  `{"ratio":{}}`,
			want: map[string]any{
				"type": "float_type", "loc": []any{"body", "ratio"},
				"msg": "Input should be a valid number", "input": map[string]any{},
			},
		},
		{
			name:  "scalar for a list is list_type",
			route: "/decode",
			body:  `{"list":"x"}`,
			want: map[string]any{
				"type": "list_type", "loc": []any{"body", "list"},
				"msg": "Input should be a valid array", "input": "x",
			},
		},
		{
			name:  "scalar for a dictionary is dict_type",
			route: "/decode",
			body:  `{"dict":1}`,
			want: map[string]any{
				"type": "dict_type", "loc": []any{"body", "dict"},
				"msg": "Input should be an object", "input": float64(1),
			},
		},
		{
			name:  "a value below its inclusive bound is greater_than_equal",
			route: "/bound",
			body:  `{}`,
			want: map[string]any{
				"type": "greater_than_equal", "loc": []any{"body", "limit"},
				"msg": "Input should be greater than or equal to 1", "input": float64(0),
			},
			hasCtx:  true,
			ctxWant: map[string]any{"ge": float64(1)},
		},
		{
			name:  "a value above its inclusive bound is less_than_equal",
			route: "/bound",
			body:  `{}`,
			index: 1,
			want: map[string]any{
				"type": "less_than_equal", "loc": []any{"body", "limit"},
				"msg": "Input should be less than or equal to 1000", "input": float64(5000),
			},
			hasCtx:  true,
			ctxWant: map[string]any{"le": float64(1000)},
		},
		{
			name:  "a value outside an enum is enum with the members as context",
			route: "/enum",
			body:  `{}`,
			want: map[string]any{
				"type": "enum", "loc": []any{"body", "budget"},
				"msg": "Input should be 'low', 'mid' or 'high'", "input": "bogus",
			},
			hasCtx:  true,
			ctxWant: map[string]any{"expected": "'low', 'mid' or 'high'"},
		},
		{
			name:  "a value outside a literal is literal_error",
			route: "/enum",
			body:  `{}`,
			index: 1,
			want: map[string]any{
				"type": "literal_error", "loc": []any{"body", "tags_match"},
				"msg": "Input should be 'any' or 'all'", "input": "bogus",
			},
			hasCtx:  true,
			ctxWant: map[string]any{"expected": "'any' or 'all'"},
		},
		{
			name:  "a rejected custom value is value_error with the exception as context",
			route: "/enum",
			body:  `{}`,
			index: 2,
			want: map[string]any{
				"type": "value_error", "loc": []any{"body", "query"},
				"msg": "Value error, query must contain at least one word character", "input": "   ",
			},
			hasCtx:  true,
			ctxWant: map[string]any{"error": map[string]any{}},
		},
	}

	served := vocabServed(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.route, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			served.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
			}
			items := servedItems(t, rec)
			if tc.index >= len(items) {
				t.Fatalf("detail has %d items, want at least %d: %s", len(items), tc.index+1, rec.Body.String())
			}
			assertVocabItem(t, tc, items[tc.index])
		})
	}
}

// servedItem keeps an item's raw key set alongside its decoded values, so a
// case can assert that a key the reference does not emit is absent.
type servedItem struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx"`
	Keys  map[string]json.RawMessage
}

func servedItems(t *testing.T, rec *httptest.ResponseRecorder) []servedItem {
	t.Helper()
	var raw struct {
		Detail []json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode detail: %v (body %s)", err, rec.Body.String())
	}
	items := make([]servedItem, 0, len(raw.Detail))
	for _, one := range raw.Detail {
		var item servedItem
		if err := json.Unmarshal(one, &item); err != nil {
			t.Fatalf("decode item: %v (item %s)", err, one)
		}
		if err := json.Unmarshal(one, &item.Keys); err != nil {
			t.Fatalf("decode item keys: %v (item %s)", err, one)
		}
		items = append(items, item)
	}
	return items
}

func assertVocabItem(t *testing.T, tc vocabCase, item servedItem) {
	t.Helper()
	if item.Type != tc.want["type"] {
		t.Fatalf("type = %q, want %v", item.Type, tc.want["type"])
	}
	if item.Msg != tc.want["msg"] {
		t.Fatalf("msg = %q, want %v", item.Msg, tc.want["msg"])
	}
	wantLoc, _ := tc.want["loc"].([]any)
	if len(item.Loc) != len(wantLoc) {
		t.Fatalf("loc = %v, want %v", item.Loc, wantLoc)
	}
	for i := range wantLoc {
		if item.Loc[i] != wantLoc[i] {
			t.Fatalf("loc = %v, want %v", item.Loc, wantLoc)
		}
	}
	if got, want := jsonValue(item.Input), jsonValue(tc.want["input"]); got != want {
		t.Fatalf("input = %s, want %s", got, want)
	}
	if tc.hasCtx {
		if _, ok := item.Keys["ctx"]; !ok {
			t.Fatalf("item carries no ctx: %v", item.Keys)
		}
		for key, want := range tc.ctxWant {
			if got, w := jsonValue(item.Ctx[key]), jsonValue(want); got != w {
				t.Fatalf("ctx[%q] = %s, want %s", key, got, w)
			}
		}
	} else if _, ok := item.Keys["ctx"]; ok {
		t.Fatalf("item must not carry ctx: %v", item.Keys)
	}
	// The reference's validation handler builds its items with
	// include_url=False, so no item on the wire carries a link.
	if _, ok := item.Keys["url"]; ok {
		t.Fatalf("item must not carry a url key: %v", item.Keys)
	}
	for _, key := range []string{"type", "loc", "msg", "input"} {
		if _, ok := item.Keys[key]; !ok {
			t.Fatalf("item is missing key %q: %v", key, item.Keys)
		}
	}
}

// jsonValue renders a value the way the wire does, so two decoded values
// compare by their JSON form rather than by Go type.
func jsonValue(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

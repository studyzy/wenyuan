package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tables below are the reference's coercion rules, read off the framework's
// own model library rather than guessed: every accepted value and every
// rejection item was produced by validating the same input against a model with
// that field type under the library's default (lax) mode.

// laxArea serves a route whose body carries one field of each scalar type the
// coercion pass reaches, plus the nested shape the memory routes use.
type laxArea struct{}

type laxBody struct {
	Count   int            `json:"count"`
	Ratio   float64        `json:"ratio"`
	Flag    bool           `json:"flag"`
	Name    string         `json:"name"`
	Items   []int          `json:"items"`
	Include *laxInclude    `json:"include"`
	Custom  *laxCustom     `json:"custom"`
	Dict    map[string]int `json:"dict"`
}

type laxInclude struct {
	Entities *laxEntities `json:"entities"`
}

// laxCustom mirrors the request models that decode a sub-object themselves: the
// decode error such a type returns carries an offset relative to its own
// sub-slice, so the rewrite must not depend on the decoder's report.
type laxCustom struct {
	Entities *laxEntities `json:"entities"`
}

func (c *laxCustom) UnmarshalJSON(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil
	}
	if raw, ok := members["entities"]; ok {
		c.Entities = &laxEntities{}
		if err := json.Unmarshal(raw, c.Entities); err != nil {
			return LocalizeNestedDecodeError([]string{"custom"}, "entities", raw, err)
		}
	}
	return nil
}

type laxEntities struct {
	MaxTokens *int `json:"max_tokens"`
}

func (laxArea) Mount(r RouteRegistrar) {
	r.Handle(http.MethodPost, "/lax", func(w http.ResponseWriter, req *http.Request) {
		var target laxBody
		if err := DecodeBody(req, &target); err != nil {
			WriteErrorFor(w, req, err)
			return
		}
		WriteJSON(w, http.StatusOK, target)
	})
}

// laxServed builds the router the coercion table is asserted against.
func laxServed(t *testing.T) http.Handler {
	t.Helper()
	router := NewRouter(RouterOptions{})
	router.RegisterArea(laxArea{}.Mount)
	return router.Service()
}

func laxPost(t *testing.T, served http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/lax", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	served.ServeHTTP(rec, req)
	return rec
}

// TestLaxCoercionAccepted asserts every value the reference's lax mode accepts:
// the request is served, and the decoded field holds the coerced value.
func TestLaxCoercionAccepted(t *testing.T) {
	cases := []struct {
		name string
		body string
		want map[string]any
	}{
		{"numeric string for an integer", `{"count":"5"}`, map[string]any{"count": float64(5)}},
		{"padded and signed numeric string", `{"count":"  +5  "}`, map[string]any{"count": float64(5)}},
		{"zero-padded numeric string", `{"count":"010"}`, map[string]any{"count": float64(10)}},
		{"underscored numeric string", `{"count":"1_0"}`, map[string]any{"count": float64(10)}},
		{"negative numeric string", `{"count":"-7"}`, map[string]any{"count": float64(-7)}},
		{"integral string with a zero fraction", `{"count":"1.000"}`, map[string]any{"count": float64(1)}},
		{"true for an integer", `{"count":true}`, map[string]any{"count": float64(1)}},
		{"false for an integer", `{"count":false}`, map[string]any{"count": float64(0)}},
		{"integral number for an integer", `{"count":5.0}`, map[string]any{"count": float64(5)}},
		{"exponent number for an integer", `{"count":1e2}`, map[string]any{"count": float64(100)}},
		{"numeric string for a number", `{"ratio":"2.5"}`, map[string]any{"ratio": float64(2.5)}},
		{"exponent string for a number", `{"ratio":"1e2"}`, map[string]any{"ratio": float64(100)}},
		{"leading-dot string for a number", `{"ratio":".5"}`, map[string]any{"ratio": float64(0.5)}},
		{"underscored string for a number", `{"ratio":"1_0.5"}`, map[string]any{"ratio": float64(10.5)}},
		{"true for a number", `{"ratio":true}`, map[string]any{"ratio": float64(1)}},
		{"word for a boolean", `{"flag":"true"}`, map[string]any{"flag": true}},
		{"upper-case word for a boolean", `{"flag":"FALSE"}`, map[string]any{"flag": false}},
		{"yes for a boolean", `{"flag":"yes"}`, map[string]any{"flag": true}},
		{"n for a boolean", `{"flag":"n"}`, map[string]any{"flag": false}},
		{"on for a boolean", `{"flag":"on"}`, map[string]any{"flag": true}},
		{"off for a boolean", `{"flag":"off"}`, map[string]any{"flag": false}},
		{"digit string for a boolean", `{"flag":"1"}`, map[string]any{"flag": true}},
		{"one for a boolean", `{"flag":1}`, map[string]any{"flag": true}},
		{"zero for a boolean", `{"flag":0}`, map[string]any{"flag": false}},
		{"one and a fraction for a boolean", `{"flag":1.0}`, map[string]any{"flag": true}},
		{"string for a string", `{"name":"x"}`, map[string]any{"name": "x"}},
		{"coerced list elements", `{"items":["2",true,2.0]}`, map[string]any{"items": []any{float64(2), float64(1), float64(2)}}},
		{"coerced dictionary values", `{"dict":{"a":"3"}}`, map[string]any{"dict": map[string]any{"a": float64(3)}}},
		{
			"numeric string for a nested integer",
			`{"include":{"entities":{"max_tokens":"5"}}}`,
			map[string]any{"include.entities.max_tokens": float64(5)},
		},
		{
			"nested boolean for a nested integer",
			`{"include":{"entities":{"max_tokens":true}}}`,
			map[string]any{"include.entities.max_tokens": float64(1)},
		},
		{
			"numeric string below a self-decoding member",
			`{"custom":{"entities":{"max_tokens":"5"}}}`,
			map[string]any{"custom.entities.max_tokens": float64(5)},
		},
		{
			"integral number below a self-decoding member",
			`{"custom":{"entities":{"max_tokens":7.0}}}`,
			map[string]any{"custom.entities.max_tokens": float64(7)},
		},
		{
			"a coercible value in an undeclared member is ignored",
			`{"unknown":{"max_tokens":"abc"},"count":"5"}`,
			map[string]any{"count": float64(5)},
		},
	}

	served := laxServed(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := laxPost(t, served, tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			var decoded map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode echo: %v (body %s)", err, rec.Body.String())
			}
			for path, want := range tc.want {
				got, ok := pathValue(decoded, path)
				if !ok {
					t.Fatalf("%s absent from %s", path, rec.Body.String())
				}
				if string(got) != jsonValue(want) {
					t.Fatalf("%s = %s, want %s", path, got, jsonValue(want))
				}
			}
		})
	}
}

// pathValue reads a dotted path out of a decoded object.
func pathValue(object map[string]any, path string) (json.RawMessage, bool) {
	var current any = object
	for _, key := range strings.Split(path, ".") {
		step, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = step[key]
		if !ok {
			return nil, false
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// TestLaxCoercionRejected asserts the values the reference rejects are still
// rejected, with the item the decoder machinery builds for the original text.
// The expected items are the reference's own, so a rejection the reference
// reports under a different name is visible here rather than silently kept.
func TestLaxCoercionRejected(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		typ   string
		loc   []any
		msg   string
		input string
	}{
		{
			name: "unparsable string for an integer", body: `{"count":"abc"}`,
			typ: "int_parsing", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"abc"`,
		},
		{
			name: "fraction string for an integer", body: `{"count":"5.5"}`,
			typ: "int_parsing", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"5.5"`,
		},
		{
			name: "exponent string for an integer", body: `{"count":"1e2"}`,
			typ: "int_parsing", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"1e2"`,
		},
		{
			name: "hex string for an integer", body: `{"count":"0x5"}`,
			typ: "int_parsing", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"0x5"`,
		},
		{
			name: "misplaced underscore for an integer", body: `{"count":"1__0"}`,
			typ: "int_parsing", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"1__0"`,
		},
		{
			name: "fraction for an integer", body: `{"count":1.5}`,
			typ: "int_from_float", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, got a number with a fractional part", input: `1.5`,
		},
		{
			name: "array for an integer", body: `{"count":[]}`,
			typ: "int_type", loc: []any{"body", "count"},
			msg: "Input should be a valid integer", input: `[]`,
		},
		{
			name: "non-empty array for an integer", body: `{"count":[1]}`,
			typ: "int_type", loc: []any{"body", "count"},
			msg: "Input should be a valid integer", input: `[1]`,
		},
		{
			name: "object for an integer", body: `{"count":{}}`,
			typ: "int_type", loc: []any{"body", "count"},
			msg: "Input should be a valid integer", input: `{}`,
		},
		{
			name: "non-empty object for an integer", body: `{"count":{"a":1}}`,
			typ: "int_type", loc: []any{"body", "count"},
			msg: "Input should be a valid integer", input: `{"a":1}`,
		},
		{
			name: "unparsable string for a number", body: `{"ratio":"z"}`,
			typ: "float_parsing", loc: []any{"body", "ratio"},
			msg: "Input should be a valid number, unable to parse string as a number", input: `"z"`,
		},
		{
			name: "hexadecimal string for a number", body: `{"ratio":"0x1p3"}`,
			typ: "float_parsing", loc: []any{"body", "ratio"},
			msg: "Input should be a valid number, unable to parse string as a number", input: `"0x1p3"`,
		},
		{
			name: "array for a number", body: `{"ratio":[]}`,
			typ: "float_type", loc: []any{"body", "ratio"},
			msg: "Input should be a valid number", input: `[]`,
		},
		{
			name: "unparsable word for a boolean", body: `{"flag":"maybe"}`,
			typ: "bool_parsing", loc: []any{"body", "flag"},
			msg: "Input should be a valid boolean, unable to interpret input", input: `"maybe"`,
		},
		{
			name: "padded word for a boolean", body: `{"flag":" true"}`,
			typ: "bool_parsing", loc: []any{"body", "flag"},
			msg: "Input should be a valid boolean, unable to interpret input", input: `" true"`,
		},
		{
			name: "two for a boolean", body: `{"flag":2}`,
			typ: "bool_parsing", loc: []any{"body", "flag"},
			msg: "Input should be a valid boolean, unable to interpret input", input: `2`,
		},
		{
			name: "array for a boolean", body: `{"flag":[]}`,
			typ: "bool_type", loc: []any{"body", "flag"},
			msg: "Input should be a valid boolean", input: `[]`,
		},
		{
			name: "number for a string", body: `{"name":7}`,
			typ: "string_type", loc: []any{"body", "name"},
			msg: "Input should be a valid string", input: `7`,
		},
		{
			name: "scalar for a list", body: `{"items":"x"}`,
			typ: "list_type", loc: []any{"body", "items"},
			msg: "Input should be a valid list", input: `"x"`,
		},
		{
			name: "unparsable list element", body: `{"items":["a"]}`,
			typ: "int_parsing", loc: []any{"body", "items", float64(0)},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"a"`,
		},
		{
			name: "fractional list element", body: `{"items":[1,1.5]}`,
			typ: "int_from_float", loc: []any{"body", "items", float64(1)},
			msg: "Input should be a valid integer, got a number with a fractional part", input: `1.5`,
		},
		{
			name: "object for a dictionary", body: `{"dict":1}`,
			typ: "dict_type", loc: []any{"body", "dict"},
			msg: "Input should be a valid dictionary", input: `1`,
		},
		{
			name: "unparsable nested integer", body: `{"include":{"entities":{"max_tokens":"abc"}}}`,
			typ: "int_parsing", loc: []any{"body", "include", "entities", "max_tokens"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"abc"`,
		},
		{
			name: "unparsable nested integer below a self-decoding member",
			body: `{"custom":{"entities":{"max_tokens":"abc"}}}`,
			typ:  "int_parsing", loc: []any{"body", "custom", "entities", "max_tokens"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"abc"`,
		},
		{
			name: "fractional nested integer below a self-decoding member",
			body: `{"custom":{"entities":{"max_tokens":1.5}}}`,
			typ:  "int_from_float", loc: []any{"body", "custom", "entities", "max_tokens"},
			msg: "Input should be a valid integer, got a number with a fractional part", input: `1.5`,
		},
		{
			name: "list for a nested integer below a self-decoding member",
			body: `{"custom":{"entities":{"max_tokens":[1]}}}`,
			typ:  "int_type", loc: []any{"body", "custom", "entities", "max_tokens"},
			msg: "Input should be a valid integer", input: `[1]`,
		},
		{
			name: "integer string beside an undeclared object", body: `{"nope":{"deep":[{"count":"x"}]},"count":"abc"}`,
			typ: "int_parsing", loc: []any{"body", "count"},
			msg: "Input should be a valid integer, unable to parse string as an integer", input: `"abc"`,
		},
	}

	served := laxServed(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := laxPost(t, served, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
			}
			items := servedItems(t, rec)
			if len(items) != 1 {
				t.Fatalf("detail has %d items, want 1: %s", len(items), rec.Body.String())
			}
			item := items[0]
			if item.Type != tc.typ {
				t.Fatalf("type = %q, want %q", item.Type, tc.typ)
			}
			if item.Msg != tc.msg {
				t.Fatalf("msg = %q, want %q", item.Msg, tc.msg)
			}
			if len(item.Loc) != len(tc.loc) {
				t.Fatalf("loc = %v, want %v", item.Loc, tc.loc)
			}
			for i := range tc.loc {
				if item.Loc[i] != tc.loc[i] {
					t.Fatalf("loc = %v, want %v", item.Loc, tc.loc)
				}
			}
			if got := jsonValue(item.Input); got != jsonValue(json.RawMessage(tc.input)) {
				t.Fatalf("input = %s, want %s", got, tc.input)
			}
		})
	}
}

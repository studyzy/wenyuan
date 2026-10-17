package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// pythonSyntaxCases pins the failure text and character position CPython's json
// module reports for a malformed document. The reference's framework reports
// CPython's position and bare message for a body it rejects as `json_invalid`,
// so these rows were derived from CPython's scanner (and confirmed against the
// live reference) rather than from the Go decoder's byte offsets.
var pythonSyntaxCases = []struct {
	name string
	body string
	pos  int
	msg  string
}{
	{"open brace", "{", 1, "Expecting property name enclosed in double quotes"},
	{"missing value", `{"query":}`, 9, "Expecting value"},
	{"bare word", "not json", 0, "Expecting value"},
	{"double comma", `{"query":"a",,`, 13, "Expecting property name enclosed in double quotes"},
	{"trailing text", `{"query":"a"}trailing`, 13, "Extra data"},
	{"unterminated string", `"unterminated`, 0, "Unterminated string starting at"},
	{"bad escape", `{"query": "a\q"}`, 12, `Invalid \escape`},
	{"leading zero", `{"query": 01}`, 11, "Expecting ',' delimiter"},
	{"bare dot", `{"query": 1.}`, 11, "Expecting ',' delimiter"},
	{"bare minus", `{"query": -}`, 10, "Expecting value"},
	{"array trailing comma", `[1,]`, 3, "Expecting value"},
	{"array leading comma", `[,1]`, 1, "Expecting value"},
	{"unclosed nested array", `{"a":[1,}`, 8, "Expecting value"},
	{"missing nested value", `{"a":{"b":}}`, 10, "Expecting value"},
	{"lone bracket", "]", 0, "Expecting value"},
	{"literal typo", "tru", 0, "Expecting value"},
	{"nested literal typo", `{"query":tru}`, 9, "Expecting value"},
	{"missing colon", `{"query" "a"}`, 9, "Expecting ':' delimiter"},
	{"object trailing comma", `{"query":"a",}`, 13, "Expecting property name enclosed in double quotes"},
	{"single quotes", `{'query':'a'}`, 1, "Expecting property name enclosed in double quotes"},
	{"plus sign", `{"query":+1}`, 9, "Expecting value"},
	{"bare exponent", `{"query":1e}`, 10, "Expecting ',' delimiter"},
	{"string then junk", `"abc"x`, 5, "Extra data"},
	{"control character", "{\"query\":\"a\nb\"}", 11, "Invalid control character at"},
	{"hex escape", `{"query":"\x41"}`, 10, `Invalid \escape`},
	{"unquoted key", `{query:"a"}`, 1, "Expecting property name enclosed in double quotes"},
	{"comment", `{"query":"a"} // hi`, 14, "Extra data"},
	{"nul byte", "{\x00}", 1, "Expecting property name enclosed in double quotes"},
	{"short unicode escape", `{"query":"\u12"}`, 11, `Invalid \uXXXX escape`},
	{"bad unicode escape", `{"query":"\uZZZZ"}`, 11, `Invalid \uXXXX escape`},
	{"missing comma", `{"query": 1 2}`, 12, "Expecting ',' delimiter"},
	{"unterminated array", "[1", 2, "Expecting ',' delimiter"},
	{"whitespace only", "   ", 3, "Expecting value"},
	{"unicode before failure", `{"a":"é","b":}`, 13, "Expecting value"},
	{"astral before failure", `{"a":"😀","b":}`, 13, "Expecting value"},
}

// pythonJSONError reports CPython's bare message and character position for the
// malformed documents the reference rejects with the same item.
func TestPythonJSONErrorMatchesCPython(t *testing.T) {
	for _, tc := range pythonSyntaxCases {
		t.Run(tc.name, func(t *testing.T) {
			pos, msg, ok := pythonJSONError([]byte(tc.body))
			if !ok {
				t.Fatalf("pythonJSONError(%q) found no failure, want %d %q", tc.body, tc.pos, tc.msg)
			}
			if pos != tc.pos || msg != tc.msg {
				t.Fatalf("pythonJSONError(%q) = %d %q, want %d %q", tc.body, pos, msg, tc.pos, tc.msg)
			}
		})
	}
}

// A document CPython accepts (its NaN and Infinity literals) is not a syntax
// failure, so the caller keeps its own rendering instead of inventing one.
func TestPythonJSONErrorAcceptsExtensions(t *testing.T) {
	for _, body := range []string{`{"query":NaN}`, `{"query":Infinity}`, `{"query":-Infinity}`, `{}`, `{"query":"a"}`} {
		if _, _, ok := pythonJSONError([]byte(body)); ok {
			t.Errorf("pythonJSONError(%q) reported a failure for a document CPython accepts", body)
		}
	}
}

// A malformed body through the shared decoder carries CPython's position and
// message, as a JSON number for the position.
func TestDecodeBodyMalformedUsesPythonItem(t *testing.T) {
	type payload struct {
		Query string `json:"query"`
	}
	for _, tc := range []struct {
		body string
		loc  string
		ctx  string
	}{
		{"{", `"loc":["body",1]`, `"Expecting property name enclosed in double quotes"`},
		{"not json", `"loc":["body",0]`, `"Expecting value"`},
		{`{"query":"a"}trailing`, `"loc":["body",13]`, `"Extra data"`},
		{"   ", `"loc":["body",3]`, `"Expecting value"`},
	} {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
		var target payload
		err := DecodeBody(req, &target)
		if err == nil {
			t.Fatalf("body %q must be rejected", tc.body)
		}
		rec := httptest.NewRecorder()
		WriteErrorFor(rec, req, err)
		body := rec.Body.String()
		if rec.Code != 422 {
			t.Fatalf("body %q status = %d, want 422: %s", tc.body, rec.Code, body)
		}
		for _, want := range []string{`"type":"json_invalid"`, tc.loc, tc.ctx, `"input":{}`} {
			if !strings.Contains(body, want) {
				t.Fatalf("body %q => %s does not carry %s", tc.body, body, want)
			}
		}
	}
}

// A non-object body sent for a model is the framework's model_attributes_type
// item, whose input is the value itself, not the generic dict item.
func TestDecodeBodyNonObjectForModel(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	for _, tc := range []struct {
		body  string
		input string
	}{
		{`[1,2]`, `"input":[1,2]`},
		{`[]`, `"input":[]`},
		{`"hello"`, `"input":"hello"`},
		{`123`, `"input":123`},
		{`true`, `"input":true`},
	} {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
		var target payload
		err := DecodeBody(req, &target)
		if err == nil {
			t.Fatalf("body %q must be rejected", tc.body)
		}
		rec := httptest.NewRecorder()
		WriteErrorFor(rec, req, err)
		body := rec.Body.String()
		for _, want := range []string{
			`"type":"model_attributes_type"`, `"loc":["body"]`,
			`"msg":"Input should be a valid dictionary or object to extract fields from"`, tc.input,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("body %q => %s does not carry %s", tc.body, body, want)
			}
		}
		if strings.Contains(body, `"ctx"`) {
			t.Fatalf("body %q => %s carries a ctx the reference omits", tc.body, body)
		}
	}
}

// A scalar with trailing text is a parser failure, not a model item: the
// framework's parser rejects the whole document before its model sees a value,
// so the decoder's earlier model-typed failure must not win.
func TestDecodeBodyScalarWithTrailingTextIsJsonInvalid(t *testing.T) {
	type payload struct {
		Query string `json:"query"`
	}
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`"abc"x`))
	var target payload
	err := DecodeBody(req, &target)
	if err == nil {
		t.Fatal("body must be rejected")
	}
	rec := httptest.NewRecorder()
	WriteErrorFor(rec, req, err)
	body := rec.Body.String()
	for _, want := range []string{`"type":"json_invalid"`, `"loc":["body",5]`, `"Extra data"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body => %s does not carry %s", body, want)
		}
	}
}

// A non-object body sent for a map target keeps the generic dict item: only a
// model (a struct) answers model_attributes_type.
func TestDecodeBodyNonObjectForMapStaysDictType(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`[1,2]`))
	var target map[string]any
	err := DecodeBody(req, &target)
	if err == nil {
		t.Fatal("body must be rejected")
	}
	rec := httptest.NewRecorder()
	WriteErrorFor(rec, req, err)
	if body := rec.Body.String(); !strings.Contains(body, `"type":"dict_type"`) {
		t.Fatalf("body => %s, want dict_type", body)
	}
}

// TestDecodeBodyRequestLayerText pins the two body-text rules the reference's
// request layer applies before routing: a body that is not valid UTF-8 is
// refused with the layer's own bad-request detail (not a validation item), and
// a leading byte-order mark is dropped before the document is parsed.
func TestDecodeBodyRequestLayerText(t *testing.T) {
	type target struct {
		Query string `json:"query"`
	}

	notText := httptest.NewRequest("POST", "/x", strings.NewReader("{\"query\":\"\xff\"}"))
	recorder := httptest.NewRecorder()
	WriteError(recorder, DecodeBody(notText, &target{}))
	if recorder.Code != KindBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, KindBadRequest)
	}
	want := `{"detail":"` + detailBodyNotText + `"}`
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}

	withMark := httptest.NewRequest("POST", "/x", strings.NewReader("\xef\xbb\xbf{\"query\":\"bom\"}"))
	var decoded target
	if err := DecodeBody(withMark, &decoded); err != nil {
		t.Fatalf("byte-order mark rejected: %v", err)
	}
	if decoded.Query != "bom" {
		t.Fatalf("query = %q, want bom", decoded.Query)
	}
}

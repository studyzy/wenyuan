package memoryops

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TestReflectResponseSchemaValidation pins the response_schema contract to the
// reference's validate_response_schema rules: each rejected shape produces a
// 422 with the reference's exact detail, and a schema with a non-dict top level
// never reaches the validator (the body decoder rejects it).
func TestReflectResponseSchemaValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		msg  string
	}{
		{
			name: "non-object type",
			body: `{"query":"q","response_schema":{"type":"array"}}`,
			msg:  `Value error, response_schema must be an object schema (its "type" must be "object")`,
		},
		{
			name: "empty schema",
			body: `{"query":"q","response_schema":{}}`,
			msg:  `Value error, response_schema must define a non-empty 'properties' object`,
		},
		{
			name: "empty properties",
			body: `{"query":"q","response_schema":{"properties":{}}}`,
			msg:  `Value error, response_schema must define a non-empty 'properties' object`,
		},
		{
			name: "property not an object",
			body: `{"query":"q","response_schema":{"properties":{"a":[]}}}`,
			msg:  `Value error, response_schema property 'a' must be an object`,
		},
		{
			name: "unsupported property type",
			body: `{"query":"q","response_schema":{"properties":{"a":{"type":"bogus"}}}}`,
			msg:  `Value error, response_schema property 'a' has unsupported type 'bogus'; expected one of ['array', 'boolean', 'integer', 'number', 'object', 'string']`,
		},
		{
			name: "required not a list",
			body: `{"query":"q","response_schema":{"properties":{"a":{"type":"string"}},"required":"a"}}`,
			msg:  `Value error, response_schema 'required' must be a list of property names`,
		},
		{
			name: "required names unknown property",
			body: `{"query":"q","response_schema":{"properties":{"a":{"type":"string"}},"required":["b"]}}`,
			msg:  `Value error, response_schema 'required' references unknown properties: ['b']`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := bindHandlers(t, NewHandlers(storage.NewMemoryStore("public"), 5, 30000))
			rec := post(t, mux, "/v1/default/banks/b1/reflect", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
			}
			var decoded struct {
				Detail []struct {
					Type string `json:"type"`
					Loc  []any  `json:"loc"`
					Msg  string `json:"msg"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body.String())
			}
			if len(decoded.Detail) != 1 {
				t.Fatalf("detail = %v, want one item", decoded.Detail)
			}
			item := decoded.Detail[0]
			if item.Type != "value_error" {
				t.Fatalf("type = %q, want value_error", item.Type)
			}
			if item.Msg != tc.msg {
				t.Fatalf("msg = %q, want %q", item.Msg, tc.msg)
			}
			if len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "response_schema" {
				t.Fatalf("loc = %v, want [body response_schema]", item.Loc)
			}
		})
	}
}

// A well-formed schema is accepted (no 422), and a non-object top level is
// rejected by the body decoder rather than the schema validator.
func TestReflectResponseSchemaAcceptedAndNonDict(t *testing.T) {
	mux := bindHandlers(t, NewHandlers(storage.NewMemoryStore("public"), 5, 30000))
	rec := post(t, mux, "/v1/default/banks/b1/reflect",
		`{"query":"q","response_schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}`)
	if rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("valid schema rejected: %s", rec.Body.String())
	}
	rec = post(t, mux, "/v1/default/banks/b1/reflect", `{"query":"q","response_schema":[]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-object schema: status = %d, want 422", rec.Code)
	}
}

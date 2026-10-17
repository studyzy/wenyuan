package memoryops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The reference validates a request in one pass: an item a field's own
// validator reports appears beside the items the sibling fields produced, while
// a rule the model enforces on the request as a whole is not reached at all
// once any field has failed. Each expectation is the body the live reference
// serves for the same request.
//
// A body carrying a rejected `null` therefore still reports the field-level
// failures, in the model's field-declaration order, and never the body-level
// rule.
func TestDecodeFailureReportsFieldRulesOnly(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{
			"recall reports the query rule beside a rejected null",
			"/v1/default/banks/b1/memories/recall",
			`{"query":"","max_tokens":null}`,
			`{"detail":[` +
				`{"type":"value_error","loc":["body","query"],"msg":"Value error, query must contain at least one word character after normalization","input":"","ctx":{"error":{}}},` +
				`{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null}]}`,
		},
		{
			"recall suppresses the model-level rule when a field failed",
			"/v1/default/banks/b1/memories/recall",
			`{"query":"x","tags":["a"],"tag_groups":[{"tags":["a"]}],"max_tokens":null}`,
			`{"detail":[{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null}]}`,
		},
		{
			"reflect reports the field rules in declaration order",
			"/v1/default/banks/b1/reflect",
			`{"query":"x","max_tokens":null,"response_schema":{"type":"bogus"},"tags":"nope"}`,
			`{"detail":[` +
				`{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null},` +
				`{"type":"value_error","loc":["body","response_schema"],"msg":"Value error, response_schema must be an object schema (its \"type\" must be \"object\")","input":{"type":"bogus"},"ctx":{"error":{}}},` +
				`{"type":"list_type","loc":["body","tags"],"msg":"Input should be a valid list","input":"nope"}]}`,
		},
		{
			"a null beside a bad include member keeps both",
			"/v1/default/banks/b1/memories/recall",
			`{"query":"x","include":{"entities":{"max_tokens":"abc"}},"max_tokens":null}`,
			`{"detail":[` +
				`{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null},` +
				`{"type":"int_parsing","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"abc"}]}`,
		},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

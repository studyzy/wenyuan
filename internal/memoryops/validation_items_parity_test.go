package memoryops

import (
	"net/http"
	"testing"
)

// recallValidationCases are request bodies whose rejection the reference's
// framework reports as a structured item list. Each expected string is the
// exact body the reference answers with, so a change to the status or to any
// item's type, location, message or input fails here.
var recallValidationCases = []struct {
	name string
	body string
	want string
}{
	{
		"absent query is the framework's missing item",
		`{}`,
		`{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required","input":{}}]}`,
	},
	{
		"null query is not a string",
		`{"query":null}`,
		`{"detail":[{"type":"string_type","loc":["body","query"],"msg":"Input should be a valid string","input":null}]}`,
	},
	{
		"an include member that is not an object names that member",
		`{"query":"x","include":{"entities":true}}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include","entities"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"a null include is not an object",
		`{"query":"x","include":null}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null}]}`,
	},
	{
		"a scalar include carries its own value",
		`{"query":"x","include":"z"}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include"],"msg":"Input should be a valid dictionary or object to extract fields from","input":"z"}]}`,
	},
	{
		"a non-numeric string for a nested include integer keeps the full path",
		`{"query":"x","include":{"entities":{"max_tokens":"abc"}}}`,
		`{"detail":[{"type":"int_parsing","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"abc"}]}`,
	},
	{
		"a fraction for a nested include integer keeps the full path",
		`{"query":"x","include":{"entities":{"max_tokens":1.5}}}`,
		`{"detail":[{"type":"int_from_float","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer, got a number with a fractional part","input":1.5}]}`,
	},
	{
		"a list for a nested include integer keeps the full path",
		`{"query":"x","include":{"entities":{"max_tokens":[1]}}}`,
		`{"detail":[{"type":"int_type","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer","input":[1]}]}`,
	},
	{
		"a nested source-facts integer names its own member",
		`{"query":"x","include":{"source_facts":{"max_tokens_per_observation":"z"}}}`,
		`{"detail":[{"type":"int_parsing","loc":["body","include","source_facts","max_tokens_per_observation"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"z"}]}`,
	},
	{
		"every non-object include member is reported in field order",
		`{"query":"x","include":{"entities":true,"chunks":true,"source_facts":true}}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include","entities"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true},` +
			`{"type":"model_attributes_type","loc":["body","include","chunks"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true},` +
			`{"type":"model_attributes_type","loc":["body","include","source_facts"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"an include member that is not an object and a nested member failure are both reported in member order",
		`{"query":"x","include":{"entities":true,"chunks":{"max_tokens":"abc"}}}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include","entities"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true},` +
			`{"type":"int_parsing","loc":["body","include","chunks","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"abc"}]}`,
	},
	{
		"two nested include member failures are both reported in member order",
		`{"query":"x","include":{"entities":{"max_tokens":"abc"},"chunks":{"max_tokens":1.5}}}`,
		`{"detail":[{"type":"int_parsing","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"abc"},` +
			`{"type":"int_from_float","loc":["body","include","chunks","max_tokens"],"msg":"Input should be a valid integer, got a number with a fractional part","input":1.5}]}`,
	},
	{
		"three include member failures are all reported in member order",
		`{"query":"x","include":{"entities":{"max_tokens":"abc"},"chunks":{"max_tokens":1.5},"source_facts":{"max_tokens_per_observation":[]}}}`,
		`{"detail":[{"type":"int_parsing","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"abc"},` +
			`{"type":"int_from_float","loc":["body","include","chunks","max_tokens"],"msg":"Input should be a valid integer, got a number with a fractional part","input":1.5},` +
			`{"type":"int_type","loc":["body","include","source_facts","max_tokens_per_observation"],"msg":"Input should be a valid integer","input":[]}]}`,
	},
	{
		"a missing query and a bad include are both reported",
		`{"include":{"entities":true}}`,
		`{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required","input":{"include":{"entities":true}}},` +
			`{"type":"model_attributes_type","loc":["body","include","entities"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"the budget check precedes the include check in field order",
		`{"query":"q","budget":"nope","include":{"entities":true}}`,
		`{"detail":[{"type":"enum","loc":["body","budget"],"msg":"Input should be 'low', 'mid' or 'high'","input":"nope","ctx":{"expected":"'low', 'mid' or 'high'"}},` +
			`{"type":"model_attributes_type","loc":["body","include","entities"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"two bad integers in one include member are both reported in field order",
		`{"query":"x","include":{"source_facts":{"max_tokens":"a","max_tokens_per_observation":"b"}}}`,
		`{"detail":[{"type":"int_parsing","loc":["body","include","source_facts","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"a"},` +
			`{"type":"int_parsing","loc":["body","include","source_facts","max_tokens_per_observation"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"b"}]}`,
	},
	{
		"one member's two bad integers keep field order regardless of JSON order",
		`{"query":"x","include":{"source_facts":{"max_tokens_per_observation":"b","max_tokens":"a"}}}`,
		`{"detail":[{"type":"int_parsing","loc":["body","include","source_facts","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"a"},` +
			`{"type":"int_parsing","loc":["body","include","source_facts","max_tokens_per_observation"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"b"}]}`,
	},
	{
		"a bad integer and a bad list in one include member are both reported",
		`{"query":"x","include":{"source_facts":{"max_tokens":1.5,"max_tokens_per_observation":[]}}}`,
		`{"detail":[{"type":"int_from_float","loc":["body","include","source_facts","max_tokens"],"msg":"Input should be a valid integer, got a number with a fractional part","input":1.5},` +
			`{"type":"int_type","loc":["body","include","source_facts","max_tokens_per_observation"],"msg":"Input should be a valid integer","input":[]}]}`,
	},
}

// An include member's integer field keeps the reference's lax coercion: a
// numeric string is accepted and stored, so the request stays a 200 even though
// Go's decoder refuses a string for an int. This exercises the per-field decode
// that also reports every bad field of a member.
func TestIncludeMemberLaxCoercionAccepted(t *testing.T) {
	mux, _ := newServer(t)
	for _, body := range []string{
		`{"query":"x","include":{"source_facts":{"max_tokens":"5"}}}`,
		`{"query":"x","include":{"entities":{"max_tokens":"7"},"chunks":{"max_tokens":"9"}}}`,
		`{"query":"x","include":{"source_facts":{"max_tokens":"5","max_tokens_per_observation":"7"}}}`,
	} {
		rec := post(t, mux, "/v1/default/banks/b1/memories/recall", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("body %s: status = %d, body = %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestRecallValidationItems(t *testing.T) {
	for _, tc := range recallValidationCases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newServer(t)
			rec := post(t, mux, "/v1/default/banks/b1/memories/recall", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.want {
				t.Fatalf("body =\n %s\nwant\n %s", got, tc.want)
			}
		})
	}
}

// reflectValidationCases mirror the same contract for the reflection route,
// whose blank-query rule is the model's own and therefore names the whole body.
var reflectValidationCases = []struct {
	name string
	body string
	want string
}{
	{
		"absent query is the framework's missing item",
		`{}`,
		`{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required","input":{}}]}`,
	},
	{
		"a body carrying only context still misses the query",
		`{"context":"hi"}`,
		`{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required","input":{"context":"hi"}}]}`,
	},
	{
		"a blank query is the model's own error, naming the whole body",
		`{"query":""}`,
		`{"detail":[{"type":"value_error","loc":["body"],"msg":"Value error, 'query' must not be empty or whitespace-only.","input":{"query":""},"ctx":{"error":{}}}]}`,
	},
	{
		"null query is not a string",
		`{"query":null}`,
		`{"detail":[{"type":"string_type","loc":["body","query"],"msg":"Input should be a valid string","input":null}]}`,
	},
	{
		"a null include is not an object",
		`{"query":"x","include":null}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null}]}`,
	},
	{
		"a non-object include member names that member",
		`{"query":"x","include":{"facts":true}}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include","facts"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"a non-object tool-calls member names that member",
		`{"query":"x","include":{"tool_calls":true}}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include","tool_calls"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"a bad bool word for a nested tool-calls member keeps the full path",
		`{"query":"x","include":{"tool_calls":{"output":"maybe"}}}`,
		`{"detail":[{"type":"bool_parsing","loc":["body","include","tool_calls","output"],"msg":"Input should be a valid boolean, unable to interpret input","input":"maybe"}]}`,
	},
	{
		"a list for a nested tool-calls member keeps the full path",
		`{"query":"x","include":{"tool_calls":{"output":[1]}}}`,
		`{"detail":[{"type":"bool_type","loc":["body","include","tool_calls","output"],"msg":"Input should be a valid boolean","input":[1]}]}`,
	},
	{
		"a non-object facts member and a nested tool-calls failure are both reported in member order",
		`{"query":"x","include":{"facts":true,"tool_calls":{"output":"abc"}}}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include","facts"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true},` +
			`{"type":"bool_parsing","loc":["body","include","tool_calls","output"],"msg":"Input should be a valid boolean, unable to interpret input","input":"abc"}]}`,
	},
}

func TestReflectValidationItems(t *testing.T) {
	for _, tc := range reflectValidationCases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := reflectServer(t)
			rec := post(t, mux, "/v1/default/banks/b1/reflect", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.want {
				t.Fatalf("body =\n %s\nwant\n %s", got, tc.want)
			}
		})
	}
}

// The reflection include's member objects match the reference's models: the
// tool-calls member's boolean takes a lax word, and the facts member carries no
// fields at all, so a value it does not declare is ignored rather than rejected.
func TestReflectIncludeMembersKeepLaxAndIgnoreUnknown(t *testing.T) {
	mux, _ := reflectServer(t)
	for _, body := range []string{
		`{"query":"x","include":{"tool_calls":{"output":"yes"}}}`,
		`{"query":"x","include":{"facts":{"max_tokens":"a"}}}`,
		`{"query":"x","include":{"facts":{"anything":[1,2]}}}`,
	} {
		rec := post(t, mux, "/v1/default/banks/b1/reflect", body)
		if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
			// A reflect deployment without a model answers 400; the point here is
			// that the request was not rejected as unprocessable.
			t.Fatalf("reflect %s = %d, body = %s", body, rec.Code, rec.Body.String())
		}
	}
}

// A null member is the reference's "disabled" value, so it stays an accepted
// request rather than a rejected one.
func TestIncludeNullMembersAreAccepted(t *testing.T) {
	mux, _ := newServer(t)
	for _, body := range []string{
		`{"query":"x","include":{"entities":null}}`,
		`{"query":"x","include":{"chunks":null}}`,
		`{"query":"x","include":{"source_facts":null}}`,
		`{"query":"x","include":{}}`,
	} {
		if rec := post(t, mux, "/v1/default/banks/b1/memories/recall", body); rec.Code != http.StatusOK {
			t.Fatalf("recall %s = %d, body = %s", body, rec.Code, rec.Body.String())
		}
	}
	rmux, _ := reflectServer(t)
	for _, body := range []string{
		`{"query":"x","include":{"facts":null}}`,
		`{"query":"x","include":{"tool_calls":null}}`,
	} {
		if rec := post(t, rmux, "/v1/default/banks/b1/reflect", body); rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
			// A reflect deployment without a model answers 400; the point here is
			// that the request was not rejected as unprocessable.
			t.Fatalf("reflect %s = %d, body = %s", body, rec.Code, rec.Body.String())
		}
	}
}

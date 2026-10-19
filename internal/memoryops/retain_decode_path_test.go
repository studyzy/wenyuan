package memoryops

import (
	"net/http"
	"strings"
	"testing"
)

// The reference names a rejected write member at its full path, index levels
// included, with the offending value as the item's input. The item's own
// decoder cannot name the list index it sits at, so the route walks the body
// against the request contract; these expectations are the bodies the live
// reference serves for the same requests.
func TestRetainRejectionKeepsFullPathAndValue(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"a scalar for a nested object member",
			`{"items":[{"content":"hi","metadata":5}]}`,
			`{"detail":[{"type":"dict_type","loc":["body","items",0,"metadata"],` +
				`"msg":"Input should be a valid dictionary","input":5}]}`,
		},
		{
			"a list element that is not an object",
			`{"items":[1]}`,
			`{"detail":[{"type":"model_attributes_type","loc":["body","items",0],` +
				`"msg":"Input should be a valid dictionary or object to extract fields from","input":1}]}`,
		},
		{
			"a value for the content union",
			`{"items":[{"content":5}]}`,
			`{"detail":[` +
				`{"type":"string_type","loc":["body","items",0,"content","str"],` +
				`"msg":"Input should be a valid string","input":5},` +
				`{"type":"list_type","loc":["body","items",0,"content",` +
				`"list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]"],` +
				`"msg":"Input should be a valid list","input":5}]}`,
		},
	}
	for _, tc := range cases {
		rec := post(t, mux, "/v1/default/banks/b1/memories", tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, body = %s", tc.name, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// The reference validates an item in one pass, so a member that fails the
// contract's type and a sibling member that fails its own field rule are
// reported together, in field-declaration order. The shared decoder stops at the
// member it cannot place, so the item is decoded past it and the two sets are
// merged. These are the bodies the live reference serves for the same requests.
func TestRetainDecodeRejectionKeepsSiblingFieldRules(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"a bad metadata member beside a bad timestamp",
			`{"items":[{"content":"hi","metadata":5,"timestamp":"bad"}]}`,
			`{"detail":[` +
				`{"type":"value_error","loc":["body","items",0,"timestamp"],` +
				`"msg":"Value error, Invalid timestamp/event_date format: 'bad'. Expected ISO format like ` +
				`'2024-01-15T10:30:00' or '2024-01-15T10:30:00Z', or the special value 'unset' to store ` +
				`without a timestamp.","input":"bad","ctx":{"error":{}}},` +
				`{"type":"dict_type","loc":["body","items",0,"metadata"],` +
				`"msg":"Input should be a valid dictionary","input":5}]}`,
		},
		{
			"the same members in the other document order",
			`{"items":[{"content":"hi","timestamp":"bad","metadata":5}]}`,
			`{"detail":[` +
				`{"type":"value_error","loc":["body","items",0,"timestamp"],` +
				`"msg":"Value error, Invalid timestamp/event_date format: 'bad'. Expected ISO format like ` +
				`'2024-01-15T10:30:00' or '2024-01-15T10:30:00Z', or the special value 'unset' to store ` +
				`without a timestamp.","input":"bad","ctx":{"error":{}}},` +
				`{"type":"dict_type","loc":["body","items",0,"metadata"],` +
				`"msg":"Input should be a valid dictionary","input":5}]}`,
		},
		{
			"a bad metadata member beside a bad scope union",
			`{"items":[{"content":"hi","metadata":5,"observation_scopes":"bogus"}]}`,
			`{"detail":[` +
				`{"type":"dict_type","loc":["body","items",0,"metadata"],` +
				`"msg":"Input should be a valid dictionary","input":5},` +
				`{"type":"literal_error","loc":["body","items",0,"observation_scopes",` +
				`"literal['per_tag','combined','all_combinations','shared']"],` +
				`"msg":"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'",` +
				`"input":"bogus","ctx":{"expected":"'per_tag', 'combined', 'all_combinations' or 'shared'"}},` +
				`{"type":"list_type","loc":["body","items",0,"observation_scopes","list[list[str]]"],` +
				`"msg":"Input should be a valid list","input":"bogus"}]}`,
		},
	}
	for _, tc := range cases {
		rec := post(t, mux, "/v1/default/banks/b1/memories", tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, body = %s", tc.name, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

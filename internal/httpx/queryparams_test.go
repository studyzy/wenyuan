package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A scalar query parameter repeated binds its LAST occurrence, the way the
// reference's request framework reads a scalar from a multimap. url.Values.Get
// would return the first, which is the defect this pins.
func TestQueryBindsLastOccurrence(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?limit=1&limit=2&state=valid&state=invalidated", nil)
	if got := Query(req, "limit"); got != "2" {
		t.Fatalf("Query(limit) = %q, want the last occurrence %q", got, "2")
	}
	if got := Query(req, "state"); got != "invalidated" {
		t.Fatalf("Query(state) = %q, want the last occurrence %q", got, "invalidated")
	}
	// A single occurrence and an absent parameter are unchanged.
	if got := Query(req, "absent"); got != "" {
		t.Fatalf("Query(absent) = %q, want empty", got)
	}
	present := httptest.NewRequest(http.MethodGet, "/?only=7", nil)
	if got := Query(present, "only"); got != "7" {
		t.Fatalf("Query(only) = %q, want %q", got, "7")
	}
}

// A parameter that legitimately repeats as a sequence is read through
// QueryValues, which collects every occurrence in order rather than binding
// one. The scalar fix must not narrow it.
func TestQueryValuesCollectsAllInOrder(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?tags=a&tags=b&tags=c", nil)
	values := QueryValues(req, "tags")
	if len(values) != 3 || values[0] != "a" || values[1] != "b" || values[2] != "c" {
		t.Fatalf("QueryValues(tags) = %v, want [a b c]", values)
	}
	// QueryPresent likewise hands the caller every occurrence.
	values, present := QueryPresent(req, "tags")
	if !present || len(values) != 3 {
		t.Fatalf("QueryPresent(tags) = %v,%v", values, present)
	}
	// An explicitly empty occurrence is still an occurrence: Query reads it.
	empty := httptest.NewRequest(http.MethodGet, "/?q=&q=x", nil)
	if got := Query(empty, "q"); got != "x" {
		t.Fatalf("Query(q) = %q, want the last occurrence %q", got, "x")
	}
	trailing := httptest.NewRequest(http.MethodGet, "/?q=x&q=", nil)
	if got := Query(trailing, "q"); got != "" {
		t.Fatalf("Query(q) = %q, want the empty last occurrence", got)
	}
}

// An unescaped semicolon inside a query value is an ordinary character, not a
// field separator: the reference's parser only ever splits fields on `&`, so
// `?limit=5;6` binds the string "5;6" (which an integer binder then refuses)
// rather than reading as "no limit at all". Go's parser rejects the whole query
// when a `;` appears as a separator, and url.URL.Query silently drops the pair,
// which is the defect this pins: without the fallback every one of these reads
// as absent.
func TestQueryKeepsSemicolonsInValues(t *testing.T) {
	cases := []struct {
		target string
		name   string
		want   string
		values []string
	}{
		{target: "/?limit=5;6", name: "limit", want: "5;6", values: []string{"5;6"}},
		{target: "/?a=1;2&b=3", name: "a", want: "1;2", values: []string{"1;2"}},
		{target: "/?a=1;2&b=3", name: "b", want: "3", values: []string{"3"}},
		{target: "/?tag=x%3By&tag=z;w", name: "tag", want: "z;w", values: []string{"x;y", "z;w"}},
		// A trailing separator after `&` is an empty pair, not a value.
		{target: "/?q=a&r=b;", name: "r", want: "b;", values: []string{"b;"}},
	}
	for _, testCase := range cases {
		req := httptest.NewRequest(http.MethodGet, testCase.target, nil)
		if got := Query(req, testCase.name); got != testCase.want {
			t.Fatalf("%s: Query(%s) = %q, want %q", testCase.target, testCase.name, got, testCase.want)
		}
		values, present := QueryPresent(req, testCase.name)
		if !present || len(values) != len(testCase.values) {
			t.Fatalf("%s: QueryPresent(%s) = %v,%v, want %v,true", testCase.target, testCase.name, values, present, testCase.values)
		}
		for i := range testCase.values {
			if values[i] != testCase.values[i] {
				t.Fatalf("%s: QueryValues(%s)[%d] = %q, want %q", testCase.target, testCase.name, i, values[i], testCase.values[i])
			}
		}
	}
	// The fallback must not disturb a query without semicolons, including one
	// whose pairs the standard library decodes from escapes.
	plain := httptest.NewRequest(http.MethodGet, "/?q=a%26b&limit=5", nil)
	if got := Query(plain, "q"); got != "a&b" {
		t.Fatalf("Query(q) = %q, want the decoded %q", got, "a&b")
	}
}

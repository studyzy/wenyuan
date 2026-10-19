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

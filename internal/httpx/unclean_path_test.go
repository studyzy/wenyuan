package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newUncleanRouter builds a router whose table exercises both shapes of path
// parameter: a tail wildcard ("{name...}") and a single-segment wildcard
// ("{name}"), plus the descriptor surface.
func newUncleanRouter() *Router {
	router := NewRouter(RouterOptions{})
	handler := func(key, field string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			WriteJSON(w, http.StatusOK, map[string]string{field: r.PathValue(key)})
		}
	}
	router.Handle(http.MethodGet, "/v1/default/banks", handler("bank_id", "bank"))
	router.Handle(http.MethodGet, "/v1/default/banks/{bank_id}", handler("bank_id", "bank"))
	router.Handle(http.MethodGet, "/v1/default/chunks/{chunk_id...}", handler("chunk_id", "chunk"))
	router.Handle(http.MethodGet, "/v1/default/files/download/{key...}", handler("key", "key"))
	return router
}

func serve(service http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// A doubled slash inside a tail wildcard's capture is part of the value, exactly
// as the reference's router captures the raw path remainder. The mux would
// otherwise clean the path and answer 301 before the handler ran.
func TestUncleanPathReachesTailWildcard(t *testing.T) {
	service := newUncleanRouter().Service()
	cases := []struct {
		path  string
		field string
		value string
	}{
		{"/v1/default/chunks//abc", "chunk", "/abc"},
		{"/v1/default/chunks/abc/", "chunk", "abc/"},
		{"/v1/default/chunks/a/../b", "chunk", "a/../b"},
		{"/v1/default/files/download//banks/b1/x.zip", "key", "/banks/b1/x.zip"},
		{"/v1/default/files/download/../etc/passwd", "key", "../etc/passwd"},
	}
	for _, tc := range cases {
		rec := serve(service, http.MethodGet, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body %s)", tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"`+tc.field+`":"`+tc.value+`"`) {
			t.Fatalf("GET %s body = %s, want %s=%q", tc.path, rec.Body.String(), tc.field, tc.value)
		}
	}
}

// A doubled slash leaves an empty element that no single-segment wildcard can
// absorb, so the reference matches no route. The mux would otherwise merge the
// element and let the wildcard capture a value the reference never supplies.
func TestUncleanPathWithSegmentWildcardIsNotFound(t *testing.T) {
	service := newUncleanRouter().Service()
	for _, path := range []string{
		"/v1/default/banks//x",
		"/v1/default/banks/./x",
		"/v1/default//chunks/x",
	} {
		rec := serve(service, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404 (body %s)", path, rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) != `{"detail":"Not Found"}` {
			t.Fatalf("GET %s body = %q, want the reference's Not Found envelope", path, rec.Body.String())
		}
	}
}

// The reference strips every trailing slash and redirects to the stripped path
// when it matches a route, whatever the number of slashes.
func TestUncleanPathTrailingSlashRedirects(t *testing.T) {
	service := newUncleanRouter().Service()
	cases := map[string]string{
		"/v1/default/banks/":    "http://example.com/v1/default/banks",
		"/v1/default/banks//":   "http://example.com/v1/default/banks",
		"/v1/default/banks/x//": "http://example.com/v1/default/banks/x",
		"/docs/":                "http://example.com/docs",
	}
	for path, location := range cases {
		rec := serve(service, http.MethodGet, path)
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != location {
			t.Fatalf("GET %s = %d %q, want 307 %q", path, rec.Code, rec.Header().Get("Location"), location)
		}
	}
}

// A clean path keeps resolving to its handler, so the canonicalisation guard
// leaves the ordinary route table untouched.
func TestCleanPathStillResolves(t *testing.T) {
	service := newUncleanRouter().Service()
	rec := serve(service, http.MethodGet, "/v1/default/chunks/ok")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"chunk":"ok"`) {
		t.Fatalf("GET /v1/default/chunks/ok = %d %s", rec.Code, rec.Body.String())
	}
}

package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The contract document is the reference's own, so the version the version
// endpoint reports cannot drift from the version inside it.
func TestAPIVersionMatchesContractDocument(t *testing.T) {
	var document struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal([]byte(openAPIDocument), &document); err != nil {
		t.Fatalf("the embedded contract document does not parse: %v", err)
	}
	if document.Info.Version != APIVersion {
		t.Fatalf("api version = %q, document version = %q", APIVersion, document.Info.Version)
	}
	if len(document.Paths) == 0 {
		t.Fatal("the embedded contract document declares no paths")
	}
}

// The descriptor surface answers without a base path, and the document carries
// no servers entry, exactly as the reference's does.
func TestDescriptorSurface(t *testing.T) {
	router := NewRouter(RouterOptions{})
	service := router.Service()

	cases := []struct {
		path        string
		status      int
		contentType string
		body        string
	}{
		{"/openapi.json", http.StatusOK, "application/json", `"openapi"`},
		{"/docs", http.StatusOK, "text/html; charset=utf-8", "SwaggerUIBundle"},
		{"/docs/oauth2-redirect", http.StatusOK, "text/html; charset=utf-8", "swaggerUIRedirectOauth2"},
		{"/redoc", http.StatusOK, "text/html; charset=utf-8", "redoc spec-url=\"/openapi.json\""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("GET %s: status = %d, want %d", tc.path, rec.Code, tc.status)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("GET %s: content type = %q, want %q", tc.path, got, tc.contentType)
		}
		// The reference's descriptor routes are plain responses, so they carry
		// the body length rather than a chunked encoding.
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
			t.Fatalf("GET %s: content length = %q, body = %d", tc.path, got, rec.Body.Len())
		}
		if !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("GET %s: body does not contain %q", tc.path, tc.body)
		}
	}
	if strings.Contains(OpenAPIDocument(""), `"servers"`) {
		t.Fatal("the document must not carry a servers entry without a base path")
	}
	// The pages answer their trailing-slash spelling with a redirect to the
	// page itself, as the reference's router does: 307, an absolute Location,
	// and the query preserved.
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/?limit=1", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "http://example.com/docs?limit=1" {
		t.Fatalf("GET /docs/ = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, page := range []string{"/openapi.json", "/docs/oauth2-redirect", "/redoc"} {
		rec = httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page+"/", nil))
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "http://example.com"+page {
			t.Fatalf("GET %s/ = %d %q", page, rec.Code, rec.Header().Get("Location"))
		}
	}
	// The redirect answers any method, as the reference's does.
	rec = httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/docs/", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("PUT /docs/ = %d, want 307", rec.Code)
	}
}

// The documentation pages take their title from the served document's
// info.title, exactly as the reference's FastAPI renders it.
func TestDescriptorPageTitles(t *testing.T) {
	var document struct {
		Info struct {
			Title string `json:"title"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(openAPIDocument), &document); err != nil {
		t.Fatalf("the embedded contract document does not parse: %v", err)
	}
	service := NewRouter(RouterOptions{}).Service()
	cases := map[string]string{
		"/docs":  document.Info.Title + " - Swagger UI",
		"/redoc": document.Info.Title + " - ReDoc",
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if want := "<title>" + want + "</title>"; !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET %s: body does not contain %q", path, want)
		}
	}
}

// A descriptor route admits HEAD, because it is a plain route whose GET implies
// it, and reports `Allow: HEAD, GET` for a method it does not take — unlike an
// API route. An API route rejects HEAD and reports only its decorated method.
func TestDescriptorMethodSemantics(t *testing.T) {
	router := NewRouter(RouterOptions{})
	router.Handle(http.MethodGet, "/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	for _, path := range []string{"/openapi.json", "/docs", "/docs/oauth2-redirect", "/redoc"} {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("HEAD %s = %d, want 200", path, rec.Code)
		}
		rec = httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "HEAD, GET" {
			t.Fatalf("POST %s = %d allow=%q, want 405 allow=HEAD, GET", path, rec.Code, rec.Header().Get("Allow"))
		}
	}

	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" {
		t.Fatalf("POST /health = %d allow=%q, want 405 allow=GET", rec.Code, rec.Header().Get("Allow"))
	}
	rec = httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" {
		t.Fatalf("HEAD /health = %d allow=%q, want 405 allow=GET", rec.Code, rec.Header().Get("Allow"))
	}
}

// A descriptor route never reports ignored parameters, because the reference
// serves it from a plain route rather than the reporting route class.
func TestDescriptorRouteReportsNoIgnoredParams(t *testing.T) {
	service := NewRouter(RouterOptions{}).Service()
	for _, path := range []string{"/docs", "/openapi.json", "/redoc", "/docs/oauth2-redirect"} {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?foo=1", nil))
		if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
			t.Fatalf("GET %s?foo=1: ignored header = %q, want none", path, got)
		}
	}
}

// A request that reaches no route is answered by the router, so a configured
// credential never gates it: an unmatched path is 404 and a method mismatch is
// 405, neither of which is a 401.
func TestAuthFollowsRouting(t *testing.T) {
	router := NewRouter(RouterOptions{TenantAPIKey: "s3cret"})
	router.Handle(http.MethodGet, "/v1/default/banks", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method mismatch = %d, want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("dispatched route without a key = %d, want 401", rec.Code)
	}
}

// The ignored-parameter header keeps only printable ASCII, replacing anything
// else with '?', as the reference's _header_safe does.
func TestIgnoredParamsHeaderSanitized(t *testing.T) {
	if got := formatIgnoredParams([]string{"naïve", "a\tb"}); got != "na?ve, a?b" {
		t.Fatalf("formatIgnoredParams = %q, want %q", got, "na?ve, a?b")
	}
}

// A path declared for two methods reports only the first-declared one in Allow,
// because the reference's router answers with the Allow header of the first
// route that partially matched.
func TestAllowReportsFirstDeclaredMethod(t *testing.T) {
	router := NewRouter(RouterOptions{})
	router.Handle(http.MethodPost, "/v1/default/banks/{bank_id}/memories", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Handle(http.MethodDelete, "/v1/default/banks/{bank_id}/memories", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/default/banks/b1/memories", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("PUT = %d allow=%q, want 405 allow=POST", rec.Code, rec.Header().Get("Allow"))
	}
}

// A configured base path serves every route under the prefix as well as at the
// root, and the document reports the prefix as its server.
func TestBasePathServesBothSpellings(t *testing.T) {
	router := NewRouter(RouterOptions{BasePath: "/wenyuan"})
	router.Handle(http.MethodGet, "/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	for _, path := range []string{"/health", "/wenyuan/health", "/wenyuan/openapi.json"} {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
	}
	document := OpenAPIDocument("/wenyuan")
	if !strings.Contains(document, `"servers":[{"url":"/wenyuan"}]`) {
		t.Fatal("the document must name the base path as its server")
	}
	if !json.Valid([]byte(document)) {
		t.Fatal("the document with a server injected is not valid JSON")
	}
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wenyuan/docs", nil))
	if !strings.Contains(rec.Body.String(), `url: '/wenyuan/openapi.json'`) {
		t.Fatalf("the page must read the contract under the base path: %s", rec.Body.String())
	}
}

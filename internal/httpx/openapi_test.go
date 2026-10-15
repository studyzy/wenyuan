package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("GET %s: body does not contain %q", tc.path, tc.body)
		}
	}
	if strings.Contains(OpenAPIDocument(""), `"servers"`) {
		t.Fatal("the document must not carry a servers entry without a base path")
	}
	// The pages answer their trailing-slash spelling with a redirect, as the
	// reference's router does.
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/docs" {
		t.Fatalf("GET /docs/ = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A configured base path serves every route under the prefix as well as at the
// root, and the document reports the prefix as its server.
func TestBasePathServesBothSpellings(t *testing.T) {
	router := NewRouter(RouterOptions{BasePath: "/hindsight"})
	router.Handle(http.MethodGet, "/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	for _, path := range []string{"/health", "/hindsight/health", "/hindsight/openapi.json"} {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
	}
	document := OpenAPIDocument("/hindsight")
	if !strings.Contains(document, `"servers":[{"url":"/hindsight"}]`) {
		t.Fatal("the document must name the base path as its server")
	}
	if !json.Valid([]byte(document)) {
		t.Fatal("the document with a server injected is not valid JSON")
	}
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hindsight/docs", nil))
	if !strings.Contains(rec.Body.String(), `url: '/hindsight/openapi.json'`) {
		t.Fatalf("the page must read the contract under the base path: %s", rec.Body.String())
	}
}

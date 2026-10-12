package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

type fixedAuth bool

func (a fixedAuth) Authenticated(r *http.Request) bool { return bool(a) }

// ts-0007 / sec-0002 / neg-0003: retired routes remain routable, reject
// unauthenticated callers, and return the terminal gone status when
// authenticated.
func TestRetiredRoutesRemainRoutable(t *testing.T) {
	mux := http.NewServeMux()
	register := func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	}

	retired := &RetiredHandler{Auth: fixedAuth(true)}
	retired.BindRetiredRoutes(register)

	for _, route := range RetiredRoutes {
		path := replaceBankID(route.Pattern)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(route.Method, path, nil))
		if rec.Code != http.StatusGone {
			t.Fatalf("retired %s %s status = %d, want 410", route.Method, path, rec.Code)
		}
		var envelope map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["detail"] == "" {
			t.Fatalf("retirement message must be present for %s: %s", path, rec.Body.String())
		}
	}
}

func TestRetiredRoutesRejectUnauthenticatedCallers(t *testing.T) {
	mux := http.NewServeMux()
	register := func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	}
	(&RetiredHandler{Auth: fixedAuth(false)}).BindRetiredRoutes(register)

	path := replaceBankID(RetiredRoutes[0].Pattern)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}
}

func TestRetiredRoutesWithNilAuthAreDenied(t *testing.T) {
	mux := http.NewServeMux()
	(&RetiredHandler{}).BindRetiredRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	path := replaceBankID(RetiredRoutes[0].Pattern)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a missing authenticator must deny access, got %d", rec.Code)
	}
}

// ts-0018: the retirement message names the configuration read and write routes
// and the bank list route.
func TestRetiredMessagePointsAtCurrentFields(t *testing.T) {
	for _, needle := range []string{"config", "banks"} {
		if !containsFold(httpx.DetailRetired, needle) {
			t.Fatalf("retirement detail must name %q: %q", needle, httpx.DetailRetired)
		}
	}
}

func replaceBankID(pattern string) string {
	return replaceFirstPathParam(pattern, "b1")
}

func replaceFirstPathParam(pattern, value string) string {
	for i := 0; i+len("{bank_id}") <= len(pattern); i++ {
		if pattern[i:i+len("{bank_id}")] == "{bank_id}" {
			return pattern[:i] + value + pattern[i+len("{bank_id}"):]
		}
	}
	return pattern
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	hl := make([]byte, len(haystack))
	for i := 0; i < len(haystack); i++ {
		c := haystack[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		hl[i] = c
	}
	nl := make([]byte, len(needle))
	for i := 0; i < len(needle); i++ {
		c := needle[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		nl[i] = c
	}
	return indexBytes(hl, nl) >= 0
}

func indexBytes(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

var _ storage.Bank

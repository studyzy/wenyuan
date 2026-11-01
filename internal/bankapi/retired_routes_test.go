package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ts-0007 / sec-0002 / neg-0003: retired routes remain routable and answer the
// terminal gone status without a credential, each with its own message.
func TestRetiredRoutesRemainRoutable(t *testing.T) {
	mux := http.NewServeMux()
	register := func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	}

	retired := &RetiredHandler{}
	retired.BindRetiredRoutes(register)

	for _, route := range RetiredRoutes {
		path := replaceBankID(route.Pattern)
		rec := httptest.NewRecorder()
		// A retired route that declares a request body needs that body to be
		// acceptable before it answers the retirement status; a route without one
		// answers it directly.
		body := acceptableRetiredBody(route)
		mux.ServeHTTP(rec, httptest.NewRequest(route.Method, path, strings.NewReader(body)))
		if rec.Code != http.StatusGone {
			t.Fatalf("retired %s %s status = %d, want 410", route.Method, path, rec.Code)
		}
		var envelope map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["detail"] != route.Detail {
			t.Fatalf("retired %s %s detail = %q, want %q", route.Method, path, envelope["detail"], route.Detail)
		}
	}
}

// t-retired-profile-requires-auth: the retired PROFILE routes are answered only
// after authentication, so an unauthenticated caller is refused instead of
// learning the route exists and is retired. The retired background route is
// not on that list and keeps answering without a credential.
//
// The refusal comes from the router's credential middleware, so this test goes
// through the composed router with a credential configured; a family mux alone
// has no authenticator to consult.
func TestRetiredProfileRequiresAuthBeforeRevealingRetirement(t *testing.T) {
	router := httpx.NewRouter(httpx.RouterOptions{TenantAPIKey: "secret-key"})
	(&RetiredHandler{}).BindRetiredRoutes(router.Handle)

	profile := []struct{ method, pattern string }{
		{http.MethodGet, httpx.BankPath(httpx.SuffixProfile)},
		{http.MethodPut, httpx.BankPath(httpx.SuffixProfile)},
	}
	for _, route := range profile {
		req := httptest.NewRequest(route.method, replaceBankID(route.pattern), nil)
		rec := httptest.NewRecorder()
		router.Service().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a credential = %d, want 401", route.method, route.pattern, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "retired") || strings.Contains(rec.Body.String(), "410") {
			t.Fatalf("%s %s leaked the retirement: %s", route.method, route.pattern, rec.Body.String())
		}

		// The same request with the credential reaches the retirement status.
		body := acceptableRetiredBody(RetiredRoute{Method: route.method, Pattern: route.pattern})
		authed := httptest.NewRequest(route.method, replaceBankID(route.pattern), strings.NewReader(body))
		authed.Header.Set("Authorization", "Bearer secret-key")
		authedRec := httptest.NewRecorder()
		router.Service().ServeHTTP(authedRec, authed)
		if authedRec.Code != http.StatusGone {
			t.Fatalf("%s %s with a credential = %d, want 410 (body %s)",
				route.method, route.pattern, authedRec.Code, authedRec.Body.String())
		}
	}

	// The background route stays reachable without a credential.
	background := httptest.NewRequest(http.MethodPost, replaceBankID(httpx.BankPath(httpx.SuffixBackground)),
		strings.NewReader(`{"content":"x"}`))
	backgroundRec := httptest.NewRecorder()
	router.Service().ServeHTTP(backgroundRec, background)
	if backgroundRec.Code != http.StatusGone {
		t.Fatalf("retired background without a credential = %d, want 410 (body %s)",
			backgroundRec.Code, backgroundRec.Body.String())
	}
}

// ts-0018: the two retirement messages each name the configuration routes and
// the bank list route.
func TestRetiredMessagePointsAtCurrentFields(t *testing.T) {
	for _, detail := range []string{httpx.DetailRetiredProfile, httpx.DetailRetiredBackground} {
		for _, needle := range []string{"config", "banks"} {
			if !containsFold(detail, needle) {
				t.Fatalf("retirement detail must name %q: %q", needle, detail)
			}
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

// acceptableRetiredBody is the body a retired write route's declared model
// accepts, so the route reaches its retirement status.
func acceptableRetiredBody(route RetiredRoute) string {
	switch retiredRouteKey(route.Method, route.Pattern) {
	case "PUT /v1/default/banks/{bank_id}/profile":
		return `{"disposition":{"skepticism":3,"literalism":3,"empathy":3}}`
	case "POST /v1/default/banks/{bank_id}/background":
		return `{"content":"x"}`
	}
	return ""
}

package httpx

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// capableAuth is a test authenticator that also supplies tenant overrides.
type capableAuth struct {
	overrides map[string]any
}

func (capableAuth) Authenticated(*http.Request) bool { return true }

func (a capableAuth) TenantOverrides(*http.Request) map[string]any { return a.overrides }

// plainAuth is an authenticator without the optional capability.
type plainAuth struct{}

func (plainAuth) Authenticated(*http.Request) bool { return true }

func overridesHandler(seen *map[string]any) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/default/banks/{bank_id}/memories/recall", func(w http.ResponseWriter, r *http.Request) {
		*seen = TenantOverridesFrom(r)
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	})
	return mux
}

// An authenticator that supplies overrides has them recorded on the request
// context for a downstream handler to read.
func TestTenantOverridesMiddlewareRecordsCapableAuthenticator(t *testing.T) {
	var seen map[string]any
	handler := Chain(overridesHandler(&seen), NullDropMiddleware(false),
		TenantOverridesMiddleware(capableAuth{overrides: map[string]any{"enable_text_search": false}}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", nil))

	if got := seen["enable_text_search"]; got != false {
		t.Fatalf("overrides = %v, want the authenticator's", seen)
	}
}

// An authenticator without the optional capability changes nothing: the
// middleware is a pass-through and the handler sees no overrides.
func TestTenantOverridesMiddlewarePassesThroughWithoutCapability(t *testing.T) {
	var seen map[string]any
	handler := Chain(overridesHandler(&seen), NullDropMiddleware(false),
		TenantOverridesMiddleware(plainAuth{}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if seen != nil {
		t.Fatalf("overrides = %v, want none from a capability-less authenticator", seen)
	}
}

// A nil authenticator is also a pass-through, so a router built without one
// keeps working.
func TestTenantOverridesMiddlewareNilAuthenticatorPassesThrough(t *testing.T) {
	var seen map[string]any
	handler := Chain(overridesHandler(&seen), NullDropMiddleware(false), TenantOverridesMiddleware(nil))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", nil))
	if rec.Code != http.StatusOK || seen != nil {
		t.Fatalf("nil authenticator changed behaviour: status = %d overrides = %v", rec.Code, seen)
	}
}

// The context helpers round-trip the overrides exactly.
func TestTenantOverridesContextRoundTrip(t *testing.T) {
	overrides := map[string]any{"retain_chunk_size": 5}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if TenantOverridesFrom(req) != nil {
		t.Fatal("a request with no overrides must yield nil")
	}
	req = WithTenantOverrides(req, overrides)
	if !reflect.DeepEqual(TenantOverridesFrom(req), overrides) {
		t.Fatalf("overrides = %v, want the recorded map", TenantOverridesFrom(req))
	}
}

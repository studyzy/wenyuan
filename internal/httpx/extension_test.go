package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollectPassthroughHeadersAllowlistsAndLowercases(t *testing.T) {
	header := http.Header{}
	header.Set("X-Tenant-Id", "acme")
	header.Set("X-Trace-Id", "abc")
	header.Set("Authorization", "Bearer secret")

	got := CollectPassthroughHeaders(header, []string{"x-tenant-id", "X-Trace-Id"})

	if got["x-tenant-id"] != "acme" {
		t.Fatalf("allowlisted header must be keyed lower-cased: %v", got)
	}
	if got["x-trace-id"] != "abc" {
		t.Fatalf("allowlist matching must ignore case: %v", got)
	}
	if _, ok := got["authorization"]; ok {
		t.Fatalf("a header outside the allowlist must not be exposed: %v", got)
	}
}

func TestCollectPassthroughHeadersEmptyAllowlistExposesNothing(t *testing.T) {
	header := http.Header{}
	header.Set("X-Tenant-Id", "acme")

	if got := CollectPassthroughHeaders(header, nil); got != nil {
		t.Fatalf("nil allowlist must expose nothing, got %v", got)
	}
	if got := CollectPassthroughHeaders(header, []string{}); got != nil {
		t.Fatalf("empty allowlist must expose nothing, got %v", got)
	}
	if got := CollectPassthroughHeaders(header, []string{"", "  "}); got != nil {
		t.Fatalf("blank allowlist entries must expose nothing, got %v", got)
	}
}

func TestCollectPassthroughHeadersDropsDuplicates(t *testing.T) {
	header := http.Header{}
	header.Add("X-Tenant-Id", "spoofed")
	header.Add("X-Tenant-Id", "trusted")

	got := CollectPassthroughHeaders(header, []string{"x-tenant-id"})
	if _, ok := got["x-tenant-id"]; ok {
		t.Fatalf("a duplicated header must be dropped, got %v", got)
	}
}

func TestExtraHeadersFromEmptyRequestIsNil(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ext/anything", nil)
	if got := ExtraHeadersFrom(req); got != nil {
		t.Fatalf("a request with no recorded headers must yield nil, got %v", got)
	}
}

// A header an extension reads off the request context reaches it only when the
// host was given an allowlist naming it; a header outside the allowlist never
// arrives.
func TestMountExtensionsForwardsAllowlistedHeader(t *testing.T) {
	host := NewExtensionHost()
	host.SetPassthroughHeaders([]string{"X-Tenant-Id"})
	var seen map[string]string
	host.Register(extensionFunc(func(r RouteRegistrar) {
		r.Handle(http.MethodGet, "/probe", func(w http.ResponseWriter, req *http.Request) {
			seen = ExtraHeadersFrom(req)
			w.WriteHeader(http.StatusOK)
		})
	}))

	mux := http.NewServeMux()
	MountExtensions(mux, host, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodGet, NamespaceExt+"/probe", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	req.Header.Set("X-Other", "nope")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("extension route status = %d", rec.Code)
	}
	if seen["x-tenant-id"] != "acme" {
		t.Fatalf("extension must read the allowlisted header, got %v", seen)
	}
	if _, ok := seen["x-other"]; ok {
		t.Fatalf("a header outside the allowlist must not reach the extension: %v", seen)
	}
}

// With no allowlist configured the extension context carries nothing, so a
// deployment that never opted in sees no change.
func TestMountExtensionsWithoutAllowlistForwardsNothing(t *testing.T) {
	host := NewExtensionHost()
	called := false
	host.Register(extensionFunc(func(r RouteRegistrar) {
		r.Handle(http.MethodGet, "/probe", func(w http.ResponseWriter, req *http.Request) {
			called = true
			if got := ExtraHeadersFrom(req); got != nil {
				t.Fatalf("no allowlist must expose nothing, got %v", got)
			}
			w.WriteHeader(http.StatusOK)
		})
	}))

	mux := http.NewServeMux()
	MountExtensions(mux, host, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodGet, NamespaceExt+"/probe", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !called {
		t.Fatal("extension route was not reached")
	}
}

// extensionFunc adapts a mount callback to the Extension interface.
type extensionFunc func(RouteRegistrar)

func (f extensionFunc) Mount(r RouteRegistrar) { f(r) }

package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticExtension struct{ mounted bool }

func (s *staticExtension) Mount(r RouteRegistrar) {
	s.mounted = true
	r.Handle(http.MethodGet, "/ping", func(w http.ResponseWriter, req *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"pong": "ok"})
	})
}

// ts-0018: an extension's routes are served under the fixed extension prefix
// and unknown-parameter reporting applies to them.
func TestExtensionNamespaceServesExtensionRoutes(t *testing.T) {
	host := NewExtensionHost()
	ext := &staticExtension{}
	host.Register(ext)

	root := NewRouter(RouterOptions{Extensions: host})
	rec := httptest.NewRecorder()
	root.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/ping?bogus=1", nil))

	if !ext.mounted {
		t.Fatal("extension was not mounted")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("extension body missing: %s", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderIgnoredParams); got != "bogus" {
		t.Fatalf("X-Ignored-Params = %q, want bogus", got)
	}
}

// ts-0017: when MCP is disabled the sub-path is not served; when enabled it is.
func TestMCPNamespaceGating(t *testing.T) {
	disabled := NewRouter(RouterOptions{EnabledMCP: false})
	rec := httptest.NewRecorder()
	disabled.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled MCP must not be served, got %d", rec.Code)
	}

	enabled := NewRouter(RouterOptions{EnabledMCP: true})
	rec = httptest.NewRecorder()
	enabled.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp/", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("enabled MCP must be served at the fixed sub-path")
	}
}

// The MCP namespace is served as a mounted sub-application, so no route
// declares its query parameters and no ignored-parameter header is produced
// for it, matching the reference where the reporting route class never sees an
// MCP request.
func TestMCPNamespaceReportsNoIgnoredParams(t *testing.T) {
	router := NewRouter(RouterOptions{EnabledMCP: true})
	rec := httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp/?bogus=1", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("MCP must not report ignored parameters, got %q", got)
	}
}

// declaredExtension registers a route that declares one accepted parameter, so
// the extension namespace's report is the extension's own declaration rather
// than a process-wide allowlist.
type declaredExtension struct{}

func (d *declaredExtension) Mount(r RouteRegistrar) {
	r.Handle(http.MethodGet, "/declared", QueryParams(func(w http.ResponseWriter, req *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"ok": "1"})
	}, "limit"))
}

// An extension route that declares a parameter does not report it, while a
// name it did not declare is still reported: the extension namespace keeps its
// own declaration rather than inheriting the core routes' parameters.
func TestExtensionNamespaceDeclaresItsOwnParams(t *testing.T) {
	host := NewExtensionHost()
	host.Register(&declaredExtension{})
	router := NewRouter(RouterOptions{Extensions: host})

	rec := httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/declared?limit=1", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("a declared extension parameter must not be reported, got %q", got)
	}

	rec = httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/declared?tag=1", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "tag" {
		t.Fatalf("an undeclared extension parameter must be reported, got %q", got)
	}
}

// An unmatched path and a method mismatch are answered by the router, not by a
// route handler, so the reference emits no ignored-parameter header for them
// even though the query string carries an unknown name. A matched route still
// reports.
func TestIgnoredParamsOnlyOnDispatchedRoute(t *testing.T) {
	router := NewRouter(RouterOptions{})
	router.Handle(http.MethodGet, BankPath(SuffixStats), QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}, "refresh"))
	handler := router.Service()

	// Control: the route runs and reports the unknown parameter.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/stats?bogus=1", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "bogus" {
		t.Fatalf("matched route X-Ignored-Params = %q, want bogus", got)
	}

	// Unmatched path: the fallback's 404 carries no header.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/nope?bogus=1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unmatched path status = %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("unmatched path X-Ignored-Params = %q, want none", got)
	}

	// Method mismatch: the router's 405 carries no header.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/stats?bogus=1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method mismatch status = %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("method mismatch X-Ignored-Params = %q, want none", got)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("method mismatch Allow = %q, want GET", got)
	}
}

func TestBankPathHelpers(t *testing.T) {
	if got := BankPath(SuffixMemories); got != "/v1/default/banks/{bank_id}/memories" {
		t.Fatalf("memory write path = %q", got)
	}
	if got := BankMemberPath(); got != "/v1/default/banks/{bank_id}" {
		t.Fatalf("bank member path = %q", got)
	}
	if got := BanksCollectionPath(); got != "/v1/default/banks" {
		t.Fatalf("banks collection path = %q", got)
	}
}

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
		WriteJSONFor(w, req, http.StatusOK, map[string]string{"pong": "ok"})
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

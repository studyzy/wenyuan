package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The reference registers /mcp and /mcp/{bank_id} as whole paths, so a deeper
// one matches nothing and its plain-text 404 answers it. A Go mux would instead
// let /mcp/{bank_id}/ swallow the deeper path.
func TestMCPDeeperThanABankMountIsTheFrameworkNotFound(t *testing.T) {
	for _, path := range []string{"/mcp/a/b", "/mcp/a/b/c", "/mcp/a/b/c/d"} {
		if !(&Router{options: RouterOptions{MCPHandler: http.NotFoundHandler()}}).undeclaredMCPDepth(
			httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("%s: want it answered by the framework's 404", path)
		}
	}
}

// Deeper is deeper whatever the extra segments spell: the reference's
// /mcp/{bank_id} is a whole path, so /mcp/a/tools/list is its plain-text 404 too.
func TestMCPApplicationPathUnderABankMountIsAlsoRejected(t *testing.T) {
	for _, path := range []string{"/mcp/a/tools/list", "/mcp/a/tools/call", "/mcp/a/messages"} {
		if !(&Router{options: RouterOptions{MCPHandler: http.NotFoundHandler()}}).undeclaredMCPDepth(
			httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("%s: want it answered by the framework's 404", path)
		}
	}
}

// The declared mounts themselves are served.
func TestMCPDeclaredMountsAreServed(t *testing.T) {
	for _, path := range []string{"/mcp", "/mcp/", "/mcp/a", "/mcp/a/"} {
		if (&Router{options: RouterOptions{MCPHandler: http.NotFoundHandler()}}).undeclaredMCPDepth(
			httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("%s: a declared mount is served", path)
		}
	}
}

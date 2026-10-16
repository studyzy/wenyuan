package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// proxyProbe answers with the client address and scheme the middleware left on
// the request.
func proxyProbe() (http.Handler, *http.Request) {
	seen := &http.Request{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = *r
		w.WriteHeader(http.StatusOK)
	})
	return handler, seen
}

// A peer that is not trusted has its forwarded headers ignored: the header is
// caller-controlled, so believing it from an unknown peer would let any client
// claim to be anyone.
func TestProxyHeadersIgnoreUntrustedPeer(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware([]string{"10.0.0.1"})(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.RemoteAddr != "203.0.113.9:5555" {
		t.Fatalf("untrusted peer's forwarded address was believed: %q", seen.RemoteAddr)
	}
	if seen.URL.Scheme != "" {
		t.Fatalf("untrusted peer's forwarded scheme was believed: %q", seen.URL.Scheme)
	}
}

// A trusted peer's forwarded client is the request's client, with the port the
// header cannot carry reported as the reference reports it.
func TestProxyHeadersTrustedPeerRewritesClient(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware([]string{"127.0.0.1"})(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.RemoteAddr != "198.51.100.7:0" {
		t.Fatalf("trusted peer's client = %q, want 198.51.100.7:0", seen.RemoteAddr)
	}
	if seen.URL.Scheme != "https" {
		t.Fatalf("forwarded scheme = %q, want https", seen.URL.Scheme)
	}
}

// The list is walked from the right, so the client is the first entry no trusted
// proxy vouched for — the left-most entries are the caller's own claim.
func TestProxyHeadersTakeFirstUntrustedFromTheRight(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware([]string{"10.0.0.1", "172.16.0.0/12"})(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "10.0.0.1:4444"
	req.Header.Set("X-Forwarded-For", "spoofed-by-the-client, 203.0.113.5, 172.16.9.9")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.RemoteAddr != "203.0.113.5:0" {
		t.Fatalf("client = %q, want the first untrusted hop 203.0.113.5:0", seen.RemoteAddr)
	}
}

// "*" trusts every peer, and then the left-most entry is the client.
func TestProxyHeadersWildcardTrustsEveryPeer(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware([]string{"*"})(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.RemoteAddr != "198.51.100.7:0" {
		t.Fatalf("client = %q, want the left-most entry", seen.RemoteAddr)
	}
}

// Without the flag there is no trusted peer at all, so the middleware is a
// pass-through even when a header is present.
func TestProxyHeadersDisabledBelievesNothing(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware(nil)(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.RemoteAddr != "127.0.0.1:4444" {
		t.Fatalf("client = %q, want the peer unchanged", seen.RemoteAddr)
	}
}

// A scheme the reference does not accept is ignored rather than copied into the
// request.
func TestProxyHeadersIgnoreUnknownScheme(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware([]string{"127.0.0.1"})(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	req.Header.Set("X-Forwarded-Proto", "ftp")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.URL.Scheme != "" {
		t.Fatalf("unknown scheme was accepted: %q", seen.URL.Scheme)
	}
}

// A websocket upgrade keeps the ws scheme the reference translates it into.
func TestProxyHeadersWebsocketScheme(t *testing.T) {
	next, seen := proxyProbe()
	handler := ProxyHeadersMiddleware([]string{"127.0.0.1"})(next)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.URL.Scheme != "wss" {
		t.Fatalf("websocket scheme = %q, want wss", seen.URL.Scheme)
	}
}

// A non-address entry is compared literally, which is how a deployment naming a
// unix socket's peer or a hostname configures trust.
func TestTrustedHostsLiteralEntries(t *testing.T) {
	hosts := parseTrustedHosts([]string{"proxy.internal", "10.0.0.0/8"})
	if !hosts.trusts("proxy.internal") {
		t.Fatal("literal entry not trusted")
	}
	if !hosts.trusts("10.1.2.3") {
		t.Fatal("network entry not trusted")
	}
	if hosts.trusts("192.168.1.1") {
		t.Fatal("unlisted address trusted")
	}
}

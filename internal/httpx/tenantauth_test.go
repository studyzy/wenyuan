package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A configured key authenticates the engine-backed namespaces and leaves the
// monitoring and descriptor routes open, which is what the reference does.
func TestTenantAPIKeyGate(t *testing.T) {
	router := NewRouter(RouterOptions{TenantAPIKey: "s3cret"})
	router.Handle(http.MethodGet, "/v1/default/banks", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Handle(http.MethodGet, PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	cases := []struct {
		name       string
		path       string
		credential string
		status     int
	}{
		{"bearer accepted", "/v1/default/banks", "Bearer s3cret", http.StatusOK},
		{"bare key accepted", "/v1/default/banks", "s3cret", http.StatusOK},
		{"wrong key rejected", "/v1/default/banks", "Bearer nope", http.StatusUnauthorized},
		{"missing key rejected", "/v1/default/banks", "", http.StatusUnauthorized},
		{"monitoring open", "/health", "", http.StatusOK},
		{"contract document open", "/openapi.json", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.credential != "" {
				req.Header.Set("Authorization", tc.credential)
			}
			rec := httptest.NewRecorder()
			service.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if tc.status == http.StatusUnauthorized {
				var body struct {
					Detail string `json:"detail"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("body is not the error envelope: %v", err)
				}
				if body.Detail != DetailAuthenticationFailed {
					t.Fatalf("detail = %q, want %q", body.Detail, DetailAuthenticationFailed)
				}
			}
		})
	}
}

// The retired BACKGROUND route is the one the reference answers before reaching
// the engine, so it keeps its own status even when a credential is configured.
// The retired profile routes are engine routes and so sit behind the check —
// see the bank lifecycle's test for those.
func TestTenantAPIKeyDoesNotGateRetiredBackgroundRoute(t *testing.T) {
	router := NewRouter(RouterOptions{TenantAPIKey: "s3cret"})
	router.Handle(http.MethodPost, BankPath(SuffixBackground), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	service := router.Service()

	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/background", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want the retirement status", rec.Code)
	}
}

// The retired profile routes reach the engine, so a configured credential is
// examined before their handler runs: an unauthenticated caller is refused
// rather than told the route is retired.
func TestTenantAPIKeyGatesRetiredProfileRoutes(t *testing.T) {
	router := NewRouter(RouterOptions{TenantAPIKey: "s3cret"})
	router.Handle(http.MethodGet, BankPath(SuffixProfile), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	service := router.Service()

	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/profile", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without a credential = %d, want 401", rec.Code)
	}

	authed := httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/profile", nil)
	authed.Header.Set("Authorization", "Bearer s3cret")
	authedRec := httptest.NewRecorder()
	service.ServeHTTP(authedRec, authed)
	if authedRec.Code != http.StatusGone {
		t.Fatalf("status with a credential = %d, want the retirement status", authedRec.Code)
	}
}

// Without a key nothing is authenticated, which is the reference's default.
func TestTenantAPIKeyAbsentLeavesRoutesOpen(t *testing.T) {
	router := NewRouter(RouterOptions{})
	router.Handle(http.MethodGet, "/v1/default/banks", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// The legacy MCP token replaces tenant auth for the MCP namespace, with the
// reference's two refusals: a missing header and a wrong token are different
// sentences.
func TestMCPAuthTokenReplacesTenantAuthForMCP(t *testing.T) {
	handler := TenantAuthMiddleware("tenant-key", false, "mcp-token", nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	// t-mcp-auth-error-envelope: the refusal carries the protocol's "error"
	// member, not the shared envelope's "detail" one, and the two messages stay
	// distinct.
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	assertMCPRefusal(t, missing, DetailMCPAuthorizationRequired)

	wrong := httptest.NewRecorder()
	wrongReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	wrongReq.Header.Set("Authorization", "Bearer tenant-key")
	handler.ServeHTTP(wrong, wrongReq)
	assertMCPRefusal(t, wrong, DetailMCPInvalidToken)

	ok := httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	okReq.Header.Set("Authorization", "Bearer mcp-token")
	handler.ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("the legacy token was rejected: %d", ok.Code)
	}

	// The engine routes keep the tenant key: the legacy token covers MCP only.
	// Their refusal keeps the shared envelope, so the MCP shape does not leak
	// into an ordinary route.
	engine := httptest.NewRecorder()
	engineReq := httptest.NewRequest(http.MethodGet, PrefixDefaultV1+"/banks", nil)
	engineReq.Header.Set("Authorization", "Bearer mcp-token")
	handler.ServeHTTP(engine, engineReq)
	if engine.Code != http.StatusUnauthorized {
		t.Fatalf("the legacy token opened an engine route: %d", engine.Code)
	}
	var shared ErrorEnvelope
	if err := json.Unmarshal(engine.Body.Bytes(), &shared); err != nil {
		t.Fatalf("engine refusal is not the shared envelope: %v (%s)", err, engine.Body.String())
	}
	if shared.Detail != DetailAuthenticationFailed {
		t.Fatalf("engine refusal detail = %q, want %q", shared.Detail, DetailAuthenticationFailed)
	}
}

// assertMCPRefusal checks one MCP credential refusal: the status, the message,
// and that the body carries exactly one member, named for the protocol.
func assertMCPRefusal(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a JSON object: %v (%s)", err, rec.Body.String())
	}
	if len(body) != 1 {
		t.Fatalf("body members = %v, want exactly one", body)
	}
	if got := body["error"]; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if _, present := body["detail"]; present {
		t.Fatalf("the MCP refusal must not carry the shared detail member: %s", rec.Body.String())
	}
}

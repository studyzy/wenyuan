package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

type stubTenantExtension struct {
	contexts map[string]tenant.Context
	down     bool
}

func (s *stubTenantExtension) Authenticate(_ context.Context, request tenant.Request) (tenant.Context, error) {
	if s.down {
		return tenant.Context{}, errors.New("registry unavailable")
	}
	resolved, ok := s.contexts[request.Credential]
	if !ok {
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "unknown"}
	}
	return resolved, nil
}

func (s *stubTenantExtension) ListTenants(context.Context) ([]tenant.Tenant, error) {
	return nil, nil
}

func TestTenantExtensionAuthSetsSchemaAndOverrides(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"good": {ID: "acme", Schema: "tenant_acme", Overrides: map[string]any{"retain_chunk_size": 200}},
	}}
	router := NewRouter(RouterOptions{TenantExtension: extension})
	var seenSchema string
	var seenOverrides any
	router.Handle("GET", "/v1/default/banks", func(w http.ResponseWriter, r *http.Request) {
		seenSchema, _ = storage.SchemaFrom(r.Context())
		seenOverrides = TenantOverridesFrom(r)["retain_chunk_size"]
		w.WriteHeader(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil)
	request.Header.Set("Authorization", "Bearer good")
	recorder := httptest.NewRecorder()
	router.Service().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if seenSchema != "tenant_acme" {
		t.Fatalf("schema = %q, want tenant_acme", seenSchema)
	}
	if seenOverrides != 200 {
		t.Fatalf("overrides = %+v", seenOverrides)
	}
}

func TestTenantExtensionAuthRejects(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{"good": {Schema: "tenant_acme"}}}
	for _, tc := range []struct {
		name   string
		status int
		ext    *stubTenantExtension
		header string
	}{
		{name: "missing", status: http.StatusUnauthorized, ext: extension},
		{name: "unknown", status: http.StatusUnauthorized, ext: extension, header: "Bearer bad"},
		{name: "registry down", status: http.StatusServiceUnavailable, ext: &stubTenantExtension{down: true}, header: "Bearer good"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter(RouterOptions{TenantExtension: tc.ext})
			called := false
			router.Handle("GET", "/v1/default/banks", func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil)
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			recorder := httptest.NewRecorder()
			router.Service().ServeHTTP(recorder, request)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
			if called {
				t.Fatal("handler ran for a rejected request")
			}
		})
	}
}

func TestTenantExtensionAuthLeavesUnprotectedRoutesOpen(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{}}
	router := NewRouter(RouterOptions{TenantExtension: extension})
	called := false
	router.Handle("GET", "/health", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	recorder := httptest.NewRecorder()
	router.Service().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !called || recorder.Code != http.StatusOK {
		t.Fatalf("health route was gated: status=%d called=%v", recorder.Code, called)
	}
}

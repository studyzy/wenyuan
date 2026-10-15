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

// The routes the reference answers before reaching the engine keep their own
// status even when a credential is configured.
func TestTenantAPIKeyDoesNotGateRetiredRoutes(t *testing.T) {
	router := NewRouter(RouterOptions{TenantAPIKey: "s3cret"})
	router.Handle(http.MethodGet, BankPath(SuffixProfile), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	service := router.Service()

	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/profile", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want the retirement status", rec.Code)
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

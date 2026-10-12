package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

func testConfig() config.Config {
	return config.FromEnv(func(string) (string, bool) { return "", false })
}

func TestComposesServedApplication(t *testing.T) {
	cfg := testConfig()
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if application.Handler == nil {
		t.Fatal("composed application must expose a handler")
	}
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("version status = %d", rec.Code)
	}
}

// ts-0033: the application entrypoint resolves to the same served application
// that answers the public routes.
func TestEntrypointResolvesToServedApplication(t *testing.T) {
	t.Setenv(config.EnvRunMigrationsOnStartup, "false")
	t.Setenv(config.EnvDatabaseURL, "")
	application, err := Application.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if application.Handler == nil {
		t.Fatal("the import target must resolve to a served application")
	}
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", rec.Code)
	}
}

// ts-0013 / tmg-0002: a startup on an already-bound address fails before
// initialization and exits non-zero with a bind error.
func TestAlreadyBoundAddressFailsBeforeInitialization(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().String()

	host, port := splitHostPort(t, address)
	cfg := testConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.BindGrace = 0

	store := storage.NewMemoryStore("public")
	_, err = New(context.Background(), cfg, Dependencies{Store: store})
	if err == nil {
		t.Fatal("composition must fail on an already-bound address")
	}
	if store.Migrated() {
		t.Fatal("initialization must not run when the address cannot be bound")
	}
}

func TestMigrationsRunBeforeServing(t *testing.T) {
	cfg := testConfig()
	cfg.RunMigrationsOnStartup = true
	store := storage.NewMemoryStore("public")
	if _, err := New(context.Background(), cfg, Dependencies{Store: store}); err != nil {
		t.Fatal(err)
	}
	if !store.Migrated() {
		t.Fatal("migrations must run before the application serves traffic")
	}
}

func TestRouteInventoryIsMounted(t *testing.T) {
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	cfg.MCPEnabled = true
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodGet, "/health/ready", http.StatusOK},
		{http.MethodGet, "/health/live", http.StatusOK},
		{http.MethodGet, "/version", http.StatusOK},
		{http.MethodGet, "/metrics", http.StatusOK},
		{http.MethodGet, "/v1/default/banks", http.StatusOK},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		application.Handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Fatalf("%s %s status = %d, want %d (body %s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// ts-0017 / neg-0002: the configuration read is always available while the
// write is gated by HINDSIGHT_API_ENABLE_BANK_CONFIG_API.
func TestBankConfigGateAtCompositionRoot(t *testing.T) {
	cfg := testConfig()
	cfg.EnableBankConfigAPI = false
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("listing status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/default/banks/b1", strings.NewReader(`{"name":"B"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The read route answers even when the gate is off.
	rec = httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("config read must be served regardless of the gate: %d", rec.Code)
	}
	// The write route is not served when the gate is off.
	rec = httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/default/banks/b1/config", strings.NewReader(`{"updates":{}}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("config write must be gated off")
	}
}

func TestVersionFeatureMapReflectsConfig(t *testing.T) {
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	var body struct {
		APIVersion string          `json:"api_version"`
		Features   map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Features["bank_config_api"] {
		t.Fatalf("feature map must reflect the gate: %v", body.Features)
	}
}

func splitHostPort(t *testing.T, address string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	return host, port
}

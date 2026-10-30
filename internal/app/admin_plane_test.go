package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

func TestNewAdminHandlerServesWithMemoryRegistry(t *testing.T) {
	store := storage.NewMemoryStore("public")
	registry, err := newTenantRegistry(context.Background(), config.Config{}, store)
	if err != nil {
		t.Fatalf("newTenantRegistry: %v", err)
	}
	handler, err := newAdminHandler(config.Config{AdminAPIKey: "operator-secret"}, registry, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newAdminHandler: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/tenants", nil)
	request.Header.Set("Authorization", "Bearer operator-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestMultiTenantDataPlaneUsesRegistry(t *testing.T) {
	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	cfg.AdminListen = "127.0.0.1:18890"
	cfg.AdminAPIKey = "operator-secret"
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.AdminHandler == nil {
		t.Fatal("admin handler is nil in multi mode with an admin listener")
	}

	create := httptest.NewRequest(http.MethodPost, "/admin/v1/tenants", strings.NewReader("{\"tenant_id\":\"acme\"}"))
	create.Header.Set("Authorization", "Bearer operator-secret")
	create.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	application.AdminHandler.ServeHTTP(recorder, create)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d: %s", recorder.Code, recorder.Body.String())
	}

	issue := httptest.NewRequest(http.MethodPost, "/admin/v1/tenants/acme/keys", strings.NewReader("{\"label\":\"ci\"}"))
	issue.Header.Set("Authorization", "Bearer operator-secret")
	issue.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	application.AdminHandler.ServeHTTP(recorder, issue)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create key = %d: %s", recorder.Code, recorder.Body.String())
	}
	var issued struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode key: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil)
	request.Header.Set("Authorization", "Bearer "+issued.Plaintext)
	recorder = httptest.NewRecorder()
	application.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("data plane with tenant key = %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	application.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("data plane without a key = %d, want 401", recorder.Code)
	}
}

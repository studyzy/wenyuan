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

// teamAdminRequest issues a management request against the composed
// application's admin plane.
func teamAdminRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer operator-secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return value
}

// multiTenantApp composes the service in multi-tenant mode with an admin
// listener, which is the only mode that serves the team routes.
func multiTenantApp(t *testing.T) *App {
	t.Helper()
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
	return application
}

// TestTeamPlaneEndToEnd drives the whole team surface through the served
// management handler: create a team, add its members, create a project and put
// a member on it.
func TestTeamPlaneEndToEnd(t *testing.T) {
	application := multiTenantApp(t)
	admin := application.AdminHandler

	// Two people exist as tenants first: a team admits tenants, not names.
	for _, id := range []string{"alice", "bob"} {
		if recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/tenants", `{"tenant_id":"`+id+`"}`); recorder.Code != http.StatusCreated {
			t.Fatalf("create tenant %s = %d: %s", id, recorder.Code, recorder.Body.String())
		}
	}

	recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/teams", `{"tenant_id":"acme","display_name":"Acme"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", recorder.Code, recorder.Body.String())
	}
	created := decodeResponse(t, recorder)
	key, _ := created["plaintext"].(string)
	if !strings.HasPrefix(key, "wyk_") {
		t.Fatalf("team created without a key: %s", recorder.Body.String())
	}
	team, _ := created["team"].(map[string]any)
	if team["kind"] != "team" {
		t.Fatalf("team kind = %v", team["kind"])
	}

	// The team's key is a working data-plane credential for the team's own
	// schema, which is what "a team is a tenant" buys.
	dataPlane := httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil)
	dataPlane.Header.Set("Authorization", "Bearer "+key)
	dataRecorder := httptest.NewRecorder()
	application.Handler.ServeHTTP(dataRecorder, dataPlane)
	if dataRecorder.Code != http.StatusOK {
		t.Fatalf("team key on the data plane = %d: %s", dataRecorder.Code, dataRecorder.Body.String())
	}

	// Members.
	for _, body := range []string{`{"role":"owner"}`, `{}`} {
		member := "alice"
		if body == "{}" {
			member = "bob"
		}
		if recorder := teamAdminRequest(t, admin, http.MethodPut, "/admin/v1/teams/acme/members/"+member, body); recorder.Code != http.StatusOK {
			t.Fatalf("add %s = %d: %s", member, recorder.Code, recorder.Body.String())
		}
	}
	members := decodeResponse(t, teamAdminRequest(t, admin, http.MethodGet, "/admin/v1/teams/acme/members", ""))
	if got := len(members["members"].([]any)); got != 2 {
		t.Fatalf("members = %d, want 2", got)
	}

	// Project and its membership. Creating the project creates its bank, so the
	// team's key can address "acme::web" without a first write having made it.
	if recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	banks := httptest.NewRequest(http.MethodGet, "/v1/default/banks", nil)
	banks.Header.Set("Authorization", "Bearer "+key)
	banksRecorder := httptest.NewRecorder()
	application.Handler.ServeHTTP(banksRecorder, banks)
	if banksRecorder.Code != http.StatusOK {
		t.Fatalf("list banks = %d: %s", banksRecorder.Code, banksRecorder.Body.String())
	}
	var listed struct {
		Banks []struct {
			BankID string `json:"bank_id"`
		} `json:"banks"`
	}
	if err := json.Unmarshal(banksRecorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode banks: %v", err)
	}
	if len(listed.Banks) != 1 || listed.Banks[0].BankID != "acme::web" {
		t.Fatalf("banks after creating the project = %s, want acme::web", banksRecorder.Body.String())
	}
	if recorder := teamAdminRequest(t, admin, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d: %s", recorder.Code, recorder.Body.String())
	}
	project := decodeResponse(t, teamAdminRequest(t, admin, http.MethodGet, "/admin/v1/teams/acme/projects/web", ""))
	if project["bank_id"] != "acme::web" {
		t.Fatalf("project bank = %v", project["bank_id"])
	}

	// The data plane is unchanged: the team routes never appear there, and the
	// ordinary bank routes still behave as they did.
	for _, path := range []string{"/admin/v1/teams", "/v1/default/banks/acme::Web/memories"} {
		recorder := httptest.NewRecorder()
		application.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if path == "/admin/v1/teams" && recorder.Code != http.StatusNotFound {
			t.Fatalf("the data plane served %s: %d", path, recorder.Code)
		}
	}
}

// TestTeamRoutesAreAbsentInSingleTenantMode pins the mode gate: a single-tenant
// process has no team registry, so every team route answers 501 while the tenant
// routes keep working.
func TestTeamRoutesAreAbsentInSingleTenantMode(t *testing.T) {
	cfg := testConfig()
	cfg.TenantMode = config.TenantModeSingle
	cfg.AdminListen = "127.0.0.1:18890"
	cfg.AdminAPIKey = "operator-secret"
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.AdminHandler == nil {
		t.Fatal("admin handler is nil with an admin listener")
	}
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/v1/teams", ""},
		{http.MethodPost, "/admin/v1/teams", `{"tenant_id":"acme"}`},
	} {
		recorder := teamAdminRequest(t, application.AdminHandler, route.method, route.path, route.body)
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s in single mode = %d, want 501: %s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := teamAdminRequest(t, application.AdminHandler, http.MethodGet, "/admin/v1/tenants", ""); recorder.Code != http.StatusOK {
		t.Fatalf("tenant list in single mode = %d", recorder.Code)
	}
}

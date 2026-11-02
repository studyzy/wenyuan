package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// A Hindsight-compatible coding-agent client addresses its memory bank as
// "coding-agent::<project>", the harness name rather than a tenant. The served
// data plane must serve that id as the bank of the tenant the request key
// belongs to, end to end through the registry: the write lands in alice schema
// under alice bank, not under a bank named after the harness.
func TestCodingAgentBankIDResolvesToTenant(t *testing.T) {
	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	cfg.AdminListen = "127.0.0.1:18892"
	cfg.AdminAPIKey = "operator-secret"
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	admin := application.AdminHandler
	if recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/tenants", `{"tenant_id":"alice"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d: %s", recorder.Code, recorder.Body.String())
	}
	created := decodeResponse(t, teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/tenants/alice/keys", `{}`))
	key, _ := created["plaintext"].(string)
	if key == "" {
		t.Fatalf("tenant got no key: %v", created)
	}

	dataPlane := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		application.Handler.ServeHTTP(recorder, request)
		return recorder
	}

	// The client retains through the id it was configured with; the colon pair
	// is percent-encoded the way the coding-agent client encodes a bank id.
	if recorder := dataPlane(http.MethodPost,
		"/v1/default/banks/coding-agent%3A%3Awenyuan/memories",
		`{"items":[{"content":"Alice moved to Berlin."}]}`); recorder.Code != http.StatusOK {
		t.Fatalf("retain through the harness id = %d: %s", recorder.Code, recorder.Body.String())
	}

	// Reading through the same id answers the tenant bank.
	configRecorder := dataPlane(http.MethodGet, "/v1/default/banks/coding-agent::wenyuan/config", "")
	if configRecorder.Code != http.StatusOK {
		t.Fatalf("read through the harness id = %d: %s", configRecorder.Code, configRecorder.Body.String())
	}
	if profile := decodeResponse(t, configRecorder); profile["bank_id"] != "alice::wenyuan" {
		t.Fatalf("bank_id = %v, want alice::wenyuan", profile["bank_id"])
	}

	// The collection holds the tenant-prefixed id, never the harness one.
	list := decodeResponse(t, dataPlane(http.MethodGet, "/v1/default/banks", ""))
	rows, _ := list["banks"].([]any)
	var ids []string
	for _, row := range rows {
		if object, ok := row.(map[string]any); ok {
			id, _ := object["bank_id"].(string)
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 || ids[0] != "alice::wenyuan" {
		t.Fatalf("bank ids = %v, want [alice::wenyuan]", ids)
	}
}

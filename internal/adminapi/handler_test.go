package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

const testOperatorKey = "operator-secret"

func newTestHandler(t *testing.T) (*Handler, *storage.MemoryTenantRegistry) {
	t.Helper()
	registry := storage.NewMemoryTenantRegistry()
	handler, err := New(Config{Registry: registry, OperatorKey: testOperatorKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return handler, registry
}

func adminRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testOperatorKey)
	return request
}

func serve(t *testing.T, handler *Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeTenant(t *testing.T, recorder *httptest.ResponseRecorder) tenantResponse {
	t.Helper()
	var response tenantResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode tenant: %v: %s", err, recorder.Body.String())
	}
	return response
}

func TestAdminTenantCRUD(t *testing.T) {
	handler, _ := newTestHandler(t)

	created := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants",
		"{\"tenant_id\":\"acme\",\"display_name\":\"Acme\",\"config\":{\"enable_text_search\":false}}"))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	record := decodeTenant(t, created)
	if record.TenantID != "acme" || record.Status != "active" {
		t.Fatalf("created = %+v", record)
	}

	got := serve(t, handler, adminRequest(http.MethodGet, "/admin/v1/tenants/acme", ""))
	if got.Code != http.StatusOK {
		t.Fatalf("get status = %d", got.Code)
	}
	if decoded := decodeTenant(t, got); decoded.DisplayName != "Acme" {
		t.Fatalf("get = %+v", decoded)
	}

	patched := serve(t, handler, adminRequest(http.MethodPatch, "/admin/v1/tenants/acme",
		"{\"display_name\":\"Acme Inc\",\"status\":\"suspended\"}"))
	if patched.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", patched.Code, patched.Body.String())
	}
	if decoded := decodeTenant(t, patched); decoded.DisplayName != "Acme Inc" || decoded.Status != "suspended" {
		t.Fatalf("patched = %+v", decoded)
	}

	list := serve(t, handler, adminRequest(http.MethodGet, "/admin/v1/tenants?status=suspended", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	var listed struct {
		Tenants []tenantResponse `json:"tenants"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Tenants) != 1 || listed.Tenants[0].TenantID != "acme" {
		t.Fatalf("list = %+v", listed)
	}

	deleted := serve(t, handler, adminRequest(http.MethodDelete, "/admin/v1/tenants/acme", ""))
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d", deleted.Code)
	}
	if decoded := decodeTenant(t, deleted); decoded.Status != "deleted" || decoded.DeletedAt == nil {
		t.Fatalf("deleted = %+v", decoded)
	}
}

func TestAdminAuth(t *testing.T) {
	handler, _ := newTestHandler(t)
	for _, tc := range []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", header: "", want: http.StatusUnauthorized},
		{name: "wrong", header: "Bearer nope", want: http.StatusUnauthorized},
		{name: "correct", header: "Bearer " + testOperatorKey, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/admin/v1/tenants", nil)
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			if got := serve(t, handler, request).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAdminCreateValidation(t *testing.T) {
	handler, _ := newTestHandler(t)
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants",
		"{\"tenant_id\":\"Admin\"}")).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d, want 400", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants",
		"{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create status = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants",
		"{\"tenant_id\":\"acme\"}")).Code; got != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", got)
	}
}

func TestAdminKeys(t *testing.T) {
	handler, _ := newTestHandler(t)
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants",
		"{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create tenant status = %d", got)
	}

	created := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/keys",
		"{\"label\":\"ci\"}"))
	if created.Code != http.StatusCreated {
		t.Fatalf("create key status = %d: %s", created.Code, created.Body.String())
	}
	var issued struct {
		Plaintext string      `json:"plaintext"`
		Key       keyResponse `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode key: %v", err)
	}
	if !strings.HasPrefix(issued.Plaintext, "wyk_") {
		t.Fatalf("plaintext = %q", issued.Plaintext)
	}
	if issued.Key.Label != "ci" || issued.Key.Prefix == "" {
		t.Fatalf("key = %+v", issued.Key)
	}

	list := serve(t, handler, adminRequest(http.MethodGet, "/admin/v1/tenants/acme/keys", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list keys status = %d", list.Code)
	}
	if strings.Contains(list.Body.String(), issued.Plaintext) || strings.Contains(list.Body.String(), "hash") {
		t.Fatalf("key list leaked the credential: %s", list.Body.String())
	}

	if got := serve(t, handler, adminRequest(http.MethodDelete,
		"/admin/v1/tenants/acme/keys/"+issued.Key.KeyID, "")).Code; got != http.StatusOK {
		t.Fatalf("revoke status = %d", got)
	}
}

func TestAdminSessionKey(t *testing.T) {
	handler, _ := newTestHandler(t)
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants",
		"{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create tenant status = %d", got)
	}
	created := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/session", "{}"))
	if created.Code != http.StatusCreated {
		t.Fatalf("session status = %d: %s", created.Code, created.Body.String())
	}
	var issued struct {
		Plaintext string      `json:"plaintext"`
		Key       keyResponse `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !strings.HasPrefix(issued.Plaintext, "wyk_") {
		t.Fatalf("plaintext = %q", issued.Plaintext)
	}
	if !issued.Key.Ephemeral {
		t.Fatal("session key is not ephemeral")
	}
	if issued.Key.Label != "console:operator" {
		t.Fatalf("label = %q", issued.Key.Label)
	}
	if issued.Key.ExpiresAt == nil {
		t.Fatal("session key has no expiry")
	}
}

func TestAdminOverview(t *testing.T) {
	handler, _ := newTestHandler(t)
	for _, body := range []string{"{\"tenant_id\":\"acme\"}", "{\"tenant_id\":\"beta\"}"} {
		if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", body)).Code; got != http.StatusCreated {
			t.Fatalf("create tenant status = %d", got)
		}
	}
	overview := serve(t, handler, adminRequest(http.MethodGet, "/admin/v1/overview", ""))
	if overview.Code != http.StatusOK {
		t.Fatalf("overview status = %d", overview.Code)
	}
	var counts map[string]int
	if err := json.Unmarshal(overview.Body.Bytes(), &counts); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if counts["total"] != 2 || counts["active"] != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestAdminCreateTenantAdoptConflict(t *testing.T) {
	registry := storage.NewMemoryTenantRegistry()
	handler, err := New(Config{
		Registry:    registry,
		OperatorKey: testOperatorKey,
		Provisioner: func(context.Context, string, bool) error { return storage.ErrSchemaExists },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", "{\"tenant_id\":\"acme\"}"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminPurge(t *testing.T) {
	registry := storage.NewMemoryTenantRegistry()
	dropped := ""
	handler, err := New(Config{
		Registry:    registry,
		OperatorKey: testOperatorKey,
		Dropper: func(_ context.Context, tenantID string) error {
			dropped = tenantID
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", "{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodDelete, "/admin/v1/tenants/acme", "")).Code; got != http.StatusOK {
		t.Fatalf("delete = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/purge", "{\"confirm\":\"other\"}")).Code; got != http.StatusBadRequest {
		t.Fatalf("mismatched confirm = %d, want 400", got)
	}
	if dropped != "" {
		t.Fatal("purge ran with a mismatched confirmation")
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/purge", "{\"confirm\":\"acme\"}")).Code; got != http.StatusOK {
		t.Fatalf("purge = %d", got)
	}
	if dropped != "acme" {
		t.Fatalf("dropped = %q, want acme", dropped)
	}
}

func TestAdminPurgeRequiresDeleted(t *testing.T) {
	registry := storage.NewMemoryTenantRegistry()
	handler, err := New(Config{
		Registry:    registry,
		OperatorKey: testOperatorKey,
		Dropper:     func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", "{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/purge", "{\"confirm\":\"acme\"}")).Code; got != http.StatusConflict {
		t.Fatalf("purge active = %d, want 409", got)
	}
}

func TestAdminPurgeNotConfigured(t *testing.T) {
	handler, _ := newTestHandler(t)
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", "{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodDelete, "/admin/v1/tenants/acme", "")).Code; got != http.StatusOK {
		t.Fatalf("delete = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/purge", "{\"confirm\":\"acme\"}")).Code; got != http.StatusNotImplemented {
		t.Fatalf("unconfigured purge = %d, want 501", got)
	}
}

func TestAdminMigrate(t *testing.T) {
	registry := storage.NewMemoryTenantRegistry()
	migrated := ""
	handler, err := New(Config{
		Registry:    registry,
		OperatorKey: testOperatorKey,
		Migrator: func(_ context.Context, tenantID string) error {
			migrated = tenantID
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", "{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/migrate", "{}")).Code; got != http.StatusOK {
		t.Fatalf("migrate = %d", got)
	}
	if migrated != "acme" {
		t.Fatalf("migrated = %q, want acme", migrated)
	}
}

func TestAdminMigrateRejectsDeleted(t *testing.T) {
	registry := storage.NewMemoryTenantRegistry()
	handler, err := New(Config{
		Registry:    registry,
		OperatorKey: testOperatorKey,
		Migrator:    func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants", "{\"tenant_id\":\"acme\"}")).Code; got != http.StatusCreated {
		t.Fatalf("create = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodDelete, "/admin/v1/tenants/acme", "")).Code; got != http.StatusOK {
		t.Fatalf("delete = %d", got)
	}
	if got := serve(t, handler, adminRequest(http.MethodPost, "/admin/v1/tenants/acme/migrate", "{}")).Code; got != http.StatusConflict {
		t.Fatalf("migrate deleted = %d, want 409", got)
	}
}

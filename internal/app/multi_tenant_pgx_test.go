//go:build pgx

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

func adminCall(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer operator-secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func issueTenantKey(t *testing.T, handler http.Handler, tenantID string) string {
	t.Helper()
	created := adminCall(t, handler, http.MethodPost, "/admin/v1/tenants",
		fmt.Sprintf("{\"tenant_id\":%q}", tenantID))
	if created.Code != http.StatusCreated {
		t.Fatalf("create tenant %s = %d: %s", tenantID, created.Code, created.Body.String())
	}
	issued := adminCall(t, handler, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/keys",
		"{\"label\":\"e2e\"}")
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue key %s = %d: %s", tenantID, issued.Code, issued.Body.String())
	}
	var payload struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode key: %v", err)
	}
	return payload.Plaintext
}

func dataPlaneCall(t *testing.T, handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestPostgresMultiTenantEndToEnd(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the multi-tenant e2e test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(url, storage.PoolBudget{MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	tenantA := fmt.Sprintf("t_a_%d", suffix)
	tenantB := fmt.Sprintf("t_b_%d", suffix)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		for _, schema := range []string{tenantA, tenantB, "admin"} {
			_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS \""+schema+"\" CASCADE")
		}
		_ = db.Close()
	})

	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	cfg.AdminListen = "127.0.0.1:0"
	cfg.AdminAPIKey = "operator-secret"
	cfg.RunMigrationsOnStartup = true
	store := storage.NewMultiTenantPostgresStore(db, "public")
	application, err := New(ctx, cfg, Dependencies{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	keyA := issueTenantKey(t, application.AdminHandler, tenantA)
	keyB := issueTenantKey(t, application.AdminHandler, tenantB)

	for name, key := range map[string]string{"A": keyA, "B": keyB} {
		recorder := dataPlaneCall(t, application.Handler, http.MethodPut, "/v1/default/banks/shared", key,
			"{\"name\":\""+name+"\"}")
		if recorder.Code != http.StatusOK {
			t.Fatalf("create bank for %s = %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}

	for name, key := range map[string]string{"A": keyA, "B": keyB} {
		recorder := dataPlaneCall(t, application.Handler, http.MethodGet, "/v1/default/banks", key, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("list banks for %s = %d: %s", name, recorder.Code, recorder.Body.String())
		}
		var page struct {
			Banks []struct {
				BankID string `json:"bank_id"`
				Name   string `json:"name"`
			} `json:"banks"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode banks for %s: %v", name, err)
		}
		if len(page.Banks) != 1 || page.Banks[0].BankID != "shared" || page.Banks[0].Name != name {
			t.Fatalf("%s sees %+v, want only shared/%s", name, page.Banks, name)
		}
	}
}

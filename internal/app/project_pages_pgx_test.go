//go:build pgx

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// TestPostgresProjectBankSeedsKnowledgePages is the seeded-pages counterpart of
// TestPostgresTeamProjectCreatesItsBank, and it exists because the in-memory
// suite cannot see the defect it pins: the management plane's request context
// carries no tenant schema, and the in-memory backend never asks for one. Only
// a real database turns the missing schema into a failure — which it did, on a
// running deployment, while every offline test was green.
func TestPostgresProjectBankSeedsKnowledgePages(t *testing.T) {
	dsn := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the seeded pages e2e test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(dsn, storage.PoolBudget{MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	teamID := fmt.Sprintf("t_pages_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		for _, schema := range []string{teamID, "admin"} {
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

	created := adminCall(t, application.AdminHandler, http.MethodPost, "/admin/v1/teams",
		fmt.Sprintf("{\"tenant_id\":%q}", teamID))
	if created.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", created.Code, created.Body.String())
	}
	var issued struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode team: %v", err)
	}

	project := adminCall(t, application.AdminHandler, http.MethodPost,
		"/admin/v1/teams/"+teamID+"/projects", `{"project_id":"web","name":"Web"}`)
	if project.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", project.Code, project.Body.String())
	}
	bankID := teamID + "::web"

	// The rows are in the team's schema: five nodes, each page backed by its own
	// mental model and its own queued first refresh.
	var nodes, models, queued int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "`+teamID+`".knowledge_pages WHERE bank_id = $1`, bankID).Scan(&nodes); err != nil {
		t.Fatalf("count the knowledge nodes: %v", err)
	}
	if nodes != 5 {
		t.Fatalf("knowledge nodes = %d, want the taxonomy's 5", nodes)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "`+teamID+`".mental_models WHERE bank_id = $1`, bankID).Scan(&models); err != nil {
		t.Fatalf("count the mental models: %v", err)
	}
	if models != 5 {
		t.Fatalf("mental models = %d, want 5", models)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "`+teamID+`".async_operations
		 WHERE bank_id = $1 AND operation_type = 'refresh_mental_model'`, bankID).Scan(&queued); err != nil {
		t.Fatalf("count the queued refreshes: %v", err)
	}
	if queued != 5 {
		t.Fatalf("queued refreshes = %d, want one per page", queued)
	}

	// And the data plane serves them: the tree a console reads lists the five by
	// name, so this is the surface the operator was looking at.
	tree := dataPlaneCall(t, application.Handler, http.MethodGet,
		"/v1/default/banks/"+bankID+"/knowledge-base/tree", issued.Plaintext, "")
	if tree.Code != http.StatusOK {
		t.Fatalf("knowledge tree = %d: %s", tree.Code, tree.Body.String())
	}
	var payload struct {
		Roots []struct {
			Name string `json:"name"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(tree.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	names := make(map[string]bool, len(payload.Roots))
	for _, root := range payload.Roots {
		names[root.Name] = true
	}
	for _, want := range []string{
		"Component map", "Core concepts", "Conventions and patterns",
		"Key decisions and rationale", "Initiatives and enhancements",
	} {
		if !names[want] {
			t.Errorf("the project bank has no %q page: %s", want, tree.Body.String())
		}
	}
}

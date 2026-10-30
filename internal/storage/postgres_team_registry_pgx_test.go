//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// TestPostgresTeamRegistry runs the shared TeamRegistry suite against
// PostgreSQL, so the two backends are held to one contract.
func TestPostgresTeamRegistry(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the team registry test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("admin_team_e2e_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		_ = db.Close()
	})
	if err := migrateAdminSchema(ctx, db, schema); err != nil {
		t.Fatalf("migrate admin schema: %v", err)
	}

	registry := &PostgresTenantRegistry{db: db, schema: schema}
	runTeamRegistrySuite(t, teamRegistryFixture{teams: registry, tenants: registry})
}

// TestPostgresTeamRegistrySurvivesTenantDeletion proves the cascade the
// migration declares: dropping the tenant row takes its team rows with it, so a
// purged team never leaves membership behind.
func TestPostgresTeamRegistrySurvivesTenantDeletion(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the team registry test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("admin_team_cascade_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		_ = db.Close()
	})
	if err := migrateAdminSchema(ctx, db, schema); err != nil {
		t.Fatalf("migrate admin schema: %v", err)
	}
	registry := &PostgresTenantRegistry{db: db, schema: schema}
	fixture := teamRegistryFixture{teams: registry, tenants: registry}
	seedTenant(t, fixture, "acme", "team")
	seedTenant(t, fixture, "alice", "personal")
	if err := registry.AddTeamMember(ctx, "acme", "alice", "member"); err != nil {
		t.Fatalf("AddTeamMember: %v", err)
	}
	if err := registry.CreateTeamProject(ctx, tenant.TeamProject{TeamID: "acme", ProjectID: "web", Name: "Web"}); err != nil {
		t.Fatalf("CreateTeamProject: %v", err)
	}
	if err := registry.AddProjectMember(ctx, "acme", "web", "alice"); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM "+quoteIdent(schema)+".tenant WHERE tenant_id = 'acme'"); err != nil {
		t.Fatalf("delete team tenant: %v", err)
	}
	for _, table := range []string{"team_member", "team_project", "team_project_member"} {
		var remaining int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(schema)+"."+table).Scan(&remaining); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if remaining != 0 {
			t.Fatalf("%s still holds %d rows after the team tenant was deleted", table, remaining)
		}
	}
}

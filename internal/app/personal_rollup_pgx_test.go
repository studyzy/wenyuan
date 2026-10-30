//go:build pgx

package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The personal rollup job against a real database.
//
// The job's own decisions are pinned by personal_rollup_test.go with a fake store.
// What only PostgreSQL can show is that one round reads every bank of a person's
// schema and writes the aggregate bank, its ledger and the per-bank watermarks
// together, and that the project merge never folds an aggregate into a team.

// rollupJobFixture is one person's schema with a few banks, plus the admin ledger.
type rollupJobFixture struct {
	db      storage.DB
	persona string
	store   storage.PersonalStore
}

// newRollupJobFixture provisions a personal tenant.
func newRollupJobFixture(t *testing.T) *rollupJobFixture {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the aggregate job integration test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(url, storage.PoolBudget{MaxOpenConns: 8, MaxIdleConns: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &rollupJobFixture{db: db, persona: "agg_job_" + suffix}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoteIdentForTest(f.persona)+" CASCADE")
		_, _ = db.ExecContext(ctx, "DELETE FROM admin.tenant WHERE tenant_id = $1", f.persona)
		_ = db.Close()
	})
	ctx := context.Background()
	if err := storage.MigrateAdminSchemaFor(ctx, db, false); err != nil {
		t.Fatalf("admin migrations: %v", err)
	}
	if err := storage.ProvisionTenantSchema(ctx, db, f.persona, false, "vector"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO admin.tenant (tenant_id, display_name, status, kind) VALUES ($1,$1,'active','personal')",
		f.persona); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	f.store = storage.NewPostgresPersonalStore(db)
	return f
}

// ctx is the fixture's tenant context.
func (f *rollupJobFixture) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), f.persona)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	return ctx
}

// bank creates one bank in the person's schema.
func (f *rollupJobFixture) bank(t *testing.T, name string) string {
	t.Helper()
	bankID := tenant.ProjectBankID(f.persona, name)
	if _, err := f.db.ExecContext(f.ctx(t), `
		INSERT INTO `+quoteIdentForTest(f.persona)+`.banks (bank_id, name, config) VALUES ($1,$2,'{}'::jsonb)
		ON CONFLICT DO NOTHING`, bankID, name); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	return bankID
}

// fact inserts one world fact into a bank.
func (f *rollupJobFixture) fact(t *testing.T, bankID, text string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRowContext(f.ctx(t), `
		INSERT INTO `+quoteIdentForTest(f.persona)+`.memory_units (bank_id, text, fact_type)
		VALUES ($1,$2,'world') RETURNING id::text`, bankID, text).Scan(&id); err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	return id
}

// aggregateFacts reads the aggregate bank's world facts.
func (f *rollupJobFixture) aggregateFacts(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.QueryContext(f.ctx(t), `
		SELECT text FROM `+quoteIdentForTest(f.persona)+`.memory_units
		WHERE bank_id = $1 AND fact_type='world' ORDER BY text`, tenant.PersonalBankID(f.persona))
	if err != nil {
		t.Fatalf("read aggregate: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, text)
	}
	return out
}

// TestPersonalRollupJobFoldsEveryBank drives the real job: two banks, one model
// call, and the aggregate bank plus its ledger and watermarks written together.
func TestPersonalRollupJobFoldsEveryBank(t *testing.T) {
	f := newRollupJobFixture(t)
	orders := f.bank(t, "orders")
	billing := f.bank(t, "billing")
	f.fact(t, orders, "orders deploys on port 15432")
	f.fact(t, billing, "billing stores cents")

	model := &answerProvider{name: "fake",
		reply: `{"facts":[{"text":"orders deploys on port 15432","sources":[0],"topics":["deploy"]},` +
			`{"text":"billing stores cents","sources":[1],"topics":["billing"]}]}`}
	runner := &personalRunner{store: f.store, models: func(string) provider.Provider { return model }, cfg: rollupTestCfg()}
	if err := runner.runPersonalRounds(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1 for the tenant", model.calls)
	}
	if got := f.aggregateFacts(t); len(got) != 2 {
		t.Fatalf("aggregate = %v, want both conclusions", got)
	}
	var ledger int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM admin.personal_source WHERE tenant_id=$1", f.persona).Scan(&ledger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if ledger != 2 {
		t.Fatalf("ledger rows = %d, want one per bank", ledger)
	}

	// The next round has nothing new: no model call, no write.
	model.calls = 0
	if err := runner.runPersonalRounds(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls on a quiet second round = %d, want 0", model.calls)
	}

	// A fact newly written into one bank is the only thing the next round reads,
	// and it reaches the same aggregate.
	f.fact(t, orders, "orders has a health endpoint")
	model.reply = `{"facts":[{"text":"orders deploys on port 15432","sources":[]},` +
		`{"text":"billing stores cents","sources":[]},` +
		`{"text":"orders has a health endpoint","sources":[0],"topics":["deploy"]}]}`
	if err := runner.runPersonalRounds(context.Background()); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if got := f.aggregateFacts(t); len(got) != 3 {
		t.Fatalf("aggregate after the new fact = %v, want three", got)
	}
}

// TestPersonalRollupJobSkipsATeamTenant pins the product decision end to end: a
// team is not a person, so the discovery never hands it to a round and no
// aggregate bank appears in its schema.
func TestPersonalRollupJobSkipsATeamTenant(t *testing.T) {
	f := newRollupJobFixture(t)
	teamID := f.persona + "_team"
	ctx := context.Background()
	if err := storage.ProvisionTenantSchema(ctx, f.db, teamID, false, "vector"); err != nil {
		t.Fatalf("provision team: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quoteIdentForTest(teamID)+" CASCADE")
		_, _ = f.db.ExecContext(context.Background(), "DELETE FROM admin.tenant WHERE tenant_id = $1", teamID)
	})
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO admin.tenant (tenant_id, display_name, status, kind) VALUES ($1,$1,'active','team')",
		teamID); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	tenants, err := f.store.ListRollupTenants(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, one := range tenants {
		if one.ID == teamID {
			t.Fatal("a team tenant was handed to the rollup")
		}
	}
}

// TestProjectMergeIgnoresTheAggregateBank pins the N4 exclusion: a project whose
// id is "myself" points at the member's aggregate bank, and the project merge must
// not fold that person's cross-project conclusions into the team's content.
func TestProjectMergeIgnoresTheAggregateBank(t *testing.T) {
	f := newMergeJobFixture(t)
	ctx := context.Background()
	projectID := "myself"
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO admin.team_project (team_tenant_id, project_id, name, bank_id) VALUES ($1,$2,'My Self',$3) ON CONFLICT DO NOTHING",
		f.teamID, projectID, tenant.ProjectBankID(f.teamID, projectID)); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(),
			"DELETE FROM admin.team_project WHERE team_tenant_id=$1 AND project_id=$2", f.teamID, projectID)
	})
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO admin.team_project_member (team_tenant_id, project_id, tenant_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING",
		f.teamID, projectID, f.memberA); err != nil {
		t.Fatalf("add member: %v", err)
	}
	memberCtx, err := storage.WithSchema(context.Background(), f.memberA)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	aggregate := tenant.PersonalBankID(f.memberA)
	if _, err := f.db.ExecContext(memberCtx,
		"INSERT INTO "+quoteIdentForTest(f.memberA)+".banks (bank_id, name, config) VALUES ($1,'myself','{}'::jsonb) ON CONFLICT DO NOTHING",
		aggregate); err != nil {
		t.Fatalf("create aggregate bank: %v", err)
	}
	if _, err := f.db.ExecContext(memberCtx,
		"INSERT INTO "+quoteIdentForTest(f.memberA)+".memory_units (bank_id, text, fact_type) VALUES ($1,'a cross-project conclusion','world')",
		aggregate); err != nil {
		t.Fatalf("insert aggregate fact: %v", err)
	}

	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"nothing from the aggregate","sources":[]}]}`}
	runner := &mergeRunner{merges: f.merges, store: storage.NewPostgresStore(f.db, "public"), model: model, cfg: mergeTestCfg()}
	if err := runner.runProjectMerges(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0: the only member bank is the aggregate", model.calls)
	}
	var ledger int
	if err := f.db.QueryRowContext(ctx,
		"SELECT count(*) FROM admin.merge_source_unit WHERE team_tenant_id=$1 AND project_id=$2",
		f.teamID, projectID).Scan(&ledger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if ledger != 0 {
		t.Fatalf("ledger rows = %d, want none: the aggregate was read", ledger)
	}
}

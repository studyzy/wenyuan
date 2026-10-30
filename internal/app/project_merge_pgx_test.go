//go:build pgx

package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The merge job against a real database.
//
// The job's own decisions are pinned by project_merge_test.go with a fake store.
// What only PostgreSQL can show is that the three schemas a round touches are
// updated together: the team's bank gets the facts, the admin ledger records their
// provenance, and the member states advance.

// mergeJobFixture is three real schemas plus the admin ledger.
type mergeJobFixture struct {
	db        storage.DB
	merges    storage.MergeStore
	teamID    string
	memberA   string
	memberB   string
	projectID string
}

// newMergeJobFixture provisions the team, its two members and one project.
func newMergeJobFixture(t *testing.T) *mergeJobFixture {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the merge job integration test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(url, storage.PoolBudget{MaxOpenConns: 8, MaxIdleConns: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &mergeJobFixture{
		db:        db,
		merges:    storage.NewPostgresMergeStore(db),
		teamID:    "t_job_team_" + suffix,
		memberA:   "t_job_a_" + suffix,
		memberB:   "t_job_b_" + suffix,
		projectID: "proj_" + suffix,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, schema := range []string{f.teamID, f.memberA, f.memberB} {
			_, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoteIdentForTest(schema)+" CASCADE")
			_, _ = db.ExecContext(ctx, "DELETE FROM admin.tenant WHERE tenant_id = $1", schema)
		}
		_, _ = db.ExecContext(ctx, "DELETE FROM admin.team_project WHERE team_tenant_id = $1", f.teamID)
		_, _ = db.ExecContext(ctx, "DELETE FROM admin.team_member WHERE team_tenant_id = $1", f.teamID)
		_ = db.Close()
	})
	ctx := context.Background()
	if err := storage.MigrateAdminSchemaFor(ctx, db, true); err != nil {
		t.Fatalf("admin migrations: %v", err)
	}
	for _, schema := range []string{f.teamID, f.memberA, f.memberB} {
		if err := storage.ProvisionTenantSchema(ctx, db, schema, false, "vector"); err != nil {
			t.Fatalf("provision %s: %v", schema, err)
		}
		kind := "personal"
		if schema == f.teamID {
			kind = "team"
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO admin.tenant (tenant_id, display_name, status, kind) VALUES ($1,$2,'active',$3)",
			schema, schema, kind); err != nil {
			t.Fatalf("insert tenant %s: %v", schema, err)
		}
	}
	for _, member := range []string{f.memberA, f.memberB} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO admin.team_member (team_tenant_id, tenant_id) VALUES ($1,$2)",
			f.teamID, member); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}
	// The project row comes before its membership: team_project_member's foreign
	// key onto team_project is what refuses the other order.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO admin.team_project (team_tenant_id, project_id, name, bank_id) VALUES ($1,$2,'Web',$3)",
		f.teamID, f.projectID, tenant.ProjectBankID(f.teamID, f.projectID)); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	for _, member := range []string{f.memberA, f.memberB} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO admin.team_project_member (team_tenant_id, project_id, tenant_id) VALUES ($1,$2,$3)",
			f.teamID, f.projectID, member); err != nil {
			t.Fatalf("add project member: %v", err)
		}
	}
	return f
}

// quoteIdentForTest quotes a schema name the way storage does.
func quoteIdentForTest(name string) string { return `"` + name + `"` }

// writeFact inserts one world fact in a schema's bank.
func (f *mergeJobFixture) writeFact(t *testing.T, schema, text string) string {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), schema)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	store := storage.NewPostgresStore(f.db, schema)
	bankID := tenant.ProjectBankID(schema, f.projectID)
	if err := store.EnsureBank(ctx, bankID); err != nil {
		t.Fatalf("ensure bank %s: %v", bankID, err)
	}
	var id string
	if err := f.db.QueryRowContext(ctx,
		"INSERT INTO "+quoteIdentForTest(schema)+".memory_units (bank_id, text, fact_type) VALUES ($1,$2,'world') RETURNING id::text",
		bankID, text).Scan(&id); err != nil {
		t.Fatalf("write fact: %v", err)
	}
	return id
}

// teamFacts reads the team bank's facts.
func (f *mergeJobFixture) teamFacts(t *testing.T) []string {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), f.teamID)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	page, err := storage.NewPostgresStore(f.db, f.teamID).ListMemories(ctx, storage.MemoryQuery{
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Limit: 100,
	})
	if err != nil {
		t.Fatalf("list team facts: %v", err)
	}
	texts := make([]string, 0, len(page.Memories))
	for _, unit := range page.Memories {
		texts = append(texts, unit.Text)
	}
	return texts
}

// ledgerRows counts the project's ledger rows.
func (f *mergeJobFixture) ledgerRows(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM admin.merge_source_unit WHERE team_tenant_id=$1 AND project_id=$2",
		f.teamID, f.projectID).Scan(&count); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	return count
}

// TestProjectMergeRoundWritesThreeSchemas drives the real job: it reads both
// members' schemas, merges through a model, and writes the team's schema and the
// admin ledger together.
func TestProjectMergeRoundWritesThreeSchemas(t *testing.T) {
	f := newMergeJobFixture(t)
	f.writeFact(t, f.memberA, "The deploy uses port 15432.")
	f.writeFact(t, f.memberB, "The deploy uses port 15432.")

	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"The deploy uses port 15432.","sources":[0,1]}]}`,
	}
	runner := &mergeRunner{merges: f.merges, store: storage.NewPostgresStore(f.db, "public"), model: model, cfg: mergeTestCfg()}
	if err := runner.runProjectMerges(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
	texts := f.teamFacts(t)
	if len(texts) != 1 || texts[0] != "The deploy uses port 15432." {
		t.Fatalf("team facts = %v, want the merged fact", texts)
	}
	// Both members' facts support the one merged fact.
	if rows := f.ledgerRows(t); rows != 2 {
		t.Fatalf("ledger rows = %d, want one per member's source", rows)
	}
	// The cursors advanced, so a second round with no new input calls no model.
	model.calls = 0
	if err := runner.runProjectMerges(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls on a quiet second round = %d, want 0", model.calls)
	}
}

// TestProjectMergeRoundDropsADeletedSource pins the privacy底线 end to end: a fact
// deleted after merging leaves the team's content.
func TestProjectMergeRoundDropsADeletedSource(t *testing.T) {
	f := newMergeJobFixture(t)
	unitA := f.writeFact(t, f.memberA, "Only Alice knew this.")

	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"Only Alice knew this.","sources":[0]}]}`,
	}
	cfg := mergeTestCfg()
	runner := &mergeRunner{merges: f.merges, store: storage.NewPostgresStore(f.db, "public"), model: model, cfg: cfg}
	if err := runner.runProjectMerges(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if texts := f.teamFacts(t); len(texts) != 1 {
		t.Fatalf("team facts = %v, want the merged fact", texts)
	}

	// Alice deletes her fact. The next round sees the deleted source, rebuilds
	// from what is left, and the model returns nothing to keep.
	ctx, err := storage.WithSchema(context.Background(), f.memberA)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		"DELETE FROM "+quoteIdentForTest(f.memberA)+".memory_units WHERE id = $1::uuid", unitA); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	model.reply = `{"facts":[{"text":"Nothing survived.","sources":[]}]}`
	model.calls = 0
	if err := runner.runProjectMerges(context.Background()); err != nil {
		t.Fatalf("rebuild run: %v", err)
	}
	texts := f.teamFacts(t)
	for _, text := range texts {
		if text == "Only Alice knew this." {
			t.Fatalf("the deleted fact is still in the team's content: %v", texts)
		}
	}
}

// TestProjectMergeRespectsTheCircuitBreaker pins the failure path end to end: a
// failing model records the error and advances the counter.
func TestProjectMergeRespectsTheCircuitBreaker(t *testing.T) {
	f := newMergeJobFixture(t)
	f.writeFact(t, f.memberA, "A fact the model will choke on.")
	model := &answerProvider{name: "fake", reply: "not json at all"}
	runner := &mergeRunner{merges: f.merges, store: storage.NewPostgresStore(f.db, "public"), model: model, cfg: mergeTestCfg()}
	if err := runner.runProjectMerges(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var failCount int
	var lastError string
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT COALESCE(max(fail_count),0), COALESCE(max(last_error),'') FROM admin.merge_state WHERE team_tenant_id=$1 AND project_id=$2",
		f.teamID, f.projectID).Scan(&failCount, &lastError); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if failCount == 0 {
		t.Fatal("a failed round did not advance the failure counter")
	}
	if lastError == "" {
		t.Fatal("a failed round recorded no error")
	}
	// Nothing was written to the team's bank.
	if texts := f.teamFacts(t); len(texts) != 0 {
		t.Fatalf("team facts = %v after a failed round, want none", texts)
	}
}

// TestMergeStatusReportsTheProject pins the console's read against real rows.
func TestMergeStatusReportsTheProject(t *testing.T) {
	f := newMergeJobFixture(t)
	f.writeFact(t, f.memberA, "A fact for the status read.")
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"A fact for the status read.","sources":[0]}]}`}
	runner := &mergeRunner{merges: f.merges, store: storage.NewPostgresStore(f.db, "public"), model: model, cfg: mergeTestCfg()}
	if err := runner.runProjectMerges(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	status, err := f.merges.MergeStatus(context.Background(), f.teamID, f.projectID)
	if err != nil {
		t.Fatalf("MergeStatus: %v", err)
	}
	if !status.Merged {
		t.Fatal("status reports the project as never merged")
	}
	if status.TeamFacts != 1 {
		t.Fatalf("team facts = %d, want 1", status.TeamFacts)
	}
	if status.BankID != tenant.ProjectBankID(f.teamID, f.projectID) {
		t.Fatalf("bank id = %q", status.BankID)
	}
}

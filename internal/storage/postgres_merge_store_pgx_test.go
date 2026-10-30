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

// mergeFixture is a three-schema project: two members and a team.
type mergeFixture struct {
	db          DB
	store       *PostgresMergeStore
	teamID      string
	projectID   string
	projectName string
	memberA     string
	memberB     string
}

// openMergeDB opens a database for a merge test, or skips without one.
func openMergeDB(t *testing.T) DB {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the merge store test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 8, MaxIdleConns: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// newMergeFixture provisions a team schema, two member schemas and one project.
func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	db := openMergeDB(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &mergeFixture{
		db:          db,
		store:       NewPostgresMergeStore(db),
		teamID:      "t_team_" + suffix,
		projectID:   "proj_" + suffix,
		projectName: "Web",
		memberA:     "t_mem_a_" + suffix,
		memberB:     "t_mem_b_" + suffix,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, schema := range []string{f.teamID, f.memberA, f.memberB} {
			_, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
			_, _ = db.ExecContext(ctx, "DELETE FROM "+adminTable("tenant")+" WHERE tenant_id = $1", schema)
		}
		_, _ = db.ExecContext(ctx, "DELETE FROM "+adminTable("team_project")+" WHERE team_tenant_id = $1", f.teamID)
		_, _ = db.ExecContext(ctx, "DELETE FROM "+adminTable("team_member")+" WHERE team_tenant_id = $1", f.teamID)
		_ = db.Close()
	})
	ctx := context.Background()
	if err := MigrateAdminSchemaFor(ctx, db, true); err != nil {
		t.Fatalf("admin migrations: %v", err)
	}
	for _, schema := range []string{f.teamID, f.memberA, f.memberB} {
		if err := ProvisionTenantSchema(ctx, db, schema, false, "vector"); err != nil {
			t.Fatalf("provision %s: %v", schema, err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO "+adminTable("tenant")+" (tenant_id, display_name, status, kind) VALUES ($1,$2,'active',$3)",
			schema, schema, kindFor(schema, f)); err != nil {
			t.Fatalf("insert tenant %s: %v", schema, err)
		}
	}
	for _, member := range []string{f.memberA, f.memberB} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO "+adminTable("team_member")+" (team_tenant_id, tenant_id) VALUES ($1,$2)",
			f.teamID, member); err != nil {
			t.Fatalf("add member %s: %v", member, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+adminTable("team_project")+" (team_tenant_id, project_id, name, bank_id) VALUES ($1,$2,$3,$4)",
		f.teamID, f.projectID, f.projectName, tenant.ProjectBankID(f.teamID, f.projectID)); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	for _, member := range []string{f.memberA, f.memberB} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO "+adminTable("team_project_member")+" (team_tenant_id, project_id, tenant_id) VALUES ($1,$2,$3)",
			f.teamID, f.projectID, member); err != nil {
			t.Fatalf("add project member %s: %v", member, err)
		}
	}
	return f
}

// kindFor reports the tenant kind a schema plays in the fixture.
func kindFor(schema string, f *mergeFixture) string {
	if schema == f.teamID {
		return "team"
	}
	return "personal"
}

// teamSchemaCtx is a context naming the team's schema.
func (f *mergeFixture) teamSchemaCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := WithSchema(context.Background(), f.teamID)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	return ctx
}

// writeMemberFact inserts one world fact into a member's own bank.
func (f *mergeFixture) writeMemberFact(t *testing.T, member, text string) string {
	t.Helper()
	ctx, err := WithSchema(context.Background(), member)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	bankID := tenant.ProjectBankID(member, f.projectID)
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO "+quoteIdent(member)+".banks (bank_id, name, config) VALUES ($1,$2,'{}'::jsonb) ON CONFLICT DO NOTHING",
		bankID, f.projectName); err != nil {
		t.Fatalf("create member bank: %v", err)
	}
	var id string
	if err := f.db.QueryRowContext(ctx,
		"INSERT INTO "+quoteIdent(member)+".memory_units (bank_id, text, fact_type) VALUES ($1,$2,'world') RETURNING id::text",
		bankID, text).Scan(&id); err != nil {
		t.Fatalf("insert member fact: %v", err)
	}
	return id
}

// deleteMemberFact removes one fact from a member's bank.
func (f *mergeFixture) deleteMemberFact(t *testing.T, member, unitID string) {
	t.Helper()
	ctx, err := WithSchema(context.Background(), member)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		"DELETE FROM "+quoteIdent(member)+".memory_units WHERE id = $1::uuid", unitID); err != nil {
		t.Fatalf("delete member fact: %v", err)
	}
}

// ensureTeamBank creates the team's bank.
func (f *mergeFixture) ensureTeamBank(t *testing.T) {
	t.Helper()
	ctx := f.teamSchemaCtx(t)
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO "+quoteIdent(f.teamID)+".banks (bank_id, name, config) VALUES ($1,$2,'{}'::jsonb) ON CONFLICT DO NOTHING",
		tenant.ProjectBankID(f.teamID, f.projectID), f.projectName); err != nil {
		t.Fatalf("create team bank: %v", err)
	}
}

// teamFactTexts reads the team bank's managed facts.
func (f *mergeFixture) teamFactTexts(t *testing.T) []string {
	t.Helper()
	ctx := f.teamSchemaCtx(t)
	rows, err := f.db.QueryContext(ctx,
		"SELECT text FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id = $1 AND fact_type='world' ORDER BY text",
		tenant.ProjectBankID(f.teamID, f.projectID))
	if err != nil {
		t.Fatalf("read team facts: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatalf("scan: %v", err)
		}
		texts = append(texts, text)
	}
	return texts
}

// runRound runs one round end to end: begin, produce a fixed answer, finish.
func (f *mergeFixture) runRound(t *testing.T, facts []TeamFactRow, sources map[string][]MergeSourceRow, rebuild []string) bool {
	t.Helper()
	ctx := context.Background()
	var stamp time.Time
	ok, err := f.store.BeginMergeStanza(ctx, f.teamID, f.projectID, 100, 5*time.Minute,
		func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			return nil
		})
	if err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !ok {
		return false
	}
	req := FinishMergeRequest{
		TeamID:     f.teamID,
		ProjectID:  f.projectID,
		TeamSchema: f.teamID,
		BankID:     tenant.ProjectBankID(f.teamID, f.projectID),
		Stamp:      stamp,
		Replace: MergeReplaceResult{
			Insert:         facts,
			Sources:        sources,
			RebuildMembers: rebuild,
		},
		Members: []string{f.memberA, f.memberB},
		Cursors: map[string]MergeCursor{},
		Rebuild: len(rebuild) > 0,
	}
	if err := f.store.FinishMergeStanza(ctx, req); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}
	return true
}

// TestMergeStoreDiscoversTheProject pins discovery: only an active team's
// project with members is listed, and each member's state comes with it.
func TestMergeStoreDiscoversTheProject(t *testing.T) {
	f := newMergeFixture(t)
	projects, err := f.store.ListMergeProjects(context.Background())
	if err != nil {
		t.Fatalf("ListMergeProjects: %v", err)
	}
	var found *MergeProject
	for i := range projects {
		if projects[i].TeamID == f.teamID && projects[i].ProjectID == f.projectID {
			found = &projects[i]
		}
	}
	if found == nil {
		t.Fatalf("project %s/%s not discovered", f.teamID, f.projectID)
	}
	if len(found.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(found.Members))
	}
	if found.BankID != tenant.ProjectBankID(f.teamID, f.projectID) {
		t.Fatalf("bank id = %q, want %q", found.BankID, tenant.ProjectBankID(f.teamID, f.projectID))
	}
	for _, member := range found.Members {
		if member.HasLedger {
			t.Fatalf("member %s reported a ledger row before any round", member.TenantID)
		}
	}
}

// TestMergeStoreRoundWritesTeamFactsAndLedger is the round's happy path: facts
// land in the team's schema, the ledger records provenance, and the cursor moves.
func TestMergeStoreRoundWritesTeamFactsAndLedger(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "The deploy uses port 15432.")
	unitB := f.writeMemberFact(t, f.memberB, "The deploy uses port 15432.")

	key := tenant.MergeKey("The deploy uses port 15432.")
	if !f.runRound(t,
		[]TeamFactRow{{Text: "The deploy uses port 15432.", MergeKey: key}},
		map[string][]MergeSourceRow{
			f.memberA: {{UnitID: unitA, MergeKey: key}},
			f.memberB: {{UnitID: unitB, MergeKey: key}},
		}, nil) {
		t.Fatal("round did not run")
	}

	texts := f.teamFactTexts(t)
	if len(texts) != 1 || texts[0] != "The deploy uses port 15432." {
		t.Fatalf("team facts = %v, want the one merged fact", texts)
	}
	var ledgerRows int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+adminTable("merge_source_unit")+" WHERE team_tenant_id=$1 AND project_id=$2",
		f.teamID, f.projectID).Scan(&ledgerRows); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if ledgerRows != 2 {
		t.Fatalf("ledger rows = %d, want 2 (one per member's source)", ledgerRows)
	}
}

// TestMergeStoreSourceDeletionTriggersRebuild is the privacy底线: a member fact
// deleted after merging is detected, so the team's content can be rebuilt
// without it.
func TestMergeStoreSourceDeletionTriggersRebuild(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "Only A knew this.")
	key := tenant.MergeKey("Only A knew this.")
	if !f.runRound(t,
		[]TeamFactRow{{Text: "Only A knew this.", MergeKey: key}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unitA, MergeKey: key}}}, nil) {
		t.Fatal("round did not run")
	}

	var deleted bool
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, 100, time.Minute,
		func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				if member.Member.TenantID == f.memberA {
					deleted = member.Deleted
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if deleted {
		t.Fatal("a live source must not be reported deleted")
	}
	// Release the claim the probe above took.
	if err := f.store.AbandonMergeStanza(context.Background(), f.teamID, f.projectID, MergeStateFailure{}); err != nil {
		t.Fatalf("abandon: %v", err)
	}

	f.deleteMemberFact(t, f.memberA, unitA)
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, 100, time.Minute,
		func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				if member.Member.TenantID == f.memberA {
					deleted = member.Deleted
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !deleted {
		t.Fatal("a deleted source that still supports team content must be reported deleted")
	}
}

// TestMergeStoreDroppedSourceDeletionDoesNotRebuild is the ledger's whole point:
// a source the model folded away supports nothing, so deleting it must not force
// a rebuild — otherwise one stale judgement could fail the project closed.
func TestMergeStoreDroppedSourceDeletionDoesNotRebuild(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	kept := f.writeMemberFact(t, f.memberA, "A keeps this.")
	redundant := f.writeMemberFact(t, f.memberB, "B said the same thing differently.")
	key := tenant.MergeKey("A keeps this.")
	if !f.runRound(t,
		[]TeamFactRow{{Text: "A keeps this.", MergeKey: key}},
		map[string][]MergeSourceRow{
			f.memberA: {{UnitID: kept, MergeKey: key}},
			// The model folded B's version into A's, recording it as dropped.
			f.memberB: {{UnitID: redundant, MergeKey: tenant.DroppedMergeKey}},
		}, nil) {
		t.Fatal("round did not run")
	}
	f.deleteMemberFact(t, f.memberB, redundant)

	var deleted bool
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, 100, time.Minute,
		func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				if member.Member.TenantID == f.memberB {
					deleted = member.Deleted
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if deleted {
		t.Fatal("deleting a folded-away source must not trigger a rebuild")
	}
}

// TestMergeStoreClaimSerializesRounds pins the cross-transaction claim: while one
// round holds it, another process's round is skipped rather than reading the same
// inputs.
func TestMergeStoreClaimSerializesRounds(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.writeMemberFact(t, f.memberA, "something")

	firstRan := false
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, 100, 5*time.Minute,
		func(stanza MergeStanza) error {
			firstRan = true
			// A second round while the first still holds the claim must not run.
			secondRan, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, 100, 5*time.Minute,
				func(MergeStanza) error {
					t.Error("a second round ran while the first held the claim")
					return nil
				})
			if err != nil {
				return err
			}
			if secondRan {
				t.Error("second round reported it ran")
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !firstRan {
		t.Fatal("the first round did not run")
	}
}

// TestMergeStoreFenceRejectsSupersededWrite is R7's guarantee: a round whose
// claim another process took over cannot apply its stale result.
func TestMergeStoreFenceRejectsSupersededWrite(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	var stamp time.Time
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, 100, time.Minute,
		func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	// Simulate another process taking the claim over.
	if _, err := f.db.ExecContext(context.Background(),
		"UPDATE "+adminTable("merge_state")+" SET merging_since = now() + interval '1 hour' WHERE team_tenant_id=$1 AND project_id=$2",
		f.teamID, f.projectID); err != nil {
		t.Fatalf("supersede claim: %v", err)
	}
	err := f.store.FinishMergeStanza(context.Background(), FinishMergeRequest{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
		Replace: MergeReplaceResult{Insert: []TeamFactRow{{Text: "stale", MergeKey: tenant.MergeKey("stale")}}},
		Members: []string{f.memberA},
	})
	if err == nil {
		t.Fatal("a superseded round's write must be refused")
	}
	if texts := f.teamFactTexts(t); len(texts) != 0 {
		t.Fatalf("a refused write left facts behind: %v", texts)
	}
}

// TestMergeStoreClearMemberProjectErasesTheLedger pins R6: taking a member out of
// one project must remove their ledger, or the project rebuilds forever.
func TestMergeStoreClearMemberProjectErasesTheLedger(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "A contributed this.")
	key := tenant.MergeKey("A contributed this.")
	if !f.runRound(t,
		[]TeamFactRow{{Text: "A contributed this.", MergeKey: key}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unitA, MergeKey: key}}}, nil) {
		t.Fatal("round did not run")
	}
	if err := f.store.ClearMemberProject(context.Background(), f.teamID, f.projectID, f.memberA); err != nil {
		t.Fatalf("ClearMemberProject: %v", err)
	}
	var ledgerRows, stateRows int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+adminTable("merge_source_unit")+" WHERE team_tenant_id=$1 AND project_id=$2 AND tenant_id=$3",
		f.teamID, f.projectID, f.memberA).Scan(&ledgerRows); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+adminTable("merge_state")+" WHERE team_tenant_id=$1 AND project_id=$2 AND tenant_id=$3",
		f.teamID, f.projectID, f.memberA).Scan(&stateRows); err != nil {
		t.Fatalf("count state: %v", err)
	}
	if ledgerRows != 0 || stateRows != 0 {
		t.Fatalf("ledger rows = %d, state rows = %d, want both 0", ledgerRows, stateRows)
	}
}

// TestMergeStoreSpansSchemasInOneDatabase pins the assumption a cross-schema
// transaction rests on: the registry and the tenant schemas live in one database.
func TestMergeStoreSpansSchemasInOneDatabase(t *testing.T) {
	f := newMergeFixture(t)
	var adminDB, teamDB string
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT current_database() FROM "+adminTable("tenant")+" LIMIT 1").Scan(&adminDB); err != nil {
		t.Fatalf("admin database: %v", err)
	}
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT current_database() FROM "+quoteIdent(f.teamID)+".banks LIMIT 1").Scan(&teamDB); err != nil {
		// No rows is fine; ask without reading a row.
		if err := f.db.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&teamDB); err != nil {
			t.Fatalf("team database: %v", err)
		}
	}
	if adminDB != "" && teamDB != "" && adminDB != teamDB {
		t.Fatalf("admin schema is in %q but tenant schemas are in %q; one round's transaction cannot span both",
			adminDB, teamDB)
	}
}

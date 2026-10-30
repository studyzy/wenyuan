//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The personal aggregate store against a real database.
//
// Everything these tests protect lives only here: a round reads every bank of one
// tenant, writes one bank of that tenant and records its ledger in the admin
// schema, all in one transaction, behind an advisory lock and a claim stamp. The
// rollup's own rules are pure functions tested in internal/personalrollup and
// internal/app; these are the writes those tests cannot prove.

// personalFixture is one person's schema with a few banks.
type personalFixture struct {
	db      DB
	store   *PostgresPersonalStore
	persona string
}

// openPersonalDB opens a database for an aggregate test, or skips without one.
func openPersonalDB(t *testing.T) DB {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the personal aggregate test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 8, MaxIdleConns: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// newPersonalFixture provisions one personal tenant.
func newPersonalFixture(t *testing.T) *personalFixture {
	t.Helper()
	db := openPersonalDB(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &personalFixture{db: db, store: NewPostgresPersonalStore(db), persona: "agg_a_" + suffix}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoteIdent(f.persona)+" CASCADE")
		_, _ = db.ExecContext(ctx, "DELETE FROM "+adminTable("tenant")+" WHERE tenant_id = $1", f.persona)
		_ = db.Close()
	})
	ctx := context.Background()
	if err := MigrateAdminSchemaFor(ctx, db, false); err != nil {
		t.Fatalf("admin migrations: %v", err)
	}
	if err := ProvisionTenantSchema(ctx, db, f.persona, false, "vector"); err != nil {
		t.Fatalf("provision %s: %v", f.persona, err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+adminTable("tenant")+" (tenant_id, display_name, status, kind) VALUES ($1,$2,'active','personal')",
		f.persona, f.persona); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	return f
}

// ctx is the fixture's tenant context.
func (f *personalFixture) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := WithSchema(context.Background(), f.persona)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	return ctx
}

// bank creates one bank in the person's schema.
func (f *personalFixture) bank(t *testing.T, name string) string {
	t.Helper()
	bankID := tenant.ProjectBankID(f.persona, name)
	if _, err := f.db.ExecContext(f.ctx(t), `
		INSERT INTO `+quoteIdent(f.persona)+`.banks (bank_id, name, config) VALUES ($1,$2,'{}'::jsonb)
		ON CONFLICT DO NOTHING`, bankID, name); err != nil {
		t.Fatalf("create bank %s: %v", bankID, err)
	}
	return bankID
}

// fact inserts one world fact and returns its id.
func (f *personalFixture) fact(t *testing.T, bankID, text string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRowContext(f.ctx(t), `
		INSERT INTO `+quoteIdent(f.persona)+`.memory_units (bank_id, text, fact_type)
		VALUES ($1,$2,'world') RETURNING id::text`, bankID, text).Scan(&id); err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	return id
}

// deleteFact removes one fact.
func (f *personalFixture) deleteFact(t *testing.T, unitID string) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx(t),
		"DELETE FROM "+quoteIdent(f.persona)+".memory_units WHERE id = $1::uuid", unitID); err != nil {
		t.Fatalf("delete fact: %v", err)
	}
}

// round runs one round through the store and returns how the job saw it.
func (f *personalFixture) round(t *testing.T, batch int) (PersonalRoundInput, bool, time.Time) {
	t.Helper()
	var input PersonalRoundInput
	var stamp time.Time
	ran, err := f.store.BeginPersonalRound(f.ctx(t), f.persona, batch, time.Minute,
		func(read PersonalRoundInput, claim time.Time) error {
			input, stamp = read, claim
			return nil
		})
	if err != nil {
		t.Fatalf("begin round: %v", err)
	}
	return input, ran, stamp
}

// finish writes one round's output.
func (f *personalFixture) finish(t *testing.T, req FinishPersonalRequest) {
	t.Helper()
	if err := f.store.FinishPersonalRound(f.ctx(t), req); err != nil {
		t.Fatalf("finish round: %v", err)
	}
}

// aggregateTexts reads the aggregate bank's world facts.
func (f *personalFixture) aggregateTexts(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.QueryContext(f.ctx(t), `
		SELECT text FROM `+quoteIdent(f.persona)+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'world' ORDER BY text`,
		tenant.PersonalBankID(f.persona))
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

// answer builds a one-round finish request for the aggregate.
//
// It mirrors what the job does — classify against the previous content, record
// each consumed source against the fact it supports, advance the watermarks — but
// stays inside the storage package: importing the rules package from this test
// would close an import cycle through monitoring and httpx.
func personalFinish(input PersonalRoundInput, stamp time.Time, texts ...string) FinishPersonalRequest {
	plan := personalDiff(input, texts)
	sourceKeys := plan.sourceKeys(len(input.Facts))
	sources := make([]PersonalSourceRow, 0, len(input.Facts))
	for index, fact := range input.Facts {
		for _, key := range sourceKeys[index] {
			sources = append(sources, PersonalSourceRow{UnitID: fact.UnitID, MergeKey: key, BankID: fact.BankID})
		}
	}
	cursors := map[string]MergeCursor{}
	for _, fact := range input.Facts {
		cursors[fact.BankID] = MergeCursor{UpdatedAt: fact.UpdatedAt, UnitID: fact.UnitID}
	}
	return FinishPersonalRequest{
		TenantID: input.TenantID, Schema: input.Schema, BankID: input.BankID, Stamp: stamp,
		Remove: plan.Remove, Insert: plan.Insert, Sources: sources,
		Cursors: cursors, UnmanagedFacts: input.UnmanagedFacts,
	}
}

// personalDiffResult is the minimal fact-level change these tests need.
type personalDiffResult struct {
	Remove []PersonalPreviousFact
	Insert []PersonalPreviousFact
	// supported maps a fresh index to the merge keys it supports.
	supported map[int][]string
}

// sourceKeys reports, per fresh index, the keys it supports or the dropped marker.
func (d personalDiffResult) sourceKeys(freshCount int) map[int][]string {
	out := make(map[int][]string, freshCount)
	for index, keys := range d.supported {
		out[index] = keys
	}
	for index := 0; index < freshCount; index++ {
		if len(out[index]) == 0 {
			out[index] = []string{tenant.DroppedMergeKey}
		}
	}
	return out
}

// personalDiff classifies the answers against the previous content. Fact i of the
// round is assumed to come from fresh index i, which is what these fixtures build.
func personalDiff(input PersonalRoundInput, texts []string) personalDiffResult {
	result := personalDiffResult{supported: map[int][]string{}}
	answerKeys := make([]string, 0, len(texts))
	for i, text := range texts {
		key := tenant.MergeKey(text)
		answerKeys = append(answerKeys, key)
		if i < len(input.Facts) {
			result.supported[i] = append(result.supported[i], key)
		}
	}
	previousKeys := map[string]bool{}
	for _, fact := range input.Previous {
		key := fact.MergeKey
		if key == "" {
			key = tenant.MergeKey(fact.Text)
		}
		previousKeys[key] = true
	}
	for _, key := range answerKeys {
		if !previousKeys[key] {
			result.Insert = append(result.Insert, PersonalPreviousFact{Text: keyToText(key, texts), MergeKey: key})
			previousKeys[key] = true
		}
	}
	for _, fact := range input.Previous {
		key := fact.MergeKey
		if key == "" {
			key = tenant.MergeKey(fact.Text)
		}
		kept := false
		for _, answerKey := range answerKeys {
			if answerKey == key {
				kept = true
				break
			}
		}
		if !kept {
			result.Remove = append(result.Remove, PersonalPreviousFact{ID: fact.ID, Text: fact.Text, MergeKey: key})
		}
	}
	return result
}

// keyToText recovers the text a key was derived from.
func keyToText(key string, texts []string) string {
	for _, text := range texts {
		if tenant.MergeKey(text) == key {
			return text
		}
	}
	return ""
}

// TestPersonalRoundReadsEveryBankAndWritesTheAggregate is the round's shape: both
// banks' facts are read, the aggregate is written, the ledger records both banks
// and each watermark advances.
func TestPersonalRoundReadsEveryBankAndWritesTheAggregate(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	billing := f.bank(t, "billing")
	f.fact(t, orders, "orders deploys on port 15432")
	f.fact(t, billing, "billing stores cents")

	input, ran, stamp := f.round(t, 20)
	if !ran {
		t.Fatal("the first round did not run")
	}
	if len(input.Facts) != 2 {
		t.Fatalf("fresh facts = %d, want both banks", len(input.Facts))
	}
	if input.BankID != tenant.PersonalBankID(f.persona) || input.Schema != f.persona {
		t.Fatalf("input names %q/%q", input.BankID, input.Schema)
	}
	f.finish(t, personalFinish(input, stamp, "orders deploys on port 15432", "billing stores cents"))

	got := f.aggregateTexts(t)
	if len(got) != 2 {
		t.Fatalf("aggregate = %v, want both facts", got)
	}
	status, err := f.store.PersonalStatus(context.Background(), f.persona, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.AggregateFacts != 2 || status.SourceBanks != 2 {
		t.Fatalf("status = %+v, want 2 aggregate facts over 2 banks", status)
	}
	if status.UserFacts != 0 {
		t.Fatalf("user facts = %d, want 0", status.UserFacts)
	}

	// Nothing new: the next round reads nothing and is not a failure.
	second, ran, _ := f.round(t, 20)
	if !ran {
		t.Fatal("the second round did not run")
	}
	if len(second.Facts) != 0 {
		t.Fatalf("second round facts = %d, want 0", len(second.Facts))
	}
}

// TestPersonalWatermarksArePerBank pins the watermark's granularity: one bank's
// new fact does not make the other bank read again.
func TestPersonalWatermarksArePerBank(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	billing := f.bank(t, "billing")
	f.fact(t, orders, "orders deploys on port 15432")
	f.fact(t, billing, "billing stores cents")

	input, _, stamp := f.round(t, 20)
	f.finish(t, personalFinish(input, stamp, "orders deploys on port 15432", "billing stores cents"))

	f.fact(t, orders, "orders has a health endpoint")
	next, _, _ := f.round(t, 20)
	if len(next.Facts) != 1 {
		t.Fatalf("fresh facts = %d, want the one new fact", len(next.Facts))
	}
	if next.Facts[0].BankID != orders {
		t.Fatalf("fresh fact came from %s, want %s", next.Facts[0].BankID, orders)
	}
}

// TestPersonalBatchIsSharedAcrossBanks pins that one prolific bank cannot starve
// the others within a round.
func TestPersonalBatchIsSharedAcrossBanks(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	f.bank(t, "billing")
	for i := 0; i < 5; i++ {
		f.fact(t, orders, fmt.Sprintf("orders fact %d", i))
	}
	input, _, _ := f.round(t, 3)
	if len(input.Facts) != 3 {
		t.Fatalf("fresh facts = %d, want the batch bound", len(input.Facts))
	}
}

// TestPersonalDeletionForcesARebuild pins the privacy path: a ledger source that
// vanished from its bank makes the round recompute everything, and the aggregate
// no longer carries what was deleted.
func TestPersonalDeletionForcesARebuild(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	kept := f.fact(t, orders, "orders deploys on port 15432")
	gone := f.fact(t, orders, "orders used Jenkins")

	input, _, stamp := f.round(t, 20)
	if len(input.Facts) != 2 {
		t.Fatalf("fresh facts = %d, want 2", len(input.Facts))
	}
	f.finish(t, personalFinish(input, stamp, "orders deploys on port 15432", "orders used Jenkins"))

	f.deleteFact(t, gone)
	next, _, _ := f.round(t, 20)
	if !next.SourceDeleted {
		t.Fatal("a deleted ledger source did not mark the round for a rebuild")
	}
	if len(next.Facts) != 1 || next.Facts[0].UnitID != kept {
		t.Fatalf("rebuild input = %+v, want only the surviving fact", next.Facts)
	}
}

// TestPersonalDroppedSourceDeletionDoesNotForceARebuild pins the ledger's central
// distinction on this plane: a source the model folded away supports nothing, so
// deleting it must not trigger a rebuild.
func TestPersonalDroppedSourceDeletionDoesNotForceARebuild(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	f.fact(t, orders, "orders deploys on port 15432")
	dropped := f.fact(t, orders, "orders used to use Jenkins")

	input, _, stamp := f.round(t, 20)
	// The round folds "Jenkins" away: it supports nothing.
	key := tenant.MergeKey("orders deploys on port 15432")
	var sources []PersonalSourceRow
	for index, fact := range input.Facts {
		if index == 0 {
			sources = append(sources, PersonalSourceRow{UnitID: fact.UnitID, MergeKey: key, BankID: fact.BankID})
			continue
		}
		sources = append(sources, PersonalSourceRow{UnitID: fact.UnitID, MergeKey: tenant.DroppedMergeKey, BankID: fact.BankID})
	}
	f.finish(t, FinishPersonalRequest{
		TenantID: input.TenantID, Schema: input.Schema, BankID: input.BankID, Stamp: stamp,
		Insert:  []PersonalPreviousFact{{Text: "orders deploys on port 15432", MergeKey: key}},
		Sources: sources,
		Cursors: map[string]MergeCursor{orders: {UpdatedAt: input.Facts[0].UpdatedAt, UnitID: input.Facts[0].UnitID}},
	})

	f.deleteFact(t, dropped)
	next, _, _ := f.round(t, 20)
	if next.SourceDeleted {
		t.Fatal("deleting a folded-away source must not force a rebuild")
	}
}

// TestPersonalFenceRejectsASupersededRound pins the claim: a round whose stamp was
// replaced writes nothing.
func TestPersonalFenceRejectsASupersededRound(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	f.fact(t, orders, "orders deploys on port 15432")
	input, _, stamp := f.round(t, 20)

	// A second round claims the tenant and its stamp replaces the first one's.
	if _, err := f.db.ExecContext(context.Background(), `
		UPDATE `+adminTable("personal_state")+` SET merging_since = $2 WHERE tenant_id = $1`,
		f.persona, stamp.Add(time.Second)); err != nil {
		t.Fatalf("replace claim: %v", err)
	}
	req := personalFinish(input, stamp, "orders deploys on port 15432")
	if err := f.store.FinishPersonalRound(f.ctx(t), req); err != ErrPersonalClaimLost {
		t.Fatalf("finish error = %v, want a lost claim", err)
	}
	if got := f.aggregateTexts(t); len(got) != 0 {
		t.Fatalf("aggregate = %v, a superseded round wrote", got)
	}
}

// TestPersonalRoundIsSkippedWhileClaimed pins the concurrency rule: a live claim
// makes the next round report it did not run.
func TestPersonalRoundIsSkippedWhileClaimed(t *testing.T) {
	f := newPersonalFixture(t)
	f.bank(t, "orders")
	if _, err := f.db.ExecContext(context.Background(), `
		INSERT INTO `+adminTable("personal_state")+` (tenant_id, merging_since) VALUES ($1, now())`,
		f.persona); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if _, ran, _ := f.round(t, 20); ran {
		t.Fatal("a claimed tenant was merged again")
	}
}

// TestPersonalUnmanagedRowsAreReplaced pins the defence against a direct write: a
// world fact in the aggregate bank no round wrote is replaced, and its count is
// reported to the operator before it goes.
func TestPersonalUnmanagedRowsAreReplaced(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	f.fact(t, orders, "orders deploys on port 15432")
	// A client retained straight into the aggregate bank.
	f.bank(t, "myself")
	f.fact(t, tenant.PersonalBankID(f.persona), "someone wrote this by hand")

	input, _, stamp := f.round(t, 20)
	if input.UnmanagedFacts != 1 {
		t.Fatalf("unmanaged facts = %d, want 1", input.UnmanagedFacts)
	}
	f.finish(t, personalFinish(input, stamp, "orders deploys on port 15432"))
	for _, text := range f.aggregateTexts(t) {
		if strings.Contains(text, "by hand") {
			t.Fatalf("the direct write survived: %v", f.aggregateTexts(t))
		}
	}
	status, err := f.store.PersonalStatus(context.Background(), f.persona, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.UserFacts != 0 {
		t.Fatalf("user facts = %d, want 0 after the round", status.UserFacts)
	}
}

// TestPersonalObservationsFollowTheAggregate pins that deleting an aggregate fact
// removes the observation built only on it, so a deleted fact cannot survive as an
// observation.
func TestPersonalObservationsFollowTheAggregate(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	f.fact(t, orders, "orders deploys on port 15432")
	input, _, stamp := f.round(t, 20)
	f.finish(t, personalFinish(input, stamp, "orders deploys on port 15432"))

	var factID string
	if err := f.db.QueryRowContext(f.ctx(t),
		"SELECT id::text FROM "+quoteIdent(f.persona)+".memory_units WHERE bank_id = $1 AND fact_type='world'",
		tenant.PersonalBankID(f.persona)).Scan(&factID); err != nil {
		t.Fatalf("read aggregate fact: %v", err)
	}
	if _, err := f.db.ExecContext(f.ctx(t), `
		INSERT INTO `+quoteIdent(f.persona)+`.memory_units (bank_id, text, fact_type, source_memory_ids)
		VALUES ($1,'orders runs on docker','observation', ARRAY[$2::uuid])`,
		tenant.PersonalBankID(f.persona), factID); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	// The next round drops the fact: the observation goes with it.
	next, _, nextStamp := f.round(t, 20)
	f.finish(t, FinishPersonalRequest{
		TenantID: next.TenantID, Schema: next.Schema, BankID: next.BankID, Stamp: nextStamp,
		Remove: []PersonalPreviousFact{{ID: factID, Text: "orders deploys on port 15432", MergeKey: tenant.MergeKey("orders deploys on port 15432")}},
	})
	var observations int
	if err := f.db.QueryRowContext(f.ctx(t),
		"SELECT count(*) FROM "+quoteIdent(f.persona)+".memory_units WHERE bank_id=$1 AND fact_type='observation'",
		tenant.PersonalBankID(f.persona)).Scan(&observations); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if observations != 0 {
		t.Fatalf("observations = %d, want none left", observations)
	}
}

// TestPersonalDirectoryIsProjected pins that the round's output lands in the
// navigation index the console reads.
func TestPersonalDirectoryIsProjected(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	billing := f.bank(t, "billing")
	f.fact(t, orders, "orders deploys on port 15432")
	f.fact(t, billing, "billing deploys on the same port")
	input, _, stamp := f.round(t, 20)
	key := tenant.MergeKey("shared deploy port")
	req := personalFinish(input, stamp)
	req.Insert = []PersonalPreviousFact{{Text: "shared deploy port", MergeKey: key}}
	req.Topics = []PersonalTopic{
		{Name: "deploy", BankID: orders, MergeKey: key},
		{Name: "deploy", BankID: billing, MergeKey: key},
	}
	req.FactBanks = map[string][]string{key: {orders, billing}}
	f.finish(t, req)

	directory, err := f.store.PersonalDirectory(context.Background(), f.persona)
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	if len(directory.Links) != 2 {
		t.Fatalf("links = %+v, want both banks", directory.Links)
	}
	if len(directory.Topics) != 2 {
		t.Fatalf("topics = %+v, want both banks' entries", directory.Topics)
	}
}

// TestPersonalStatusReportsTheLedger pins the console's view: counts, the adopted
// flag a pre-existing bank sets, and the halt state.
func TestPersonalStatusReportsTheLedger(t *testing.T) {
	f := newPersonalFixture(t)
	status, err := f.store.PersonalStatus(context.Background(), f.persona, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Aggregate || status.Kind != "personal" || status.BankID != tenant.PersonalBankID(f.persona) {
		t.Fatalf("status = %+v, want a personal aggregate", status)
	}
	if status.LastRoundAt != nil {
		t.Fatalf("last round = %v, want none yet", status.LastRoundAt)
	}

	if err := f.store.AbandonPersonalRound(context.Background(), f.persona,
		PersonalFailure{Error: "model down", Counting: true}); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	status, err = f.store.PersonalStatus(context.Background(), f.persona, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.FailCount != 1 || status.LastError != "model down" {
		t.Fatalf("status = %+v, want a recorded failure", status)
	}
	if err := f.store.ResetPersonalState(context.Background(), f.persona); err != nil {
		t.Fatalf("reset: %v", err)
	}
	status, _ = f.store.PersonalStatus(context.Background(), f.persona, false)
	if status.FailCount != 0 || status.Halted {
		t.Fatalf("status after reset = %+v, want healthy", status)
	}
}

// TestPersonalStatusReportsATeamAsHavingNoAggregate pins the product decision: a
// team is not a person, and its aggregate bank is never built.
func TestPersonalStatusReportsATeamAsHavingNoAggregate(t *testing.T) {
	f := newPersonalFixture(t)
	teamID := f.persona + "_team"
	ctx := context.Background()
	if err := ProvisionTenantSchema(ctx, f.db, teamID, false, "vector"); err != nil {
		t.Fatalf("provision team: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quoteIdent(teamID)+" CASCADE")
		_, _ = f.db.ExecContext(context.Background(), "DELETE FROM "+adminTable("tenant")+" WHERE tenant_id = $1", teamID)
	})
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO "+adminTable("tenant")+" (tenant_id, display_name, status, kind) VALUES ($1,$1,'active','team')",
		teamID); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	status, err := f.store.PersonalStatus(ctx, teamID, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Aggregate || status.Kind != "team" {
		t.Fatalf("status = %+v, want no aggregate for a team", status)
	}
	tenants, err := f.store.ListRollupTenants(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, one := range tenants {
		if one.ID == teamID {
			t.Fatal("a team was listed as an aggregate tenant")
		}
	}
}

// TestPersonalTenantDiscovery pins that only active personal tenants are listed.
func TestPersonalTenantDiscovery(t *testing.T) {
	f := newPersonalFixture(t)
	suspended := f.persona + "_gone"
	ctx := context.Background()
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO "+adminTable("tenant")+" (tenant_id, display_name, status, kind) VALUES ($1,$1,'suspended','personal')",
		suspended); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DELETE FROM "+adminTable("tenant")+" WHERE tenant_id = $1", suspended)
	})
	tenants, err := f.store.ListRollupTenants(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, one := range tenants {
		if one.ID == suspended {
			t.Fatal("a suspended tenant was listed")
		}
		if one.ID == f.persona {
			found = true
		}
	}
	if !found {
		t.Fatal("the active personal tenant was not listed")
	}
}

// TestPersonalRebuildRequestIsHonoured pins the operator's lever: a rebuild
// request makes the next round read every fact from the beginning and clears the
// flag.
func TestPersonalRebuildRequestIsHonoured(t *testing.T) {
	f := newPersonalFixture(t)
	orders := f.bank(t, "orders")
	for i := 0; i < 3; i++ {
		f.fact(t, orders, fmt.Sprintf("orders fact %d", i))
	}
	first, _, stamp := f.round(t, 1)
	f.finish(t, personalFinish(first, stamp, "orders fact 0"))

	if err := f.store.RequestRebuild(context.Background(), f.persona); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	next, _, nextStamp := f.round(t, 1)
	if !next.RebuildRequested {
		t.Fatal("the rebuild request was not seen by the round")
	}
	if len(next.Facts) != 3 {
		t.Fatalf("rebuild input = %d, want every fact", len(next.Facts))
	}
	req := personalFinish(next, nextStamp, "orders fact 0")
	req.Rebuild = true
	f.finish(t, req)
	status, _ := f.store.PersonalStatus(context.Background(), f.persona, false)
	if status.RebuildRequested {
		t.Fatal("the rebuild request was not cleared")
	}
}

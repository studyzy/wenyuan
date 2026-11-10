//go:build pgx

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// Reading a member fact's entity names into the round.

// writeMemberEntity inserts one entity into a member's bank and posts the given
// unit to it, which is what a member's own retain leaves behind.
func (f *mergeFixture) writeMemberEntity(t *testing.T, member, unitID, name string) {
	t.Helper()
	ctx, err := WithSchema(context.Background(), member)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	bankID := tenant.ProjectBankID(member, f.projectID)
	var entityID string
	if err := f.db.QueryRowContext(ctx,
		"INSERT INTO "+quoteIdent(member)+".entities (bank_id, canonical_name) VALUES ($1,$2) RETURNING id::text",
		bankID, name).Scan(&entityID); err != nil {
		t.Fatalf("insert member entity: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		"INSERT INTO "+quoteIdent(member)+".unit_entities (unit_id, entity_id) VALUES ($1::uuid,$2::uuid)",
		unitID, entityID); err != nil {
		t.Fatalf("post member entity: %v", err)
	}
}

// readFreshEntityNames runs the member input read and returns the entity names
// each fact of the batch carries, keyed by the fact's text.
func (f *mergeFixture) readFreshEntityNames(t *testing.T, member string, limit int) map[string][]string {
	t.Helper()
	memberState := MergeProjectMember{
		TenantID:  member,
		ProjectID: f.projectID,
		Schema:    member,
	}
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	input, err := f.store.loadMemberInput(context.Background(), tx, memberState, limit, true)
	if err != nil {
		t.Fatalf("loadMemberInput: %v", err)
	}
	out := map[string][]string{}
	for _, fact := range input.Fresh {
		out[fact.Text] = fact.EntityNames
	}
	return out
}

// TestMergeInputCarriesEntityNames pins the read: a fact's entity names reach the
// round, so it can rebuild the team bank's own entities from them.
func TestMergeInputCarriesEntityNames(t *testing.T) {
	f := newMergeFixture(t)
	unitA := f.writeMemberFact(t, f.memberA, "the gateway shipped")
	f.writeMemberEntity(t, f.memberA, unitA, "支付网关")
	f.writeMemberEntity(t, f.memberA, unitA, "billing")

	names := f.readFreshEntityNames(t, f.memberA, 100)
	got := names["the gateway shipped"]
	if len(got) != 2 {
		t.Fatalf("entity names = %v, want two", got)
	}
	// The read is ordered by name, so the round's input is deterministic.
	if got[0] != "billing" || got[1] != "支付网关" {
		t.Fatalf("entity names = %v, want [billing 支付网关]", got)
	}
}

// TestMergeInputLeavesFactWithNoEntityNil pins the empty case: a fact that
// mentions nothing keeps a nil list rather than an empty one, so a reader can
// tell "mentions nothing" from "not read".
func TestMergeInputLeavesFactWithNoEntityNil(t *testing.T) {
	f := newMergeFixture(t)
	f.writeMemberFact(t, f.memberA, "a fact with no entity")

	names := f.readFreshEntityNames(t, f.memberA, 100)
	if got, present := names["a fact with no entity"]; present && got != nil {
		t.Fatalf("entity names = %v, want nil", got)
	}
}

// TestMergeInputKeepsOnlyTheBatchsEntities pins the batch scoping: entities of
// facts outside this batch are not carried into it.
func TestMergeInputKeepsOnlyTheBatchsEntities(t *testing.T) {
	f := newMergeFixture(t)
	first := f.writeMemberFact(t, f.memberA, "first fact")
	second := f.writeMemberFact(t, f.memberA, "second fact")
	f.writeMemberEntity(t, f.memberA, first, "only-first")
	f.writeMemberEntity(t, f.memberA, second, "only-second")

	names := f.readFreshEntityNames(t, f.memberA, 1)
	if len(names) != 1 {
		t.Fatalf("batch carried %d facts, want 1", len(names))
	}
	if got := names["first fact"]; len(got) != 1 || got[0] != "only-first" {
		t.Fatalf("first fact entity names = %v, want [only-first]", got)
	}
}

// TestMergeInputReadsTheSharedVocabulary pins what the read actually carries. A
// member's bank already holds one row per lowercased name — the schema's
// idx_entities_bank_lower_name makes it so — so "ABC" and "abc" cannot both
// exist there and the read cannot see two spellings of one identity.
//
// That schema constraint is what makes the team bank's exact-name rule agree
// with the member's: a name that reached the member's bank has already been
// folded the same way, so the team bank's identity is not a second, different
// judgement about the same fact.
func TestMergeInputReadsTheSharedVocabulary(t *testing.T) {
	f := newMergeFixture(t)
	unit := f.writeMemberFact(t, f.memberA, "two spellings")
	f.writeMemberEntity(t, f.memberA, unit, "ABC")

	names := f.readFreshEntityNames(t, f.memberA, 100)
	got := names["two spellings"]
	if len(got) != 1 || got[0] != "ABC" {
		t.Fatalf("entity names = %v, want the member's stored spelling [ABC]", got)
	}
}

// TestRebuildAdvancesItsCursorAcrossBatches pins how a backfill makes progress: a
// rebuild reads a bank larger than one batch over several rounds, and each round
// resumes where the last one stopped rather than restarting at the first fact.
//
// A rebuild that restarted every round would replay the same first batch forever
// — the shape a real backfill produced before AtStart: 500 facts carried, the rest
// never read, and the round timing out on every tick.
func TestRebuildAdvancesItsCursorAcrossBatches(t *testing.T) {
	f := newMergeFixture(t)
	// Distinct updated_at stamps, so the (updated_at, id) order the cursor reads
	// is the insertion order rather than an id tiebreak.
	for i := range 5 {
		f.writeMemberFact(t, f.memberA, fmt.Sprintf("fact %d", i))
	}
	f.staggerMemberFacts(t, f.memberA)

	state := MergeProjectMember{
		TenantID: f.memberA, ProjectID: f.projectID, Schema: f.memberA,
	}
	// A rebuild is requested, and RequestRebuild has put the cursor at the
	// beginning with '-infinity'.
	state.RebuildRequested = true
	state.State.RebuildRequested = true
	state.State.CursorUpdatedAt = mergeCursorEpoch.Add(-time.Hour)
	state.State.CursorUnitID = zeroUUID

	seen := map[string]bool{}
	cursor := state
	for round := range 5 {
		batch := f.readMemberBatch(t, cursor, 2)
		if len(batch) == 0 {
			break
		}
		for _, fact := range batch {
			if seen[fact.UnitID] {
				t.Fatalf("round %d reread fact %q: the rebuild restarted instead of resuming", round, fact.Text)
			}
			seen[fact.UnitID] = true
		}
		last := batch[len(batch)-1]
		cursor.State.CursorUpdatedAt = last.UpdatedAt
		cursor.State.CursorUnitID = last.UnitID
		cursor.RebuildRequested = true
	}
	if len(seen) != 5 {
		t.Fatalf("rebuild read %d distinct facts, want all 5", len(seen))
	}
}

// staggerMemberFacts rewrites a member's facts one second apart, so the cursor's
// (updated_at, id) resume point is unambiguous.
func (f *mergeFixture) staggerMemberFacts(t *testing.T, member string) {
	t.Helper()
	ctx, err := WithSchema(context.Background(), member)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, `
		UPDATE `+quoteIdent(member)+`.memory_units m
		SET updated_at = now() - (ordered.rn || ' seconds')::interval
		FROM (SELECT id, row_number() OVER (ORDER BY text) AS rn
		      FROM `+quoteIdent(member)+`.memory_units WHERE bank_id = $1) ordered
		WHERE m.id = ordered.id`,
		tenant.ProjectBankID(member, f.projectID)); err != nil {
		t.Fatalf("stagger facts: %v", err)
	}
}

// readMemberBatch reads one batch through the round's own input path.
func (f *mergeFixture) readMemberBatch(t *testing.T, member MergeProjectMember, limit int) []MergeFact {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	input, err := f.store.loadMemberInput(context.Background(), tx, member, limit, true)
	if err != nil {
		t.Fatalf("loadMemberInput: %v", err)
	}
	return input.Fresh
}

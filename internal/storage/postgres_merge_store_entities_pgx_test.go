//go:build pgx

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// Writing the team bank's own entities from the names a round carries.

// teamEntityNames reads the team bank's entities as lowercased name to id.
func (f *mergeFixture) teamEntityNames(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.db.QueryContext(f.teamSchemaCtx(t),
		"SELECT lower(canonical_name), id::text FROM "+quoteIdent(f.teamID)+".entities WHERE bank_id = $1",
		tenant.ProjectBankID(f.teamID, f.projectID))
	if err != nil {
		t.Fatalf("read team entities: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			t.Fatalf("scan team entity: %v", err)
		}
		out[name] = id
	}
	return out
}

// teamUnitEntityNames reads the entity names posted to one team fact text.
func (f *mergeFixture) teamUnitEntityNames(t *testing.T, text string) []string {
	t.Helper()
	rows, err := f.db.QueryContext(f.teamSchemaCtx(t), `
		SELECT e.canonical_name
		FROM `+quoteIdent(f.teamID)+`.memory_units u
		JOIN `+quoteIdent(f.teamID)+`.unit_entities ue ON ue.unit_id = u.id
		JOIN `+quoteIdent(f.teamID)+`.entities e ON e.id = ue.entity_id
		WHERE u.bank_id = $1 AND u.text = $2
		ORDER BY e.canonical_name`,
		tenant.ProjectBankID(f.teamID, f.projectID), text)
	if err != nil {
		t.Fatalf("read team postings: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan posting: %v", err)
		}
		names = append(names, name)
	}
	return names
}

// runRoundWithEntities runs one round carrying entity names for each fact.
//
// names is keyed by fact text, which is how these tests address a fact without
// threading a unit id through every call.
func (f *mergeFixture) runRoundWithEntities(t *testing.T, facts []string, names map[string][]string) {
	t.Helper()
	rows := make([]TeamFactRow, 0, len(facts))
	sources := map[string][]MergeSourceRow{}
	for _, text := range facts {
		unit := f.writeMemberFact(t, f.memberA, text)
		rows = append(rows, TeamFactRow{
			Text: text, MergeKey: tenant.MergeKey(text), SourceUnitID: unit,
			SourceTenant: f.memberA, EntityNames: names[text],
		})
		sources[f.memberA] = append(sources[f.memberA],
			MergeSourceRow{UnitID: unit, MergeKey: tenant.MergeKey(text)})
	}
	if !f.runRound(t, rows, sources) {
		t.Fatal("round did not run")
	}
}

// TestMergeRoundWritesTeamEntities is the feature: a carried fact's entity names
// become the team bank's own entity rows, and the team fact is posted to them.
func TestMergeRoundWritesTeamEntities(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"the gateway shipped"},
		map[string][]string{"the gateway shipped": {"支付网关", "billing"}})

	entities := f.teamEntityNames(t)
	if len(entities) != 2 {
		t.Fatalf("team entities = %v, want two", entities)
	}
	for _, name := range []string{"支付网关", "billing"} {
		if _, ok := entities[name]; !ok {
			t.Fatalf("team entities = %v, missing %q", entities, name)
		}
	}
	posted := f.teamUnitEntityNames(t, "the gateway shipped")
	if len(posted) != 2 {
		t.Fatalf("postings = %v, want both entities", posted)
	}
}

// TestMergeRoundFoldsOneNameAcrossMembers is the point of the feature: two members
// naming the same thing share one team entity.
func TestMergeRoundFoldsOneNameAcrossMembers(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "member A shipped the gateway")
	unitB := f.writeMemberFact(t, f.memberB, "member B fixed the gateway")
	key := tenant.MergeKey
	if !f.runRound(t,
		[]TeamFactRow{
			{Text: "member A shipped the gateway", MergeKey: key("member A shipped the gateway"), SourceUnitID: unitA, SourceTenant: f.memberA, EntityNames: []string{"支付网关"}},
			{Text: "member B fixed the gateway", MergeKey: key("member B fixed the gateway"), SourceUnitID: unitB, SourceTenant: f.memberB, EntityNames: []string{"支付网关"}},
		},
		map[string][]MergeSourceRow{
			f.memberA: {{UnitID: unitA, MergeKey: key("member A shipped the gateway")}},
			f.memberB: {{UnitID: unitB, MergeKey: key("member B fixed the gateway")}},
		}) {
		t.Fatal("round did not run")
	}
	if entities := f.teamEntityNames(t); len(entities) != 1 {
		t.Fatalf("team entities = %v, want one shared entity", entities)
	}
	for _, text := range []string{"member A shipped the gateway", "member B fixed the gateway"} {
		if posted := f.teamUnitEntityNames(t, text); len(posted) != 1 {
			t.Fatalf("postings for %q = %v, want one", text, posted)
		}
	}
}

// TestMergeRoundKeepsDifferentNamesApart is the rule that makes the merge safe: a
// merely similar name is not folded in, because a wrong merge cannot be undone.
func TestMergeRoundKeepsDifferentNamesApart(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"the gateway shipped"},
		map[string][]string{"the gateway shipped": {"支付网关", "支付系统"}})

	entities := f.teamEntityNames(t)
	if len(entities) != 2 {
		t.Fatalf("team entities = %v, want two distinct entities", entities)
	}
}

// TestMergeRoundPostsOneEntityPerName pins the duplicate within one fact: two
// spellings of one identity are one posting, not a rejected insert.
func TestMergeRoundPostsOneEntityPerName(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"one fact"},
		map[string][]string{"one fact": {"ABC", "abc"}})

	if entities := f.teamEntityNames(t); len(entities) != 1 {
		t.Fatalf("team entities = %v, want one", entities)
	}
	if posted := f.teamUnitEntityNames(t, "one fact"); len(posted) != 1 {
		t.Fatalf("postings = %v, want one", posted)
	}
}

// TestMergeRoundRecordsCooccurrences pins the pair a single fact witnesses, which
// is the edge the graph arm walks.
func TestMergeRoundRecordsCooccurrences(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"the gateway shipped"},
		map[string][]string{"the gateway shipped": {"支付网关", "billing"}})

	var count int
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT COALESCE(SUM(cooccurrence_count),0) FROM "+quoteIdent(f.teamID)+".entity_cooccurrences").Scan(&count); err != nil {
		t.Fatalf("read cooccurrences: %v", err)
	}
	if count != 1 {
		t.Fatalf("cooccurrence count = %d, want 1", count)
	}
}

// TestMergeRoundIsIdempotentForEntities pins the replay: running the same names
// again does not duplicate the entity, the posting or the pair.
func TestMergeRoundIsIdempotentForEntities(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	ctx := f.teamSchemaCtx(t)
	names := map[string][]string{"the gateway shipped": {"支付网关", "billing"}}
	f.runRoundWithEntities(t, []string{"the gateway shipped"}, names)

	targets := []mergeEntityTarget{{UnitID: f.teamUnitID(t, "the gateway shipped"), Names: names["the gateway shipped"]}}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := f.store.applyEntities(ctx, tx, quoteIdent(f.teamID), tenant.ProjectBankID(f.teamID, f.projectID), targets); err != nil {
		t.Fatalf("applyEntities again: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if entities := f.teamEntityNames(t); len(entities) != 2 {
		t.Fatalf("team entities = %v after a replay, want two", entities)
	}
	var postings, pairs int
	if err := f.db.QueryRowContext(ctx,
		"SELECT count(*) FROM "+quoteIdent(f.teamID)+".unit_entities").Scan(&postings); err != nil {
		t.Fatalf("count postings: %v", err)
	}
	if postings != 2 {
		t.Fatalf("postings = %d after a replay, want 2", postings)
	}
	if err := f.db.QueryRowContext(ctx,
		"SELECT count(*) FROM "+quoteIdent(f.teamID)+".entity_cooccurrences").Scan(&pairs); err != nil {
		t.Fatalf("count pairs: %v", err)
	}
	if pairs != 1 {
		t.Fatalf("pairs = %d after a replay, want 1", pairs)
	}
}

// teamUnitID is the team row one fact's text produced.
func (f *mergeFixture) teamUnitID(t *testing.T, text string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT id::text FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id = $1 AND text = $2",
		tenant.ProjectBankID(f.teamID, f.projectID), text).Scan(&id); err != nil {
		t.Fatalf("read team unit %q: %v", text, err)
	}
	return id
}

// TestMergeRoundQueuesEntitiesOfRemovedFacts pins the cleanup hand-off: when a
// round replaces a fact, the entities its postings named are queued for the
// maintenance sweep before the cascade removes the evidence.
func TestMergeRoundQueuesEntitiesOfRemovedFacts(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	ctx := f.teamSchemaCtx(t)
	f.runRoundWithEntities(t, []string{"the gateway shipped"},
		map[string][]string{"the gateway shipped": {"支付网关"}})

	rowID := f.teamUnitID(t, "the gateway shipped")
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := f.store.applyFacts(ctx, tx, FinishMergeRequest{
		BankID:  tenant.ProjectBankID(f.teamID, f.projectID),
		Replace: MergeReplaceResult{Remove: []TeamFactRow{{ID: rowID}}},
	}); err != nil {
		t.Fatalf("remove the fact: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var queued int
	if err := f.db.QueryRowContext(ctx,
		"SELECT count(*) FROM "+quoteIdent(f.teamID)+".entity_maintenance_queue WHERE bank_id = $1",
		tenant.ProjectBankID(f.teamID, f.projectID)).Scan(&queued); err != nil {
		t.Fatalf("count queue: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued entities = %d, want 1", queued)
	}
	if entities := f.teamEntityNames(t); len(entities) != 1 {
		t.Fatalf("entity row removed too early: %v", entities)
	}
}

// TestMergeRoundIgnoresUnusableNames pins that a name intake would drop never
// becomes a team entity, while the fact itself is still written.
func TestMergeRoundIgnoresUnusableNames(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"a fact"},
		map[string][]string{"a fact": {"", "   ", fmt.Sprintf("%0*d", 513, 0), "kept"}})

	entities := f.teamEntityNames(t)
	if len(entities) != 1 {
		t.Fatalf("team entities = %v, want only the kept name", entities)
	}
	if _, ok := entities["kept"]; !ok {
		t.Fatalf("team entities = %v, missing %q", entities, "kept")
	}
	if texts := f.teamFactTexts(t); len(texts) != 1 {
		t.Fatalf("team facts = %v, want the fact written regardless of its names", texts)
	}
}

// TestMergeRoundProofOfSpelling pins that the stored spelling is the member's:
// the unique index decides identity, not the stored text.
func TestMergeRoundProofOfSpelling(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"a fact"},
		map[string][]string{"a fact": {"ABC"}})

	var stored string
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT canonical_name FROM "+quoteIdent(f.teamID)+".entities WHERE bank_id = $1",
		tenant.ProjectBankID(f.teamID, f.projectID)).Scan(&stored); err != nil {
		t.Fatalf("read entity: %v", err)
	}
	if stored != "ABC" {
		t.Fatalf("stored name = %q, want the member's spelling", stored)
	}
}

// TestMergeRoundPostsAKeptFact pins the rebuild's job: a team row the round
// already holds is not rewritten, but its postings are still written.
//
// Without this a bank filled before entities were carried keeps rows whose
// entity names were never posted, and a rebuild — which reads every fact back and
// classifies them all as unchanged — would add nothing either. That is the shape
// the first deployment of this feature produced: 2025 team facts, no entities.
func TestMergeRoundPostsAKeptFact(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	text := "a fact that predates entities"
	f.runRoundWithEntities(t, []string{text}, map[string][]string{text: {"支付网关"}})

	// Strip the postings, the state a bank filled before this feature is in.
	ctx := f.teamSchemaCtx(t)
	if _, err := f.db.ExecContext(ctx, "DELETE FROM "+quoteIdent(f.teamID)+".unit_entities"); err != nil {
		t.Fatalf("clear postings: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, "DELETE FROM "+quoteIdent(f.teamID)+".entities"); err != nil {
		t.Fatalf("clear entities: %v", err)
	}

	// A rebuild reads the fact back, finds its row unchanged, and keeps it: the
	// write gets it as Keep, with the entity names but no text to rewrite.
	rowID := f.teamUnitID(t, text)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := f.store.applyFacts(ctx, tx, FinishMergeRequest{
		BankID: tenant.ProjectBankID(f.teamID, f.projectID),
		Replace: MergeReplaceResult{Keep: []TeamFactRow{
			{ID: rowID, EntityNames: []string{"支付网关"}},
		}},
	}); err != nil {
		t.Fatalf("apply kept fact: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if entities := f.teamEntityNames(t); len(entities) != 1 {
		t.Fatalf("team entities = %v after replaying a kept fact, want one", entities)
	}
}

// TestMergeRoundCountsARepeatedPairOncePerRound pins the co-occurrence upsert
// against a batch where two facts name the same two entities.
//
// A statement cannot upsert one row twice ("ON CONFLICT DO UPDATE command cannot
// affect row a second time"), so the pairs are folded per round before the
// insert. Without that fold the whole round fails, which is what the first real
// rebuild did: every round errored and the project never advanced.
func TestMergeRoundCountsARepeatedPairOncePerRound(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "first fact names both")
	unitB := f.writeMemberFact(t, f.memberB, "second fact names both")
	key := tenant.MergeKey
	textA, textB := "first fact names both", "second fact names both"
	if !f.runRound(t,
		[]TeamFactRow{
			{Text: textA, MergeKey: key(textA), SourceUnitID: unitA, SourceTenant: f.memberA, EntityNames: []string{"支付网关", "billing"}},
			{Text: textB, MergeKey: key(textB), SourceUnitID: unitB, SourceTenant: f.memberB, EntityNames: []string{"billing", "支付网关"}},
		},
		map[string][]MergeSourceRow{
			f.memberA: {{UnitID: unitA, MergeKey: key(textA)}},
			f.memberB: {{UnitID: unitB, MergeKey: key(textB)}},
		}) {
		t.Fatal("round did not run")
	}
	var count int
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT COALESCE(SUM(cooccurrence_count),0) FROM "+quoteIdent(f.teamID)+".entity_cooccurrences").Scan(&count); err != nil {
		t.Fatalf("read cooccurrences: %v", err)
	}
	if count != 2 {
		t.Fatalf("cooccurrence count = %d, want 2 (one per fact naming the pair)", count)
	}
}

// TestRebuildCompletingRoundClearsTheFlag pins how a replay ends.
//
// The round that completes a rebuild reads no new fact, so it carries no cursor —
// and a state update that only clears the flag alongside a cursor leaves it set.
// The next tick then replays the bank from the top again, forever.
func TestRebuildCompletingRoundClearsTheFlag(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"a fact"}, map[string][]string{"a fact": {"pay-gateway"}})
	if err := f.store.RequestRebuild(context.Background(), f.teamID, f.projectID); err != nil {
		t.Fatalf("request rebuild: %v", err)
	}
	var stamp time.Time
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(100, time.Minute), func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	// The completing round: nothing to write, no cursor, the flag to clear.
	if err := f.store.FinishMergeStanza(context.Background(), FinishMergeRequest{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
		Rebuild: true, RebuildDone: true, Reconcile: true,
		Members: []string{f.memberA, f.memberB},
	}); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}
	var requested bool
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT rebuild_requested FROM "+adminTable("merge_state")+" WHERE team_tenant_id=$1 AND project_id=$2",
		f.teamID, f.projectID).Scan(&requested); err != nil {
		t.Fatalf("read rebuild flag: %v", err)
	}
	if requested {
		t.Fatal("the completing round left rebuild_requested set, so the next tick replays the bank again")
	}
}

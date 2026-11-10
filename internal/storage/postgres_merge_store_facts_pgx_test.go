//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The merged fact kinds and the circuit breaker: what a round reads, what it
// writes, and when it must not run at all.

// mergeRoundOpts is a round's settings for these tests: both input kinds are
// read, which is the deployment default.
func mergeRoundOpts(batch int, staleAfter time.Duration) MergeRoundOptions {
	return MergeRoundOptions{Batch: batch, StaleAfter: staleAfter, IncludeExperience: true}
}

// writeMemberFactOfType inserts one fact of the given type into a member's bank.
func (f *mergeFixture) writeMemberFactOfType(t *testing.T, member, factType, text string) string {
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
		"INSERT INTO "+quoteIdent(member)+".memory_units (bank_id, text, fact_type) VALUES ($1,$2,$3) RETURNING id::text",
		bankID, text, factType).Scan(&id); err != nil {
		t.Fatalf("insert member fact: %v", err)
	}
	return id
}

// teamFactTypes reads the team bank's managed facts as text-to-type pairs.
func (f *mergeFixture) teamFactTypes(t *testing.T) map[string]string {
	t.Helper()
	ctx := f.teamSchemaCtx(t)
	rows, err := f.db.QueryContext(ctx,
		"SELECT text, fact_type FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id = $1 AND metadata->>'origin' = $2",
		tenant.ProjectBankID(f.teamID, f.projectID), mergeMetadataOrigin)
	if err != nil {
		t.Fatalf("read team facts: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var text, factType string
		if err := rows.Scan(&text, &factType); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[text] = factType
	}
	return out
}

// TestMergeStoreReadsOnlyCopiedFactTypes is the round's input scope: a member's
// world facts and experiences reach the round, each under its own kind, and their
// observations do not.
//
// An observation is a derived row that consolidation rewrites in place without
// moving `updated_at`, so a copy could never follow it. The team bank derives its
// own conclusions from the facts that are copied.
func TestMergeStoreReadsOnlyCopiedFactTypes(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	worldA := f.writeMemberFactOfType(t, f.memberA, "world", "The deploy uses port 15432.")
	expA := f.writeMemberFactOfType(t, f.memberA, "experience", "Alice moved the deploy to port 15432.")
	obsA := f.writeMemberFactOfType(t, f.memberA, "observation", "The deploy has migrated ports recently.")

	seen := map[string]string{}
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(100, time.Minute), func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				for _, fact := range member.Fresh {
					seen[fact.UnitID] = string(fact.FactType)
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	for unitID, want := range map[string]string{worldA: "world", expA: "experience"} {
		if got := seen[unitID]; got != want {
			t.Fatalf("fact %s read as %q, want %q", unitID, got, want)
		}
	}
	if _, ok := seen[obsA]; ok {
		t.Fatal("a member's observation reached the round; it must not")
	}
}

// TestMergeStoreExcludesExperienceWhenSwitchedOff pins the switch: with it off a
// member's experiences are not input, while their world facts still are.
func TestMergeStoreExcludesExperienceWhenSwitchedOff(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	worldA := f.writeMemberFactOfType(t, f.memberA, "world", "The deploy uses port 15432.")
	expA := f.writeMemberFactOfType(t, f.memberA, "experience", "Alice moved the deploy to port 15432.")
	obsA := f.writeMemberFactOfType(t, f.memberA, "observation", "The deploy has migrated ports recently.")

	seen := map[string]bool{}
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		MergeRoundOptions{Batch: 100, StaleAfter: time.Minute, IncludeExperience: false},
		func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				for _, fact := range member.Fresh {
					seen[fact.UnitID] = true
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if seen[expA] {
		t.Fatal("an experience fact reached the round while the switch was off")
	}
	if seen[obsA] {
		t.Fatal("an observation reached the round; it is never input")
	}
	if !seen[worldA] {
		t.Fatalf("world facts must stay input: seen=%v", seen)
	}
}

// TestMergeStoreWritesTeamFactsUnderTheirOwnKind pins the write: a team fact is
// stored under the kind of the member facts it was made from, so the team bank is
// not a pile of world facts.
func TestMergeStoreWritesTeamFactsUnderTheirOwnKind(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFactOfType(t, f.memberA, "experience", "Alice moved the deploy to port 15432.")
	key := tenant.MergeKey("Alice moved the deploy to port 15432.")
	if !f.runRound(t,
		[]TeamFactRow{{Text: "Alice moved the deploy to port 15432.", MergeKey: key, FactType: TypeExperience}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unitA, MergeKey: key}}}) {
		t.Fatal("round did not run")
	}
	if got := f.teamFactTypes(t)["Alice moved the deploy to port 15432."]; got != "experience" {
		t.Fatalf("team fact stored as %q, want experience", got)
	}
}

// TestRebuildReplaysFromTheStart pins the read a rebuild depends on: while the
// flag is set the round reads the member's bank from the beginning, so a fact
// behind the old cursor is seen again. Reading only past the cursor would leave
// every existing team fact standing after a withdrawal — the stale content the
// rebuild exists to remove.
func TestRebuildReplaysFromTheStart(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	first := f.writeMemberFact(t, f.memberA, "the oldest fact")
	// The second fact is newer than the cursor the first round will leave behind.
	time.Sleep(10 * time.Millisecond)
	second := f.writeMemberFact(t, f.memberA, "a newer fact")

	var secondAt time.Time
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT updated_at FROM "+quoteIdent(f.memberA)+".memory_units WHERE id = $1::uuid",
		second).Scan(&secondAt); err != nil {
		t.Fatalf("read second fact: %v", err)
	}
	// A rebuilt round whose cursor already stands at the end of the bank.
	var stamp time.Time
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		// A batch wider than the bank, so the round consumes everything and the
		// cursor ends at the newest fact rather than short of it.
		mergeRoundOpts(10, time.Minute), func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if err := f.store.FinishMergeStanza(context.Background(), FinishMergeRequest{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
		Replace: MergeReplaceResult{},
		Members: []string{f.memberA, f.memberB},
		Cursors: map[string]MergeCursor{
			f.memberA: {UpdatedAt: secondAt, UnitID: second},
		},
	}); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}
	// Ask for a rebuild: an operator starting one from scratch, though the cursor
	// already sits at the end of the bank. RequestRebuild is the whole request —
	// it sets the flag and rewinds the cursor together, because the flag alone
	// cannot say which round of a multi-batch replay this is.
	if err := f.store.RequestRebuild(context.Background(), f.teamID, f.projectID); err != nil {
		t.Fatalf("request rebuild: %v", err)
	}

	// The rebuilding round reads the whole bank, including the fact the cursor
	// had already passed.
	seen := map[string]bool{}
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(10, time.Minute), func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				if member.Member.TenantID != f.memberA {
					continue
				}
				for _, fact := range member.Fresh {
					seen[fact.UnitID] = true
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !seen[first] || !seen[second] {
		var curAt time.Time
		var curUnit string
		var rebuild bool
		_ = f.db.QueryRowContext(context.Background(),
			"SELECT cursor_updated_at, cursor_unit_id, rebuild_requested FROM "+adminTable("merge_state")+" WHERE team_tenant_id=$1 AND project_id=$2 AND tenant_id=$3",
			f.teamID, f.projectID, f.memberA).Scan(&curAt, &curUnit, &rebuild)
		t.Fatalf("the rebuild did not replay the whole bank: seen=%v cursor=%v/%s rebuild=%v first=%s second=%s",
			seen, curAt, curUnit, rebuild, first, second)
	}
}

// TestMergeStoreCarriesTags pins that a team fact keeps the member fact's tags.
// Tags drive recall's tag filter and fuzzy tag resolution, so a fact whose tags
// stop at the member's bank no longer answers a tag query for anyone else.
func TestMergeStoreCarriesTags(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unit := f.writeMemberFact(t, f.memberA, "the deploy uses port 15432")
	if _, err := f.db.ExecContext(context.Background(),
		"UPDATE "+quoteIdent(f.memberA)+".memory_units SET tags = $2::text[] WHERE id = $1::uuid",
		unit, "{deploy,config}"); err != nil {
		t.Fatalf("tag the member fact: %v", err)
	}

	cfgOpts := mergeRoundOpts(100, time.Minute)
	var tags []string
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID, cfgOpts,
		func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				for _, fact := range member.Fresh {
					tags = fact.Tags
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if len(tags) != 2 || tags[0] != "deploy" || tags[1] != "config" {
		t.Fatalf("member tags read as %v, want [deploy config]", tags)
	}
	if err := f.store.AbandonMergeStanza(context.Background(), f.teamID, f.projectID, MergeStateFailure{}); err != nil {
		t.Fatalf("abandon: %v", err)
	}

	key := tenant.MergeKey("the deploy uses port 15432")
	if !f.runRound(t, []TeamFactRow{{Text: "the deploy uses port 15432", MergeKey: key,
		SourceUnitID: unit, SourceTenant: f.memberA,
		FactType: TypeWorld, Tags: []string{"deploy", "config"}}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: key}}}) {
		t.Fatal("round did not run")
	}
	var stored string
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT COALESCE(tags,'{}'::text[])::text FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id=$1 AND metadata->>'source_unit_id'=$2",
		tenant.ProjectBankID(f.teamID, f.projectID), unit).Scan(&stored); err != nil {
		t.Fatalf("read team tags: %v", err)
	}
	if stored != "{deploy,config}" {
		t.Fatalf("team fact tags = %s, want {deploy,config}", stored)
	}
}

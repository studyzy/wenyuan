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

// mergeRoundOpts is a round's settings for these tests: every merged fact kind
// is input, which is the deployment default.
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
		"SELECT text, fact_type FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id = $1 AND metadata->>'merge_key' IS NOT NULL",
		tenant.ProjectBankID(f.teamID, f.projectID))
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

// TestMergeStoreReadsEveryMergedFactType is the round's input scope: a member's
// world facts, their experiences and their observations all reach the round, each
// carrying its own kind.
func TestMergeStoreReadsEveryMergedFactType(t *testing.T) {
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
	for unitID, want := range map[string]string{
		worldA: "world", expA: "experience", obsA: "observation",
	} {
		if got := seen[unitID]; got != want {
			t.Fatalf("fact %s read as %q, want %q", unitID, got, want)
		}
	}
}

// TestMergeStoreExcludesExperienceWhenSwitchedOff pins the switch: with it off a
// member's experiences are not input, while their world facts and observations
// still are.
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
	if !seen[worldA] || !seen[obsA] {
		t.Fatalf("world and observation must stay input: seen=%v", seen)
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
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unitA, MergeKey: key}}}, nil) {
		t.Fatal("round did not run")
	}
	if got := f.teamFactTypes(t)["Alice moved the deploy to port 15432."]; got != "experience" {
		t.Fatalf("team fact stored as %q, want experience", got)
	}
}

// TestRebuildContinuesFromTheCursor is the loop that made a large project look
// empty: a segmented rebuild that reads from the beginning every round always
// reads the same oldest batch, those facts are already in the team bank, nothing
// is written, and the cursor never moves. "Rebuild" means the replay reaches the
// end of the bank, not that every round restarts at the beginning.
func TestRebuildContinuesFromTheCursor(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	first := f.writeMemberFact(t, f.memberA, "the oldest fact")
	// The second fact is newer than the cursor the first round will leave behind.
	time.Sleep(10 * time.Millisecond)
	second := f.writeMemberFact(t, f.memberA, "a newer fact")

	// The cursor the first round leaves: the oldest fact's own timestamp.
	var firstAt time.Time
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT updated_at FROM "+quoteIdent(f.memberA)+".memory_units WHERE id = $1::uuid",
		first).Scan(&firstAt); err != nil {
		t.Fatalf("read first fact: %v", err)
	}
	// A rebuild that already replayed the first fact.
	var stamp time.Time
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(1, time.Minute), func(stanza MergeStanza) error {
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
			f.memberA: {UpdatedAt: firstAt, UnitID: first},
		},
		Rebuild: true, RebuildDone: false,
	}); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}
	// Set the flag without rewinding: a rebuild in progress continues from wherever
	// its cursor stands, which is what RequestRebuild's rewind is for a rebuild the
	// operator starts from scratch.
	if _, err := f.db.ExecContext(context.Background(),
		"UPDATE "+adminTable("merge_state")+" SET rebuild_requested = true WHERE team_tenant_id = $1 AND project_id = $2",
		f.teamID, f.projectID); err != nil {
		t.Fatalf("set rebuild flag: %v", err)
	}

	// The next round must read past the first fact, not restart at it.
	var seen []string
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(10, time.Minute), func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				if member.Member.TenantID != f.memberA {
					continue
				}
				for _, fact := range member.Fresh {
					seen = append(seen, fact.UnitID)
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	for _, id := range seen {
		if id == first {
			t.Fatal("the rebuild restarted at the oldest fact; it would never advance")
		}
	}
	found := false
	for _, id := range seen {
		if id == second {
			found = true
		}
	}
	if !found {
		t.Fatalf("the rebuild did not read past the cursor: %v", seen)
	}
}

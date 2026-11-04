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

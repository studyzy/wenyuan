//go:build pgx

package storage

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The graph arm on a project bank: entity-mediated expansion reaches a teammate's
// fact that no other arm could.
//
// The arm is driven directly, seeded with the id of one team fact. That is what
// the recall pipeline hands it — graphSeedsFrom picks the semantic arm's dense
// hits above the seed floor — so testing the arm this way verifies the expansion
// without needing a model to produce a query vector, and it fails for exactly the
// reason the feature exists: a team fact with no entity posting expands to
// nothing.

// graphArmHitTexts runs the graph arm from the given seeds and returns the texts
// it reached, which never include a seed itself.
func (f *mergeFixture) graphArmHitTexts(t *testing.T, seeds ...string) []string {
	t.Helper()
	store := NewPostgresStore(f.db, f.teamID)
	ctx, err := WithSchema(context.Background(), f.teamID)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	hits, err := store.graphArm(ctx, RecallQuery{
		BankID: tenant.ProjectBankID(f.teamID, f.projectID),
	}, nil, seeds)
	if err != nil {
		t.Fatalf("graphArm: %v", err)
	}
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.match.Text)
	}
	return out
}

// TestGraphArmReachesATeammateThroughASharedEntity is the feature's reason to
// exist: two members named the same thing, so a fact of one expands to a fact of
// the other that the query never matched.
func TestGraphArmReachesATeammateThroughASharedEntity(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "member A shipped the gateway")
	unitB := f.writeMemberFact(t, f.memberB, "member B fixed the gateway")
	key := tenant.MergeKey
	textA, textB := "member A shipped the gateway", "member B fixed the gateway"
	if !f.runRound(t,
		[]TeamFactRow{
			{Text: textA, MergeKey: key(textA), SourceUnitID: unitA, SourceTenant: f.memberA, EntityNames: []string{"支付网关"}},
			{Text: textB, MergeKey: key(textB), SourceUnitID: unitB, SourceTenant: f.memberB, EntityNames: []string{"支付网关"}},
		},
		map[string][]MergeSourceRow{
			f.memberA: {{UnitID: unitA, MergeKey: key(textA)}},
			f.memberB: {{UnitID: unitB, MergeKey: key(textB)}},
		}) {
		t.Fatal("round did not run")
	}
	seed := f.teamUnitID(t, textA)
	reached := f.graphArmHitTexts(t, seed)
	if len(reached) != 1 || reached[0] != textB {
		t.Fatalf("graph arm reached %v, want the teammate's fact %q", reached, textB)
	}
}

// TestGraphArmDoesNotReachAcrossSimilarNames is the safety half: a name that is
// merely similar is a different entity, so the arm does not connect them.
func TestGraphArmDoesNotReachAcrossSimilarNames(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	f.runRoundWithEntities(t, []string{"the gateway shipped", "the system shipped"},
		map[string][]string{
			"the gateway shipped": {"支付网关"},
			"the system shipped":  {"支付系统"},
		})
	seed := f.teamUnitID(t, "the gateway shipped")
	if reached := f.graphArmHitTexts(t, seed); len(reached) != 0 {
		t.Fatalf("graph arm reached %v across two different names, want nothing", reached)
	}
}

// TestGraphArmStillFollowsExplicitLinks pins that entity postings are an addition,
// not a replacement: the link half of the arm keeps working.
func TestGraphArmStillFollowsExplicitLinks(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unitA := f.writeMemberFact(t, f.memberA, "first")
	unitB := f.writeMemberFact(t, f.memberB, "second")
	key := tenant.MergeKey
	if !f.runRound(t,
		[]TeamFactRow{
			{Text: "first", MergeKey: key("first"), SourceUnitID: unitA, SourceTenant: f.memberA},
			{Text: "second", MergeKey: key("second"), SourceUnitID: unitB, SourceTenant: f.memberB},
		},
		map[string][]MergeSourceRow{
			f.memberA: {{UnitID: unitA, MergeKey: key("first")}},
			f.memberB: {{UnitID: unitB, MergeKey: key("second")}},
		}) {
		t.Fatal("round did not run")
	}
	from, to := f.teamUnitID(t, "first"), f.teamUnitID(t, "second")
	if err := NewPostgresStore(f.db, f.teamID).UpsertLink(f.teamSchemaCtx(t), MemoryLink{
		BankID:     tenant.ProjectBankID(f.teamID, f.projectID),
		FromUnitID: from, ToUnitID: to, LinkType: LinkTemporal, Weight: 1,
	}); err != nil {
		t.Fatalf("write link: %v", err)
	}
	reached := f.graphArmHitTexts(t, from)
	if len(reached) != 1 || reached[0] != "second" {
		t.Fatalf("graph arm reached %v through an explicit link, want %q", reached, "second")
	}
}

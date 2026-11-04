package projectmerge

import "testing"

// The carry pass copies facts and never asks a model. These tests pin the two
// rules that make it safe: every fact lands, and an exact repeat does not land
// twice.

func carryBatch() (present []TeamFact, fresh []MemberFact) {
	present = []TeamFact{
		{ID: "t1", Text: "The deploy uses port 15432.", MergeKey: MergeKey("The deploy uses port 15432.")},
	}
	fresh = []MemberFact{
		{TenantID: "alice", UnitID: "u0", Text: "The deploy uses port 15432.", FactType: FactTypeWorld},
		{TenantID: "alice", UnitID: "u1", Text: "Alice set the port.", FactType: FactTypeExperience},
		{TenantID: "alice", UnitID: "u2", Text: "The deploy moved recently.", FactType: FactTypeObservation},
	}
	return present, fresh
}

// TestPlanCarriesEveryFact is the property the whole design rests on: nothing the
// members hold is dropped.
func TestPlanCarriesEveryFact(t *testing.T) {
	present, fresh := carryBatch()
	plan := PlanBatch(present, fresh)
	// The first fresh fact repeats the bank's row, so it is the one that does not
	// need a second row; the other two are carried.
	if len(plan.Facts) != 2 {
		t.Fatalf("planned %d facts, want 2 (the repeat is skipped)", len(plan.Facts))
	}
	for _, planned := range plan.Facts {
		if planned.Fact.Text == "The deploy uses port 15432." {
			t.Fatal("a fact the bank already holds was planned again")
		}
	}
}

// TestPlanPreservesTextAndKind pins that the pass is a copy: the team fact is the
// member's own text under the member's own kind.
func TestPlanPreservesTextAndKind(t *testing.T) {
	_, fresh := carryBatch()
	plan := PlanBatch(nil, fresh)
	if len(plan.Facts) != len(fresh) {
		t.Fatalf("planned %d facts for %d inputs", len(plan.Facts), len(fresh))
	}
	for i, planned := range plan.Facts {
		if planned.Fact.Text != fresh[i].Text {
			t.Fatalf("fact %d text = %q, want %q", i, planned.Fact.Text, fresh[i].Text)
		}
		if planned.Fact.FactType != fresh[i].FactType {
			t.Fatalf("fact %d kind = %q, want %q", i, planned.Fact.FactType, fresh[i].FactType)
		}
	}
}

// TestPlanSkipsExactRepeatsWithinTheBatch pins text deduplication: the same text
// twice is one fact, and the comparison needs no model.
func TestPlanSkipsExactRepeatsWithinTheBatch(t *testing.T) {
	fresh := []MemberFact{
		{TenantID: "a", UnitID: "u0", Text: "same thing", FactType: FactTypeWorld},
		{TenantID: "b", UnitID: "u1", Text: "same thing", FactType: FactTypeWorld},
	}
	if plan := PlanBatch(nil, fresh); len(plan.Facts) != 1 {
		t.Fatalf("planned %d facts for one repeated text, want 1", len(plan.Facts))
	}
}

// TestPlanFoldsWhitespaceDifferences pins the comparison the merge key already
// defines: two facts separated only by spacing are the same fact, which is the
// rule a reader would apply too.
func TestPlanFoldsWhitespaceDifferences(t *testing.T) {
	fresh := []MemberFact{
		{TenantID: "a", UnitID: "u0", Text: "the deploy uses port 15432", FactType: FactTypeWorld},
		{TenantID: "a", UnitID: "u1", Text: "  the   deploy uses  port 15432  ", FactType: FactTypeWorld},
	}
	if plan := PlanBatch(nil, fresh); len(plan.Facts) != 1 {
		t.Fatalf("planned %d facts, want 1: spacing alone must not create a second fact", len(plan.Facts))
	}
}

// TestPlanKeepsFactsThatDifferInSubstance pins the other side: a near repeat that
// says something different is a different fact and must be carried.
func TestPlanKeepsFactsThatDifferInSubstance(t *testing.T) {
	fresh := []MemberFact{
		{TenantID: "a", UnitID: "u0", Text: "the deploy uses port 15432", FactType: FactTypeWorld},
		{TenantID: "a", UnitID: "u1", Text: "the deploy uses port 15433", FactType: FactTypeWorld},
	}
	if plan := PlanBatch(nil, fresh); len(plan.Facts) != 2 {
		t.Fatalf("planned %d facts, want both", len(plan.Facts))
	}
}

// TestPlanKeysOnTextNotKind pins that a fact keeps one row when a member's bank
// retypes it: identity is the text, which is what the team bank's merge key says.
func TestPlanKeysOnTextNotKind(t *testing.T) {
	present := []TeamFact{{ID: "t1", Text: "a conclusion", MergeKey: MergeKey("a conclusion")}}
	fresh := []MemberFact{{TenantID: "a", UnitID: "u0", Text: "a conclusion", FactType: FactTypeObservation}}
	if plan := PlanBatch(present, fresh); len(plan.Facts) != 0 {
		t.Fatalf("planned %d facts, want 0: the text is already in the bank", len(plan.Facts))
	}
}

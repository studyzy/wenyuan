package projectmerge

import (
	"testing"
	"time"
)

// The carry pass copies facts and never asks a model. These tests pin the rules
// that make it safe: every fact lands, an edited fact replaces its row instead of
// adding one, and two members' identical sentences stay two facts.

func carryBatch() (present map[string]TeamFact, fresh []MemberFact) {
	present = map[string]TeamFact{
		"u0": {
			ID: "t1", SourceUnitID: "u0", Text: "The deploy uses port 15432.",
			MergeKey: MergeKey("The deploy uses port 15432."), FactType: FactTypeWorld,
		},
	}
	fresh = []MemberFact{
		{TenantID: "alice", UnitID: "u0", Text: "The deploy uses port 15432.", FactType: FactTypeWorld},
		{TenantID: "alice", UnitID: "u1", Text: "Alice set the port.", FactType: FactTypeExperience},
		{TenantID: "alice", UnitID: "u2", Text: "The deploy moved recently.", FactType: FactTypeWorld},
	}
	return present, fresh
}

// TestPlanCarriesEveryNewFact is the property the whole design rests on: nothing a
// member holds is dropped.
func TestPlanCarriesEveryNewFact(t *testing.T) {
	present, fresh := carryBatch()
	plan := PlanBatch(present, fresh)
	// The first fresh fact is the one the bank already holds under the same
	// source unit, so it is the one that needs no write; the other two are new.
	if len(plan.Facts) != 2 {
		t.Fatalf("planned %d facts, want 2 (the carried one is skipped)", len(plan.Facts))
	}
	for _, planned := range plan.Facts {
		if planned.Fact.UnitID == "u0" {
			t.Fatal("a fact the bank already carries was planned again")
		}
	}
	if len(plan.Replaced) != 0 {
		t.Fatalf("replaced %d rows, want 0: nothing changed", len(plan.Replaced))
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

// TestPlanKeepsTwoMembersSameText pins that identity is the source unit rather
// than the text: two members saying the same sentence are two facts, as they are
// inside either member's own bank.
func TestPlanKeepsTwoMembersSameText(t *testing.T) {
	fresh := []MemberFact{
		{TenantID: "a", UnitID: "u0", Text: "same thing", FactType: FactTypeWorld},
		{TenantID: "b", UnitID: "u1", Text: "same thing", FactType: FactTypeWorld},
	}
	if plan := PlanBatch(nil, fresh); len(plan.Facts) != 2 {
		t.Fatalf("planned %d facts for two members' text, want 2", len(plan.Facts))
	}
}

// TestPlanSkipsAnUnchangedFact pins the idempotence: a fact whose row still says
// what it says is not rewritten, which is what keeps the row's id — and the
// observations resting on it.
func TestPlanSkipsAnUnchangedFact(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	present := map[string]TeamFact{
		"u0": {
			ID: "t1", SourceUnitID: "u0", Text: "same thing", FactType: FactTypeWorld,
			Tags: []string{"deploy", "ops"}, OccurredStart: &at, MentionedAt: &at,
		},
	}
	fresh := []MemberFact{{
		TenantID: "a", UnitID: "u0", Text: "same thing", FactType: FactTypeWorld,
		// Tag order is not substance.
		Tags: []string{"ops", "deploy"}, OccurredStart: &at, MentionedAt: &at,
	}}
	plan := PlanBatch(present, fresh)
	if len(plan.Facts) != 0 || len(plan.Replaced) != 0 {
		t.Fatalf("planned %d writes over %d replaced, want none", len(plan.Facts), len(plan.Replaced))
	}
}

// TestPlanReplacesAnEditedFact is the rule the old text key could not express: an
// edited fact keeps its unit id, so the round removes its old row rather than
// leaving both versions standing.
func TestPlanReplacesAnEditedFact(t *testing.T) {
	present := map[string]TeamFact{
		"u0": {
			ID: "t1", SourceUnitID: "u0", Text: "the deploy uses port 15432",
			MergeKey: MergeKey("the deploy uses port 15432"), FactType: FactTypeWorld,
		},
	}
	fresh := []MemberFact{
		{TenantID: "a", UnitID: "u0", Text: "the deploy uses port 15433", FactType: FactTypeWorld},
	}
	plan := PlanBatch(present, fresh)
	if len(plan.Facts) != 1 {
		t.Fatalf("planned %d facts, want the new text", len(plan.Facts))
	}
	if len(plan.Replaced) != 1 || plan.Replaced[0].ID != "t1" {
		t.Fatalf("replaced = %+v, want the row the old text produced", plan.Replaced)
	}
}

// TestPlanReplacesWhenOnlyMetadataChanged pins that a change to the kind, a tag,
// the context, the scopes or a date is a change to the fact, because the team row
// would otherwise answer with the member's old values.
func TestPlanReplacesWhenOnlyMetadataChanged(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	later := at.Add(24 * time.Hour)
	base := TeamFact{
		ID: "t1", SourceUnitID: "u0", Text: "the deploy uses port 15432", FactType: FactTypeWorld,
		Tags: []string{"deploy"}, Context: "a runbook", MentionedAt: &at,
	}
	cases := []struct {
		name  string
		row   TeamFact
		fresh MemberFact
	}{
		{
			name:  "kind",
			row:   base,
			fresh: MemberFact{UnitID: "u0", Text: base.Text, FactType: FactTypeExperience, Tags: base.Tags, Context: base.Context, MentionedAt: &at},
		},
		{
			name:  "tag",
			row:   base,
			fresh: MemberFact{UnitID: "u0", Text: base.Text, FactType: FactTypeWorld, Tags: []string{"deploy", "ports"}, Context: base.Context, MentionedAt: &at},
		},
		{
			name:  "context",
			row:   base,
			fresh: MemberFact{UnitID: "u0", Text: base.Text, FactType: FactTypeWorld, Tags: base.Tags, Context: "a different runbook", MentionedAt: &at},
		},
		{
			name:  "scopes",
			row:   TeamFact{ID: "t1", SourceUnitID: "u0", Text: base.Text, FactType: FactTypeWorld, Tags: base.Tags, Context: base.Context, MentionedAt: &at, ObservationScopes: []byte(`["a"]`)},
			fresh: MemberFact{UnitID: "u0", Text: base.Text, FactType: FactTypeWorld, Tags: base.Tags, Context: base.Context, MentionedAt: &at, ObservationScopes: []byte(`["b"]`)},
		},
		{
			name:  "date",
			row:   base,
			fresh: MemberFact{UnitID: "u0", Text: base.Text, FactType: FactTypeWorld, Tags: base.Tags, Context: base.Context, MentionedAt: &later},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanBatch(map[string]TeamFact{"u0": tc.row}, []MemberFact{tc.fresh})
			if len(plan.Replaced) != 1 || len(plan.Facts) != 1 {
				t.Fatalf("planned %d writes over %d replaced, want one of each", len(plan.Facts), len(plan.Replaced))
			}
		})
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

// TestPlanKeysOnSourceUnitNotText pins that a fact keeps its row even when
// another member's identical text is already in the bank: the rows mirror
// different sources.
func TestPlanKeysOnSourceUnitNotText(t *testing.T) {
	present := map[string]TeamFact{
		"u0": {ID: "t1", SourceUnitID: "u0", Text: "a conclusion", FactType: FactTypeWorld},
	}
	fresh := []MemberFact{{TenantID: "b", UnitID: "u9", Text: "a conclusion", FactType: FactTypeWorld}}
	if plan := PlanBatch(present, fresh); len(plan.Facts) != 1 {
		t.Fatalf("planned %d facts, want 1: the rows mirror different sources", len(plan.Facts))
	}
}

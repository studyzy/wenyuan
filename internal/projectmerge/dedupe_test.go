package projectmerge

import (
	"errors"
	"strings"
	"testing"
)

// The dedupe pass replaced a summarising one that lost facts: 1940 member facts
// produced 121 team facts because the model was asked to merge duplicates, and
// compression is the shortest way to do that. These tests pin the property that
// made the replacement necessary: every fact is carried, and the model's only
// lever is a label.

func dedupeBatch() Batch {
	return Batch{
		Present: []TeamFact{
			{ID: "t1", Text: "The deploy uses port 15432.", MergeKey: MergeKey("The deploy uses port 15432.")},
		},
		Fresh: []MemberFact{
			{TenantID: "alice", UnitID: "u0", Text: "The deploy uses port 15432.", FactType: FactTypeWorld},
			{TenantID: "alice", UnitID: "u1", Text: "Alice set the port.", FactType: FactTypeExperience},
			{TenantID: "alice", UnitID: "u2", Text: "The deploy moved recently.", FactType: FactTypeObservation},
		},
	}
}

// TestPlanCarriesEveryFact is the property the pass exists for: nothing is lost.
func TestPlanCarriesEveryFact(t *testing.T) {
	batch := dedupeBatch()
	plan := PlanBatch(batch, nil)
	if len(plan.Facts) != len(batch.Fresh) {
		t.Fatalf("planned %d facts for %d inputs", len(plan.Facts), len(batch.Fresh))
	}
	for i, planned := range plan.Facts {
		if planned.Fact.Text != batch.Fresh[i].Text {
			t.Fatalf("fact %d text = %q, want the member's own text %q",
				i, planned.Fact.Text, batch.Fresh[i].Text)
		}
		if planned.Fact.FactType != batch.Fresh[i].FactType {
			t.Fatalf("fact %d type = %q, want %q", i, planned.Fact.FactType, batch.Fresh[i].FactType)
		}
	}
}

// TestPlanCanOnlyLabelRuntime pins that the model cannot remove a fact, however
// wrong it is: a labelled duplicate is still planned.
func TestPlanCanOnlyLabelRuntime(t *testing.T) {
	plan := PlanBatch(dedupeBatch(), []Answer{{Fresh: 0, DuplicateOf: 0}})
	if len(plan.Facts) != 3 {
		t.Fatalf("planned %d facts, want all 3 — a duplicate label must not drop a fact", len(plan.Facts))
	}
	if plan.Facts[0].DuplicateOf != MergeKey("The deploy uses port 15432.") {
		t.Fatalf("duplicate label = %q, want the bank fact's key", plan.Facts[0].DuplicateOf)
	}
}

// TestPlanSpotsIdenticalTextWithoutAModel pins that the cheapest duplicate needs
// no model at all: the same text twice is the same fact twice.
func TestPlanSpotsIdenticalTextWithoutAModel(t *testing.T) {
	batch := Batch{Fresh: []MemberFact{
		{TenantID: "a", UnitID: "u0", Text: "same thing", FactType: FactTypeWorld},
		{TenantID: "a", UnitID: "u1", Text: "  same   thing  ", FactType: FactTypeWorld},
	}}
	plan := PlanBatch(batch, nil)
	if plan.Facts[0].DuplicateOf != "" {
		t.Fatal("the first occurrence must not be labelled a duplicate")
	}
	if plan.Facts[1].DuplicateOf != MergeKey("same thing") {
		t.Fatalf("second occurrence label = %q, want the first's key", plan.Facts[1].DuplicateOf)
	}
}

// TestParseAnswerAcceptsAnEmptyList pins that "nothing repeats" is a normal
// answer. The summarising pass treated an empty reply as an error because it
// would have emptied the bank; here an empty list changes nothing.
func TestParseAnswerAcceptsAnEmptyList(t *testing.T) {
	got, err := ParseAnswer(`{"duplicates": []}`, 3, 1)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d answers, want none", len(got))
	}
}

func TestParseAnswerReadsDuplicates(t *testing.T) {
	got, err := ParseAnswer(`{"duplicates":[{"fresh":2,"duplicate_of":0},{"fresh":1,"duplicate_of":-1}]}`, 3, 2)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d answers, want 2", len(got))
	}
	if got[0].Fresh != 2 || got[0].DuplicateOf != 0 {
		t.Fatalf("first = %+v, want fresh 2 of present 0", got[0])
	}
	if got[1].DuplicateOf != -1 {
		t.Fatalf("second duplicate_of = %d, want -1 (another fresh fact)", got[1].DuplicateOf)
	}
}

// TestParseAnswerRecoversJSONFromProse pins the tolerance that stops a round from
// being wasted on a model that answers in the right shape but the wrong envelope.
// It is what turned one project's fail count into the eighties.
func TestParseAnswerRecoversJSONFromProse(t *testing.T) {
	for _, raw := range []string{
		`Duplicates: {"duplicates":[]}`,
		`{"duplicates":[]} That is all.`,
		"```json\n{\"duplicates\":[]}\n```",
	} {
		if _, err := ParseAnswer(raw, 3, 1); err != nil {
			t.Fatalf("ParseAnswer(%q): %v", raw, err)
		}
	}
}

func TestParseAnswerRejectsBadIndexes(t *testing.T) {
	cases := []string{
		`{"duplicates":[{"fresh":9,"duplicate_of":0}]}`,
		`{"duplicates":[{"fresh":-1,"duplicate_of":0}]}`,
		`not json at all`,
		``,
	}
	for _, raw := range cases {
		if _, err := ParseAnswer(raw, 3, 1); !errors.Is(err, ErrAnswerShape) {
			t.Fatalf("ParseAnswer(%q) err = %v, want ErrAnswerShape", raw, err)
		}
	}
}

// TestParseAnswerIgnoresAnUnusablePresentIndex pins the asymmetry: an unusable
// fresh index is a shape failure (the answer names a fact it was not given), but
// an unusable present index only means the label cannot be honoured. Every fact is
// written either way, so dropping the label is right and failing the round is not.
func TestParseAnswerIgnoresAnUnusablePresentIndex(t *testing.T) {
	got, err := ParseAnswer(`{"duplicates":[{"fresh":0,"duplicate_of":9}]}`, 3, 1)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d answers, want the unusable label dropped", len(got))
	}
}

// TestPromptForbidsRewriting pins that the model is never asked to produce text:
// producing text is what allowed the compression this pass replaces.
func TestPromptForbidsRewriting(t *testing.T) {
	all := Prompt(dedupeBatch().Present, dedupeBatch().Fresh)[0].Content
	for _, want := range []string{
		"Report only the new facts whose meaning is already stated",
		"Never rewrite, summarise or merge any text",
		"an unreported duplicate costs a repeated line",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Produce the") {
		t.Fatalf("prompt still asks the model to produce the content:\n%s", all)
	}
}

func TestPromptNumbersBothSides(t *testing.T) {
	all := Prompt(dedupeBatch().Present, dedupeBatch().Fresh)[1].Content
	if !strings.Contains(all, "[P0] The deploy uses port 15432.") {
		t.Fatalf("existing fact lost its P0 label:\n%s", all)
	}
	if !strings.Contains(all, "[F1] Alice set the port.") {
		t.Fatalf("fresh fact lost its F1 label:\n%s", all)
	}
}

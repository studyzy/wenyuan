package projectmerge

import (
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

func TestDiff(t *testing.T) {
	previous := []TeamFact{
		{ID: "u1", Text: "alpha"},
		{ID: "u2", Text: "beta"},
		{ID: "u3", Text: "gamma"},
	}
	for i := range previous {
		previous[i].MergeKey = MergeKey(previous[i].Text)
	}

	t.Run("keeps removes and inserts", func(t *testing.T) {
		// alpha survives by wording, beta is gone, gamma is reworded, delta is new.
		got := Diff(previous, []SummarizedAnswer{
			{Text: "alpha", Sources: []int{0}},
			{Text: "gamma, reworded", Sources: []int{1}},
			{Text: "delta", Sources: []int{2}},
		}, nil)
		if len(got.Keep) != 1 || got.Keep[0].ID != "u1" {
			t.Fatalf("Keep = %+v, want the untouched alpha row", got.Keep)
		}
		if len(got.Remove) != 2 {
			t.Fatalf("Remove = %+v, want beta and the old gamma", got.Remove)
		}
		if len(got.Insert) != 2 {
			t.Fatalf("Insert = %+v, want the new gamma and delta", got.Insert)
		}
		for _, fact := range got.Insert {
			if fact.ID != "" {
				t.Fatalf("an inserted fact must carry no id, got %q", fact.ID)
			}
		}
	})

	t.Run("whitespace only rewording keeps the row", func(t *testing.T) {
		got := Diff(previous, []SummarizedAnswer{
			{Text: "alpha", Sources: []int{0}},
			{Text: "  beta  ", Sources: []int{1}},
			{Text: "gamma", Sources: []int{2}},
		}, nil)
		if len(got.Remove) != 0 || len(got.Insert) != 0 {
			t.Fatalf("spacing alone must not change any row: remove=%+v insert=%+v", got.Remove, got.Insert)
		}
		if len(got.Keep) != 3 {
			t.Fatalf("Keep = %d facts, want 3", len(got.Keep))
		}
		// The kept text is the stored text, not the answer's spacing.
		for i, fact := range got.Keep {
			if fact.Text != previous[i].Text {
				t.Fatalf("kept fact %d text = %q, want the stored %q", i, fact.Text, previous[i].Text)
			}
		}
	})

	t.Run("duplicate restatement unions its sources", func(t *testing.T) {
		got := Diff(nil, []SummarizedAnswer{
			{Text: "alpha", Sources: []int{0}},
			{Text: "alpha ", Sources: []int{1, 0}},
		}, nil)
		if len(got.Insert) != 1 {
			t.Fatalf("Insert = %+v, want one fact", got.Insert)
		}
		want := []int{0, 1}
		gotSources := got.Sources[MergeKey("alpha")]
		if len(gotSources) != len(want) {
			t.Fatalf("sources = %v, want %v", gotSources, want)
		}
		for i := range want {
			if gotSources[i] != want[i] {
				t.Fatalf("sources = %v, want %v", gotSources, want)
			}
		}
	})

	t.Run("a previous fact absent from the answer is removed", func(t *testing.T) {
		got := Diff(previous, []SummarizedAnswer{{Text: "alpha", Sources: []int{0}}}, nil)
		if len(got.Remove) != 2 {
			t.Fatalf("Remove = %+v, want beta and gamma", got.Remove)
		}
	})

	t.Run("empty answer removes everything", func(t *testing.T) {
		got := Diff(previous, nil, nil)
		if len(got.Keep) != 0 || len(got.Insert) != 0 || len(got.Remove) != 3 {
			t.Fatalf("got keep=%d insert=%d remove=%d, want 0/0/3",
				len(got.Keep), len(got.Insert), len(got.Remove))
		}
	})
}

// TestSourceMergeKeysRecordsDroppedInputs is the ledger guarantee: an input the
// answer did not cite is recorded as dropped, so its later deletion cannot force
// a rebuild, while a cited one records the fact it supports.
func TestDiffTypesAnInsertFromItsSources(t *testing.T) {
	fresh := []MemberFact{
		{TenantID: "a", UnitID: "u1", Text: "the deploy uses port 15432", FactType: FactTypeWorld},
		{TenantID: "a", UnitID: "u2", Text: "alice moved the deploy", FactType: FactTypeExperience},
		{TenantID: "a", UnitID: "u3", Text: "the deploy migrated recently", FactType: FactTypeObservation},
		{TenantID: "a", UnitID: "u4", Text: "no declared kind"},
	}
	cases := []struct {
		name   string
		answer SummarizedAnswer
		want   string
	}{
		{
			name:   "a world source makes a world fact",
			answer: SummarizedAnswer{Text: "alpha", Sources: []int{0}},
			want:   FactTypeWorld,
		},
		{
			name:   "an experience source makes an experience fact",
			answer: SummarizedAnswer{Text: "beta", Sources: []int{1}},
			want:   FactTypeExperience,
		},
		{
			name:   "an observation source makes an observation fact",
			answer: SummarizedAnswer{Text: "gamma", Sources: []int{2}},
			want:   FactTypeObservation,
		},
		{
			name:   "a majority of the sources decides",
			answer: SummarizedAnswer{Text: "delta", Sources: []int{1, 1, 0}},
			want:   FactTypeExperience,
		},
		{
			name:   "a tie falls to world",
			answer: SummarizedAnswer{Text: "epsilon", Sources: []int{0, 1}},
			want:   FactTypeWorld,
		},
		{
			name:   "no sources is world",
			answer: SummarizedAnswer{Text: "zeta", Sources: nil},
			want:   FactTypeWorld,
		},
		{
			name:   "an undeclared kind is world",
			answer: SummarizedAnswer{Text: "eta", Sources: []int{3}},
			want:   FactTypeWorld,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(nil, []SummarizedAnswer{tc.answer}, fresh)
			if len(got.Insert) != 1 {
				t.Fatalf("Insert = %+v, want one fact", got.Insert)
			}
			if got.Insert[0].FactType != tc.want {
				t.Fatalf("FactType = %q, want %q", got.Insert[0].FactType, tc.want)
			}
		})
	}
}

// TestDiffKeepsASurvivingFactsKind pins the other side: a fact whose wording
// survives keeps the row's stored kind, so a round cannot retype it by accident.
func TestDiffKeepsASurvivingFactsKind(t *testing.T) {
	previous := []TeamFact{{ID: "u1", Text: "alpha", MergeKey: MergeKey("alpha"), FactType: FactTypeObservation}}
	got := Diff(previous, []SummarizedAnswer{{Text: "alpha", Sources: []int{0}}},
		[]MemberFact{{TenantID: "a", Text: "unrelated", FactType: FactTypeWorld}})
	if len(got.Keep) != 1 {
		t.Fatalf("Keep = %+v, want the surviving row", got.Keep)
	}
	if got.Keep[0].FactType != FactTypeObservation {
		t.Fatalf("kept kind = %q, want the stored observation", got.Keep[0].FactType)
	}
}

// TestPromptGroupsFactsByKind pins that the prompt says which kind each fact is:
// the three inputs arrive as one flat list, and a model told only the text cannot
// tell what a member knows from what they did.
func TestNormalizeAndMergeKeyAgreeWithTenant(t *testing.T) {
	for _, text := range []string{"alpha", "  alpha  beta ", "Alpha, Beta."} {
		if Normalize(text) != tenant.Normalize(text) {
			t.Fatalf("Normalize(%q) = %q, want tenant.Normalize %q", text, Normalize(text), tenant.Normalize(text))
		}
		if MergeKey(text) != tenant.MergeKey(text) {
			t.Fatalf("MergeKey(%q) = %q, want tenant.MergeKey %q", text, MergeKey(text), tenant.MergeKey(text))
		}
	}
}

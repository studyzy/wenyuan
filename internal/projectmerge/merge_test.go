package projectmerge

import (
	"errors"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		freshCount int
		want       []Answer
		wantErr    error
	}{
		{
			name:       "single fact with one source",
			raw:        `{"facts":[{"text":"A uses port 15432.","sources":[0]}]}`,
			freshCount: 2,
			want:       []Answer{{Text: "A uses port 15432.", Sources: []int{0}}},
		},
		{
			name:       "several facts and sources",
			raw:        `{"facts":[{"text":"alpha","sources":[0,2]},{"text":"beta","sources":[1]}]}`,
			freshCount: 3,
			want:       []Answer{{Text: "alpha", Sources: []int{0, 2}}, {Text: "beta", Sources: []int{1}}},
		},
		{
			name:       "fact with no sources is allowed",
			raw:        `{"facts":[{"text":"alpha","sources":[]}]}`,
			freshCount: 1,
			want:       []Answer{{Text: "alpha", Sources: []int{}}},
		},
		{
			name:       "text is normalized",
			raw:        `{"facts":[{"text":"  alpha   beta  ","sources":[0]}]}`,
			freshCount: 1,
			want:       []Answer{{Text: "alpha beta", Sources: []int{0}}},
		},
		{
			name:       "fenced reply is accepted",
			raw:        "```json\n{\"facts\":[{\"text\":\"alpha\",\"sources\":[0]}]}\n```",
			freshCount: 1,
			want:       []Answer{{Text: "alpha", Sources: []int{0}}},
		},
		{
			name:       "not json is a shape failure",
			raw:        "I merged the facts for you!",
			freshCount: 1,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "empty reply is a shape failure",
			raw:        "   ",
			freshCount: 1,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "missing facts member is a shape failure",
			raw:        `{"result":[]}`,
			freshCount: 1,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "no facts is its own error",
			raw:        `{"facts":[]}`,
			freshCount: 1,
			wantErr:    ErrAnswerEmpty,
		},
		{
			name:       "out of range source is a shape failure",
			raw:        `{"facts":[{"text":"alpha","sources":[5]}]}`,
			freshCount: 2,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "negative source is a shape failure",
			raw:        `{"facts":[{"text":"alpha","sources":[-1]}]}`,
			freshCount: 2,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "blank fact text is a shape failure",
			raw:        `{"facts":[{"text":"   ","sources":[0]}]}`,
			freshCount: 1,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "bare array is a shape failure",
			raw:        `[{"text":"alpha"}]`,
			freshCount: 1,
			wantErr:    ErrAnswerShape,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAnswer(tc.raw, tc.freshCount)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseAnswer error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAnswer: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d answers, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].Text != tc.want[i].Text {
					t.Fatalf("answer %d text = %q, want %q", i, got[i].Text, tc.want[i].Text)
				}
				if len(got[i].Sources) != len(tc.want[i].Sources) {
					t.Fatalf("answer %d sources = %v, want %v", i, got[i].Sources, tc.want[i].Sources)
				}
				for j := range got[i].Sources {
					if got[i].Sources[j] != tc.want[i].Sources[j] {
						t.Fatalf("answer %d sources = %v, want %v", i, got[i].Sources, tc.want[i].Sources)
					}
				}
			}
		})
	}
}

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
		got := Diff(previous, []Answer{
			{Text: "alpha", Sources: []int{0}},
			{Text: "gamma, reworded", Sources: []int{1}},
			{Text: "delta", Sources: []int{2}},
		})
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
		got := Diff(previous, []Answer{
			{Text: "alpha", Sources: []int{0}},
			{Text: "  beta  ", Sources: []int{1}},
			{Text: "gamma", Sources: []int{2}},
		})
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
		got := Diff(nil, []Answer{
			{Text: "alpha", Sources: []int{0}},
			{Text: "alpha ", Sources: []int{1, 0}},
		})
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
		got := Diff(previous, []Answer{{Text: "alpha", Sources: []int{0}}})
		if len(got.Remove) != 2 {
			t.Fatalf("Remove = %+v, want beta and gamma", got.Remove)
		}
	})

	t.Run("empty answer removes everything", func(t *testing.T) {
		got := Diff(previous, nil)
		if len(got.Keep) != 0 || len(got.Insert) != 0 || len(got.Remove) != 3 {
			t.Fatalf("got keep=%d insert=%d remove=%d, want 0/0/3",
				len(got.Keep), len(got.Insert), len(got.Remove))
		}
	})
}

// TestSourceMergeKeysRecordsDroppedInputs is the ledger guarantee: an input the
// answer did not cite is recorded as dropped, so its later deletion cannot force
// a rebuild, while a cited one records the fact it supports.
func TestSourceMergeKeysRecordsDroppedInputs(t *testing.T) {
	got := Diff(nil, []Answer{{Text: "alpha", Sources: []int{0, 2}}})
	keys := got.SourceMergeKeys(4)
	alphaKey := MergeKey("alpha")
	for _, index := range []int{0, 2} {
		if len(keys[index]) != 1 || keys[index][0] != alphaKey {
			t.Fatalf("source %d = %v, want [%s]", index, keys[index], alphaKey)
		}
	}
	for _, index := range []int{1, 3} {
		if len(keys[index]) != 1 || keys[index][0] != DroppedMergeKey {
			t.Fatalf("uncited source %d = %v, want [%s]", index, keys[index], DroppedMergeKey)
		}
	}
}

func TestPromptCarriesEveryPreviousFact(t *testing.T) {
	previous := []TeamFact{{ID: "u1", Text: "alpha fact"}, {ID: "u2", Text: "beta fact"}}
	fresh := []MemberFact{
		{TenantID: "member_a", UnitID: "x1", Text: "gamma fact"},
		{TenantID: "member_b", UnitID: "x2", Text: "delta fact"},
	}
	messages := Prompt("Ship the payments API.", previous, fresh)
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want a system and a user turn", len(messages))
	}
	var body strings.Builder
	for _, message := range messages {
		body.WriteString(message.Content)
		body.WriteString("\n")
	}
	all := body.String()
	for _, want := range []string{
		"alpha fact", "beta fact", "gamma fact", "delta fact",
		"[P0]", "[P1]", "[F0]", "[F1]",
		"Ship the payments API.",
		"member_a", "member_b",
		`sources`,
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, all)
		}
	}
}

func TestPromptWithNoHistoryOrNoInput(t *testing.T) {
	messages := Prompt("", nil, nil)
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	all := messages[0].Content + messages[1].Content
	if !strings.Contains(all, "(none yet)") {
		t.Fatalf("prompt does not say there are no previous facts:\n%s", all)
	}
	if !strings.Contains(all, "(none)") {
		t.Fatalf("prompt does not say there are no new facts:\n%s", all)
	}
}

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

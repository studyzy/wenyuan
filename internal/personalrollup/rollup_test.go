package personalrollup

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		freshCount int
		wantLen    int
		wantErr    error
	}{
		{
			name:       "plain answer",
			raw:        `{"facts":[{"text":"deploy uses port 15432","sources":[0],"topics":["deploy"]}]}`,
			freshCount: 1,
			wantLen:    1,
		},
		{
			name:       "fenced answer",
			raw:        "```json\n{\"facts\":[{\"text\":\"a\",\"sources\":[]}]}\n```",
			freshCount: 0,
			wantLen:    1,
		},
		{
			name:    "not json",
			raw:     "I think the answer is...",
			wantErr: ErrAnswerShape,
		},
		{
			name:    "missing facts member",
			raw:     `{"items":[]}`,
			wantErr: ErrAnswerShape,
		},
		{
			name:    "empty facts",
			raw:     `{"facts":[]}`,
			wantErr: ErrAnswerEmpty,
		},
		{
			name:       "source index out of range",
			raw:        `{"facts":[{"text":"a","sources":[3]}]}`,
			freshCount: 2,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "negative source index",
			raw:        `{"facts":[{"text":"a","sources":[-1]}]}`,
			freshCount: 2,
			wantErr:    ErrAnswerShape,
		},
		{
			name:       "blank text",
			raw:        `{"facts":[{"text":"   ","sources":[]}]}`,
			freshCount: 1,
			wantErr:    ErrAnswerShape,
		},
		{
			name:    "empty reply",
			raw:     "   ",
			wantErr: ErrAnswerShape,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers, err := ParseAnswer(tc.raw, tc.freshCount)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseAnswer error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAnswer: %v", err)
			}
			if len(answers) != tc.wantLen {
				t.Fatalf("answers = %d, want %d", len(answers), tc.wantLen)
			}
		})
	}
}

// TestParseAnswerNormalizesTopics pins that a topic's spelling is folded, so
// "Postgres" and " postgres " are one vocabulary entry rather than two that look
// identical in a directory.
func TestParseAnswerNormalizesTopics(t *testing.T) {
	answers, err := ParseAnswer(
		`{"facts":[{"text":"a","sources":[0],"topics":[" Postgres ", "POSTGRES", "Deploy"]}]}`, 1)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	got := answers[0].Topics
	if len(got) != 2 || got[0] != "postgres" || got[1] != "deploy" {
		t.Fatalf("topics = %v, want [postgres deploy]", got)
	}
}

func TestDiffClassifies(t *testing.T) {
	previous := []PreviousFact{
		{ID: "1", Text: "deploy uses port 15432"},
		{ID: "2", Text: "the api lives in services/orders"},
	}
	answers := []Answer{
		{Text: "deploy uses port 15432", Sources: []int{0}},
		{Text: "the api lives in services/orders", Sources: []int{1}},
		{Text: "postgres runs in Docker on 5432", Sources: []int{2}},
	}
	result := Diff(previous, answers)
	if len(result.Keep) != 2 {
		t.Fatalf("keep = %d, want 2", len(result.Keep))
	}
	if len(result.Remove) != 0 {
		t.Fatalf("remove = %d, want 0", len(result.Remove))
	}
	if len(result.Insert) != 1 || result.Insert[0].Text != "postgres runs in Docker on 5432" {
		t.Fatalf("insert = %+v, want the new fact", result.Insert)
	}
	// A surviving fact keeps its id, which is what keeps its observations.
	if result.Keep[0].ID != "1" || result.Keep[1].ID != "2" {
		t.Fatalf("kept ids = %q,%q, want 1,2", result.Keep[0].ID, result.Keep[1].ID)
	}
}

func TestDiffRemovesWhatTheAnswerDropped(t *testing.T) {
	previous := []PreviousFact{
		{ID: "1", Text: "deploy uses port 15432"},
		{ID: "2", Text: "the old build ran on Jenkins"},
	}
	result := Diff(previous, []Answer{{Text: "deploy uses port 15432", Sources: []int{0}}})
	if len(result.Remove) != 1 || result.Remove[0].ID != "2" {
		t.Fatalf("remove = %+v, want the dropped fact", result.Remove)
	}
}

// TestDiffIdentityIsTheWording pins that identity is the normalized text: the
// model may re-punctuate, and the row (with its observations) must survive.
func TestDiffIdentityIsTheWording(t *testing.T) {
	previous := []PreviousFact{{ID: "1", Text: "deploy  uses port 15432"}}
	result := Diff(previous, []Answer{{Text: " deploy uses port 15432 ", Sources: []int{0}}})
	if len(result.Keep) != 1 || len(result.Remove) != 0 {
		t.Fatalf("keep=%d remove=%d, want the fact carried over", len(result.Keep), len(result.Remove))
	}
}

func TestDiffUnionsDuplicates(t *testing.T) {
	result := Diff(nil, []Answer{
		{Text: "a fact", Sources: []int{0}, Topics: []string{"x"}},
		{Text: "a  fact", Sources: []int{1}, Topics: []string{"X", "y"}},
	})
	if len(result.Insert) != 1 {
		t.Fatalf("insert = %d, want 1", len(result.Insert))
	}
	key := MergeKey("a fact")
	if got := result.Sources[key]; len(got) != 2 {
		t.Fatalf("sources = %v, want both indexes", got)
	}
	if got := result.Topics[key]; len(got) != 2 {
		t.Fatalf("topics = %v, want x and y once each", got)
	}
}

// TestSourceMergeKeysMarksUncitedInputsDropped pins the ledger's central
// distinction: an input no fact cites is recorded as dropped, so deleting it can
// never make the aggregate stale.
func TestSourceMergeKeysMarksUncitedInputsDropped(t *testing.T) {
	result := Diff(nil, []Answer{{Text: "a fact", Sources: []int{0, 2}}})
	keys := result.SourceMergeKeys(4)
	if got := keys[0]; len(got) != 1 || got[0] == DroppedMergeKey {
		t.Fatalf("source 0 = %v, want the supported key", got)
	}
	for _, index := range []int{1, 3} {
		if got := keys[index]; len(got) != 1 || got[0] != DroppedMergeKey {
			t.Fatalf("source %d = %v, want [%s]", index, got, DroppedMergeKey)
		}
	}
}

func TestBanksByKey(t *testing.T) {
	fresh := []SourceFact{
		{BankID: "alice::orders", UnitID: "u1"},
		{BankID: "alice::billing", UnitID: "u2"},
		{BankID: "alice::orders", UnitID: "u3"},
	}
	key := MergeKey("one fact")
	sources := map[int][]string{0: {key}, 1: {key}, 2: {key}}
	got := BanksByKey(sources, fresh)
	if len(got[key]) != 2 {
		t.Fatalf("banks = %v, want both banks once each", got[key])
	}
}

func TestDroppedMergeKeyIsTheTeamConvention(t *testing.T) {
	// Two planes, one spelling: an operator reading either ledger sees the same
	// marker for a source the model folded away.
	if DroppedMergeKey != "__dropped__" {
		t.Fatalf("DroppedMergeKey = %q", DroppedMergeKey)
	}
}

func TestParseAnswerRejectsSourceOutsideTheBatch(t *testing.T) {
	_, err := ParseAnswer(`{"facts":[{"text":"a","sources":[1]}]}`, 1)
	if !errors.Is(err, ErrAnswerShape) {
		t.Fatalf("error = %v, want a shape failure", err)
	}
	if !strings.Contains(err.Error(), "outside the 1 inputs") {
		t.Fatalf("error = %v, want it to name the bound", err)
	}
}

package memoryops

import (
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// sourceFactFixture builds a recall result whose two observations cite three
// source facts of one, one and four tokens, plus the ranked match list that
// carried them. The second observation cites the third fact as well, so the
// shared-budget path has a source it can admit for one observation and skip for
// the other.
func sourceFactFixture() ([]storage.RecallMatch, map[string]storage.RecallMatch) {
	selected := []storage.RecallMatch{
		{ID: "o1", Text: "obs one", Type: "observation", SourceMemoryIDs: []string{"f1"}},
		{ID: "o2", Text: "obs two", Type: "observation", SourceMemoryIDs: []string{"f2", "f3"}},
	}
	available := map[string]storage.RecallMatch{
		"f1": {ID: "f1", Text: "one", Type: "world"},
		"f2": {ID: "f2", Text: "two", Type: "world"},
		"f3": {ID: "f3", Text: "three four five six", Type: "world"},
	}
	return selected, available
}

// A source fact that does not fit its budget is SKIPPED, never clipped, and a
// budget of 0 therefore answers no facts at all — the reference's selection only
// ever adds whole texts (engine/source_facts.py:39-87) and recall reports that
// as a null map with `source_facts_truncated: true`, not an empty map with
// nothing truncated. Clipping the last fact to the remaining budget and
// reporting `truncated: false` for it was the defect: at `max_tokens: 0` this
// service returned the source facts and said nothing had been cut.
func TestSourceFactBudgetSkipsRatherThanClips(t *testing.T) {
	selected, available := sourceFactFixture()
	cases := []struct {
		name          string
		total         int
		perObs        int
		wantIDs       []string
		wantTruncated bool
	}{
		// f1+f2 cost two tokens; f3 costs four more and cannot fit a shared
		// budget of 3, so it is skipped whole and the skip is reported.
		{name: "shared budget admits prefix", total: 3, perObs: -1, wantIDs: []string{"f1", "f2"}, wantTruncated: true},
		// A budget of 6 admits all three (2 + 4).
		{name: "shared budget admits all", total: 6, perObs: -1, wantIDs: []string{"f1", "f2", "f3"}, wantTruncated: false},
		// Budget 0: nothing fits, so the map is null and truncation is reported.
		{name: "zero budget answers nothing", total: 0, perObs: -1, wantIDs: nil, wantTruncated: true},
		// A budget of 2 fits f1 and f2; f3 is skipped as a whole.
		{name: "skips the oversized tail", total: 2, perObs: -1, wantIDs: []string{"f1", "f2"}, wantTruncated: true},
		// A negative total is unlimited.
		{name: "negative total is unlimited", total: -1, perObs: -1, wantIDs: []string{"f1", "f2", "f3"}, wantTruncated: false},
		// A non-negative per-observation budget takes over the shared one, so a
		// tiny shared budget is unused: one token per observation admits f1 for
		// the first and f2 for the second, and skips f3 in the second.
		{name: "per observation budget wins", total: 0, perObs: 1, wantIDs: []string{"f1", "f2"}, wantTruncated: true},
		{name: "per observation budget generous", total: 0, perObs: 100, wantIDs: []string{"f1", "f2", "f3"}, wantTruncated: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inc := recallInclude{sourceFacts: true, sourceFactTokens: tc.total, sourceFactPerObservation: tc.perObs}
			got, truncated := selectSourceFacts(selected, available, inc)
			if truncated == nil {
				t.Fatal("source_facts_truncated must always be reported once source facts were requested")
			}
			if *truncated != tc.wantTruncated {
				t.Fatalf("truncated = %v, want %v", *truncated, tc.wantTruncated)
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("got %d source facts, want %d: %v", len(got), len(tc.wantIDs), got)
			}
			for _, id := range tc.wantIDs {
				item, ok := got[id]
				if !ok {
					t.Fatalf("source fact %s is missing: %v", id, got)
				}
				// A kept fact is whole: the reference never clips a source text.
				if item.Text != available[id].Text {
					t.Fatalf("source fact %s was clipped: %q, want %q", id, item.Text, available[id].Text)
				}
			}
		})
	}
}

// No observation cites anything, so the channel is null and nothing was cut.
func TestSourceFactBudgetWithNoObservations(t *testing.T) {
	selected := []storage.RecallMatch{{ID: "w1", Text: "a fact", Type: "world"}}
	got, truncated := selectSourceFacts(selected, nil, recallInclude{sourceFacts: true, sourceFactTokens: 10, sourceFactPerObservation: -1})
	if got != nil || truncated == nil || *truncated {
		t.Fatalf("want a null map and truncated=false, got %v/%v", got, truncated)
	}
}

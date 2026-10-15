package storage

import (
	"context"
	"testing"
	"time"
)

// temporalProximity grades toward the window midpoint: centre is 1, either edge
// (or beyond) is 0, an interior point falls strictly between, and no window or
// no date is the neutral 0.5.
func TestTemporalProximityGradesTowardMidpoint(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2020, 1, 31, 0, 0, 0, 0, time.UTC)
	window := &TemporalWindow{Start: start, End: end, HasStart: true, HasEnd: true}
	mid := time.Date(2020, 1, 16, 0, 0, 0, 0, time.UTC)

	if got := temporalProximity(window, &mid, nil, nil); got != 1 {
		t.Fatalf("midpoint proximity = %v, want 1", got)
	}
	atStart := start
	if got := temporalProximity(window, &atStart, nil, nil); got != 0 {
		t.Fatalf("edge proximity = %v, want 0", got)
	}
	outside := time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := temporalProximity(window, &outside, nil, nil); got != 0 {
		t.Fatalf("out-of-window proximity = %v, want it clamped to 0", got)
	}
	// Halfway from the centre to the start edge: 7.5 of the 15-day half-span.
	quarter := time.Date(2020, 1, 8, 12, 0, 0, 0, time.UTC)
	if got := temporalProximity(window, &quarter, nil, nil); got <= 0 || got >= 1 {
		t.Fatalf("interior proximity = %v, want strictly between 0 and 1", got)
	}
	if got := temporalProximity(nil, &mid, nil, nil); got != 0.5 {
		t.Fatalf("no window = %v, want neutral 0.5", got)
	}
	if got := temporalProximity(window, nil, nil, nil); got != 0.5 {
		t.Fatalf("no date = %v, want neutral 0.5", got)
	}
}

// The signal is readable only from the midpoint out: a unit dated at the window
// centre outranks an otherwise identical one at the edge, where before grading
// both read as "inside" and tied.
func TestRecallTemporalMidpointOutranksEdge(t *testing.T) {
	s := NewMemoryStore("public")
	windowStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2020, 1, 31, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2020, 1, 16, 0, 0, 0, 0, time.UTC)
	edge := windowStart
	seedBank(t, s, "b1",
		// Ids chosen so the tie-break (ascending id) puts the edge candidate
		// first on the fused score; only the temporal boost may reorder them.
		RecallMatch{ID: "a-edge", Text: "alpha", Type: "world", OccurredStart: &edge},
		RecallMatch{ID: "z-centre", Text: "alpha", Type: "world", OccurredStart: &mid},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Temporal: &TemporalWindow{Start: windowStart, End: windowEnd, HasStart: true, HasEnd: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("both in-window units must be returned: %v", recallIDs(result))
	}
	if result.Matches[0].ID != "z-centre" {
		t.Fatalf("the midpoint unit must outrank the edge one: %v", recallIDs(result))
	}
}

// A request naming a window grades its candidates' proximity, but the score map
// stays on the reference's keys: the temporal signal is an input to the combined
// weight, never a reported stage.
func TestTemporalProximityIsNotReportedInScores(t *testing.T) {
	s := NewMemoryStore("public")
	windowStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2020, 1, 31, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2020, 1, 16, 0, 0, 0, 0, time.UTC)
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world", MentionedAt: &mid})

	withWindow, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Temporal: &TemporalWindow{Start: windowStart, End: windowEnd, HasStart: true, HasEnd: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(withWindow.Matches) == 0 {
		t.Fatal("expected the temporal-arm candidate to be returned")
	}
	// The proximity is folded into the combined weight, not reported: the
	// reference's recall envelope carries only final/reranker/semantic/keyword.
	for _, key := range []string{"temporal_score", "temporal_proximity"} {
		if _, ok := withWindow.Matches[0].Scores[key]; ok {
			t.Fatalf("%s must not be reported: %v", key, withWindow.Matches[0].Scores)
		}
	}
}

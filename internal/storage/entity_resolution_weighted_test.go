package storage

import (
	"math"
	"testing"
	"time"
)

// A name whose trigram set is identical to a candidate's is a full match on
// its own, without any shared context.
func TestWeightedScoreIdenticalTrigramsTakeFullMark(t *testing.T) {
	if got := scoreEntityMatch("Wren 🎵", "Wren", 1.0, entityScoreContext{}); got != 1.0 {
		t.Fatalf("identical trigrams scored %v, want 1.0", got)
	}
	if !entityScoreAccepted(1.0) {
		t.Fatal("a full mark must clear the bar")
	}
}

// With no context, a close but not identical name contributes only its
// sequence ratio times the name weight, which cannot reach the bar alone.
func TestWeightedScoreNameWeightIsHalf(t *testing.T) {
	name, candidate := "Acme Corporation", "Acme Corporaton"
	got := scoreEntityMatch(name, candidate, 0.8, entityScoreContext{})
	want := entitySimilarity(name, candidate) * entityNameWeight
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("score %v, want %v", got, want)
	}
	if entityScoreAccepted(got) {
		t.Fatalf("a name-only score %v must stay below the bar", got)
	}
}

// A shared neighbour adds a third of the score, damped by how many other
// entities that neighbour is seen with.
func TestWeightedScoreCooccurrenceWeighting(t *testing.T) {
	ctx := entityScoreContext{
		NearbyNames:            map[string]bool{"berlin": true},
		CandidateCooccurrences: map[string]bool{"berlin": true},
		Degrees:                map[string]int{"berlin": 1},
	}
	base := entitySimilarity("Alice", "Alicia") * entityNameWeight
	if got, want := scoreEntityMatch("Alice", "Alicia", 0.5, ctx), base+entityCooccurrenceScoreWeight; math.Abs(got-want) > 1e-9 {
		t.Fatalf("degree-one neighbour scored %v, want %v", got, want)
	}
	ctx.Degrees["berlin"] = 100
	if got, want := scoreEntityMatch("Alice", "Alicia", 0.5, ctx), base+0.1*entityCooccurrenceScoreWeight; math.Abs(got-want) > 1e-9 {
		t.Fatalf("hub neighbour scored %v, want %v", got, want)
	}
}

// The co-occurrence share is divided by the number of nearby names.
func TestWeightedScoreCooccurrenceDenominator(t *testing.T) {
	ctx := entityScoreContext{
		NearbyNames:            map[string]bool{"a": true, "b": true},
		CandidateCooccurrences: map[string]bool{"a": true},
		Degrees:                map[string]int{"a": 1, "b": 1},
	}
	base := entitySimilarity("X", "Y") * entityNameWeight
	if got, want := scoreEntityMatch("X", "Y", 0.2, ctx), base+0.5*entityCooccurrenceScoreWeight; math.Abs(got-want) > 1e-9 {
		t.Fatalf("one of two neighbours scored %v, want %v", got, want)
	}
}

// A candidate seen within the window adds the recency share; one outside it,
// or with a missing date, adds nothing.
func TestWeightedScoreTemporalWindow(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-3 * 24 * time.Hour)
	ctx := entityScoreContext{LastSeen: &recent, EventDate: &now}
	base := entitySimilarity("Alice", "Alicia") * entityNameWeight
	if got, want := scoreEntityMatch("Alice", "Alicia", 0.5, ctx), base+(1-3.0/7.0)*entityTemporalWeight; math.Abs(got-want) > 1e-9 {
		t.Fatalf("three-day gap scored %v, want %v", got, want)
	}
	old := now.Add(-8 * 24 * time.Hour)
	ctx.LastSeen = &old
	if got := scoreEntityMatch("Alice", "Alicia", 0.5, ctx); math.Abs(got-base) > 1e-9 {
		t.Fatalf("a gap beyond the window scored %v, want the name-only %v", got, base)
	}
	ctx.EventDate = nil
	if got := scoreEntityMatch("Alice", "Alicia", 0.5, ctx); math.Abs(got-base) > 1e-9 {
		t.Fatalf("a missing event date scored %v, want the name-only %v", got, base)
	}
}

// The bar is inclusive on a score rounded to six decimals, so the binary-float
// overshoot of the bar merges just like the bar itself.
func TestWeightedScoreThresholdIsInclusiveAndRounded(t *testing.T) {
	for _, score := range []float64{0.6, 0.6000000000000001, 0.6000001} {
		if !entityScoreAccepted(score) {
			t.Fatalf("score %v should clear the bar", score)
		}
	}
	for _, score := range []float64{0.599999, 0.59} {
		if entityScoreAccepted(score) {
			t.Fatalf("score %v should not clear the bar", score)
		}
	}
}

// A full-mark candidate outranks a name-only one.
func TestBestCandidatePicksHighestWeightedScore(t *testing.T) {
	candidates := []Entity{
		{ID: "b", CanonicalName: "Mikes"},
		{ID: "a", CanonicalName: "MIKE!"},
	}
	got, found, err := bestEntityCandidate(candidates, "Mike", 0, entityMentionContext{})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.ID != "a" {
		t.Fatalf("picked %+v, want the full-mark candidate", got)
	}
}

// A name that differs only by a number is a different thing: the word gate
// rejects it before the score ever runs. Name similarity alone used to merge
// this pair.
func TestBestCandidateWordGateRejectsDifferentNumbers(t *testing.T) {
	candidates := []Entity{{ID: "e1", CanonicalName: "Room 102"}}
	if _, found, err := bestEntityCandidate(candidates, "Room 101", 0, entityMentionContext{}); err != nil || found {
		t.Fatalf("different room numbers must not merge: found=%v err=%v", found, err)
	}
}

// Name similarity alone no longer merges a near miss: the name weight cannot
// reach the bar without another signal.
func TestBestCandidateNameAloneDoesNotMerge(t *testing.T) {
	candidates := []Entity{{ID: "e1", CanonicalName: "Mikes"}}
	if _, found, err := bestEntityCandidate(candidates, "Mike", 0, entityMentionContext{}); err != nil || found {
		t.Fatalf("name evidence alone must not merge: found=%v err=%v", found, err)
	}
}

// The in-memory backend applies the same weighted resolution.
func TestMemoryBestMatchUsesWeightedScore(t *testing.T) {
	nameOnly := NewMemoryStore("public")
	nameOnly.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Mikes", EntityKind: EntityKindRegular}
	if resolved := nameOnly.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true, nil, nil); resolved.ID == "e1" {
		t.Fatal("name evidence alone must not merge on the in-memory backend")
	}
	fullMark := NewMemoryStore("public")
	fullMark.entities["e2"] = Entity{ID: "e2", BankID: "b1", CanonicalName: "MIKE!", EntityKind: EntityKindRegular}
	if resolved := fullMark.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true, nil, nil); resolved.ID != "e2" {
		t.Fatalf("an identical trigram set must merge: %+v", resolved)
	}
}

// The shipped caps and floors stay in place.
func TestEntityResolutionCapsAndFloors(t *testing.T) {
	if EntityCandidateSimilarity != 0.3 {
		t.Fatalf("candidate floor = %v, want 0.3", EntityCandidateSimilarity)
	}
	if DefaultEntityMergeMinSimilarity != 0.3 {
		t.Fatalf("merge floor = %v, want 0.3", DefaultEntityMergeMinSimilarity)
	}
	if DefaultEntityIntrabatchMergeSimilarity != 0.5 {
		t.Fatalf("intrabatch floor = %v, want 0.5", DefaultEntityIntrabatchMergeSimilarity)
	}
	if DefaultEntityResolutionCandidates != 200 {
		t.Fatalf("candidate cap = %d, want 200", DefaultEntityResolutionCandidates)
	}
	if maxIntrabatchNames != 250 {
		t.Fatalf("intrabatch name cap = %d, want 250", maxIntrabatchNames)
	}
}

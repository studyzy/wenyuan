package storage

import (
	"context"
	"testing"
	"time"
)

// A consolidated observation inherits the temporal columns of the facts it rests
// on, with the reference's aggregation rule (`_aggregate_source_fields`,
// engine/consolidation/consolidator.py:838-860): the earliest event_date and
// occurred_start, the latest occurred_end and mentioned_at.
//
// The rule is what makes an observation an observation *of* something: a
// conclusion folded from a fact that happened in 2015 is itself dated 2015.
// Leaving the columns NULL instead produced a row that answered "no known
// time" where the reference answers the source's date, which is what this test
// caught live before the fix (the reference's row read
// date=2015-01-01T00:00:00+00:00, this service's read date="").
func TestConsolidatedObservationInheritsSourceBounds(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")

	old := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	oldEnd := time.Date(2015, 12, 31, 0, 0, 0, 0, time.UTC)
	oldMentioned := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2018, 6, 1, 0, 0, 0, 0, time.UTC)
	newerMentioned := time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)

	first, err := store.UpsertUnit(ctx, MemoryUnit{
		BankID: "b1", Text: "Alice visited Paris in 2015.", FactType: TypeWorld,
		EventDate: &old, OccurredStart: &old, OccurredEnd: &oldEnd, MentionedAt: &oldMentioned,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertUnit(ctx, MemoryUnit{
		BankID: "b1", Text: "Alice moved to Berlin in 2018.", FactType: TypeWorld,
		OccurredStart: &newer, MentionedAt: &newerMentioned,
	})
	if err != nil {
		t.Fatal(err)
	}

	scopedStore, ok := any(store).(ConsolidationScopeStore)
	if !ok {
		t.Fatal("the memory store must provide the consolidation scope capability")
	}
	observationID, err := scopedStore.CreateObservationInScope(ctx, "b1", "Alice has lived in Paris and Berlin.",
		[]string{first.ID, second.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := store.GetMemory(ctx, "b1", observationID)
	if err != nil {
		t.Fatal(err)
	}

	if observation.EventDate == nil || !observation.EventDate.Equal(old) {
		t.Fatalf("event_date = %v, want the earliest source date %v", observation.EventDate, old)
	}
	if observation.OccurredStart == nil || !observation.OccurredStart.Equal(old) {
		t.Fatalf("occurred_start = %v, want the earliest source start %v", observation.OccurredStart, old)
	}
	if observation.OccurredEnd == nil || !observation.OccurredEnd.Equal(oldEnd) {
		t.Fatalf("occurred_end = %v, want the latest source end %v", observation.OccurredEnd, oldEnd)
	}
	if observation.MentionedAt == nil || !observation.MentionedAt.Equal(newerMentioned) {
		t.Fatalf("mentioned_at = %v, want the latest source mention %v", observation.MentionedAt, newerMentioned)
	}
}

// An observation with no dated source carries no date at all: the rule keeps a
// field absent when no source states it, so an observation is never stamped
// with an artificial timestamp.
func TestConsolidatedObservationWithoutDatedSourcesStaysUndated(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")

	source, err := store.UpsertUnit(ctx, MemoryUnit{BankID: "b1", Text: "A timeless remark.", FactType: TypeWorld})
	if err != nil {
		t.Fatal(err)
	}
	observationID, err := store.CreateObservation(ctx, "b1", "A timeless conclusion.", []string{source.ID})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := store.GetMemory(ctx, "b1", observationID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.EventDate != nil || observation.OccurredStart != nil ||
		observation.OccurredEnd != nil || observation.MentionedAt != nil {
		t.Fatalf("undated observation carries %+v, want every bound absent", observation)
	}
}

// An update widens an observation's bounds with its new sources instead of
// stamping the write clock on it: `_TemporalBounds.merged_with`
// (engine/consolidation/consolidator.py:257-262) keeps the earlier event and
// start and the later end and mention.
func TestObservationUpdateWidensBoundsInsteadOfRestamping(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")

	early := time.Date(2010, 3, 1, 0, 0, 0, 0, time.UTC)
	earlyEnd := time.Date(2010, 4, 1, 0, 0, 0, 0, time.UTC)
	earlyMentioned := time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2020, 9, 1, 0, 0, 0, 0, time.UTC)
	laterEnd := time.Date(2021, 2, 1, 0, 0, 0, 0, time.UTC)
	laterMentioned := time.Date(2022, 5, 1, 0, 0, 0, 0, time.UTC)

	source, err := store.UpsertUnit(ctx, MemoryUnit{BankID: "b1", Text: "Alice started in 2010.", FactType: TypeWorld})
	if err != nil {
		t.Fatal(err)
	}
	observationID, err := store.CreateObservation(ctx, "b1", "Alice's first role.", []string{source.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Give the observation its own bounds, then reinforce it with a later fact.
	first, err := store.GetMemory(ctx, "b1", observationID)
	if err != nil {
		t.Fatal(err)
	}
	first.EventDate, first.OccurredStart = &early, &early
	first.OccurredEnd, first.MentionedAt = &earlyEnd, &earlyMentioned
	store.mu.Lock()
	store.units[first.ID] = first
	store.mu.Unlock()

	reinforcement, err := store.UpsertUnit(ctx, MemoryUnit{
		BankID: "b1", Text: "Alice's role changed in 2020.", FactType: TypeWorld,
		OccurredStart: &later, OccurredEnd: &laterEnd, MentionedAt: &laterMentioned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateObservation(ctx, "b1", observationID, "Alice's long career.", []string{reinforcement.ID}); err != nil {
		t.Fatal(err)
	}
	observation, err := store.GetMemory(ctx, "b1", observationID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.EventDate == nil || !observation.EventDate.Equal(early) {
		t.Fatalf("event_date = %v, want the earlier bound kept %v", observation.EventDate, early)
	}
	if observation.OccurredStart == nil || !observation.OccurredStart.Equal(early) {
		t.Fatalf("occurred_start = %v, want the earlier bound kept %v", observation.OccurredStart, early)
	}
	if observation.OccurredEnd == nil || !observation.OccurredEnd.Equal(laterEnd) {
		t.Fatalf("occurred_end = %v, want the later bound taken %v", observation.OccurredEnd, laterEnd)
	}
	if observation.MentionedAt == nil || !observation.MentionedAt.Equal(laterMentioned) {
		t.Fatalf("mentioned_at = %v, want the later mention taken %v (not the write clock)",
			observation.MentionedAt, laterMentioned)
	}
}

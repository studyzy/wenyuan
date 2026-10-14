package storage

import (
	"context"
	"testing"
	"time"
)

// stalenessFixture builds a bank holding one memory, with the bank's write
// watermark set to that memory's time.
//
// The watermark is set explicitly because it is half of what staleness is
// decided on: a bank whose watermark was never advanced short-circuits every
// staleness check to "not stale" before the scoped query runs, which would make
// these tests pass without exercising the comparison they exist to pin.
func stalenessFixture(t *testing.T) (*MemoryStore, string, time.Time) {
	t.Helper()
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{ID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	seedWrite(t, store, "b1", RecallMatch{
		ID: "m1", Text: "a deployment happened", Type: "world",
		MentionedAt: &written, Tags: []string{"ops"},
	})
	return store, "b1", written
}

// seedWrite records a memory the way a real write does.
//
// It goes through the same upsert and watermark the write path uses, rather
// than through the test-only SeedMemory helper: that helper fills only the
// derived per-bank recall projection, so a staleness check reading the
// authoritative unit store would see nothing and report every model current.
func seedWrite(t *testing.T, store *MemoryStore, bankID string, match RecallMatch) {
	t.Helper()
	written := time.Now().UTC()
	if match.MentionedAt != nil {
		written = *match.MentionedAt
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.upsertUnitLocked(MemoryUnit{
		ID: match.ID, BankID: bankID, Text: match.Text,
		Type: MemoryUnitType(match.Type), Tags: match.Tags,
		MentionedAt: &written, OccurredStart: match.OccurredStart,
	})
	bank := store.banks[bankID]
	bank.LastWriteAt = &written
	store.banks[bankID] = bank
}

// A model that has seen the newest write is not stale, however old its clock
// is: staleness is a question about data, not about time.
func TestStaleIsFalseWhenNothingNewerExists(t *testing.T) {
	store, bankID, written := stalenessFixture(t)
	model := MentalModel{
		ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment",
		LastMemorySeenAt: timePtr(written.Add(time.Minute)),
		LastRefreshedAt:  written,
	}
	if store.IsStale(context.Background(), bankID, model) {
		t.Error("a model that has seen the newest write is not stale")
	}
}

// A memory written after the watermark makes the model stale.
func TestStaleIsTrueWhenNewerMemoryExists(t *testing.T) {
	store, bankID, written := stalenessFixture(t)
	model := MentalModel{
		ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment",
		LastMemorySeenAt: timePtr(written.Add(-time.Hour)),
	}
	if !store.IsStale(context.Background(), bankID, model) {
		t.Error("a memory newer than the watermark makes the model stale")
	}
}

// A model never stamped with a watermark has nothing to be current with, so it
// is stale by definition.
func TestStaleWhenNeverSeen(t *testing.T) {
	store, bankID, _ := stalenessFixture(t)
	model := MentalModel{ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment"}
	if !store.IsStale(context.Background(), bankID, model) {
		t.Error("a model with no watermark must be treated as stale")
	}
}

// A tag-scoped model is stale only when a write lands *in* its tags: an
// untagged write is not about it, and counting it would flag the model stale
// after nearly every write to a bank that mixes scoped and unscoped memories.
func TestTagScopedModelIgnoresUnscopedWrites(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{ID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	watermark := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	later := watermark.Add(time.Hour)
	// One in-scope memory that predates the watermark, one out-of-scope write
	// that postdates it.
	seedWrite(t, store, "b1", RecallMatch{ID: "in", Text: "in scope", Type: "world", MentionedAt: &watermark, Tags: []string{"ops"}})
	seedWrite(t, store, "b1", RecallMatch{ID: "out", Text: "other", Type: "world", MentionedAt: &later, Tags: []string{"sales"}})

	model := MentalModel{
		ID: "mm1", BankID: "b1", Subtype: SubtypePinned, SourceQuery: "deployment",
		Tags: []string{"ops"}, LastMemorySeenAt: timePtr(watermark),
	}
	if store.IsStale(ctx, "b1", model) {
		t.Error("a write outside the model's tags must not make it stale")
	}

	// A write inside the tags does make it stale.
	inside := later.Add(time.Hour)
	seedWrite(t, store, "b1", RecallMatch{ID: "in2", Text: "newer in scope", Type: "world", MentionedAt: &inside, Tags: []string{"ops"}})
	if !store.IsStale(ctx, "b1", model) {
		t.Error("a write inside the model's tags must make it stale")
	}
}

// An unscoped model's scope is the whole bank, so any write makes it stale.
func TestUnscopedModelCountsEveryWrite(t *testing.T) {
	store, bankID, written := stalenessFixture(t)
	later := written.Add(time.Hour)
	seedWrite(t, store, bankID, RecallMatch{ID: "m2", Text: "anything", Type: "world", MentionedAt: &later})
	model := MentalModel{
		ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment",
		LastMemorySeenAt: timePtr(written),
	}
	if !store.IsStale(context.Background(), bankID, model) {
		t.Error("an unscoped model's scope is the whole bank, so any write counts")
	}
}

// The fact-type filter narrows the scope, so a write of another type does not
// make the model stale.
func TestFactTypeFilterNarrowsTheScope(t *testing.T) {
	store, bankID, written := stalenessFixture(t)
	later := written.Add(time.Hour)
	seedWrite(t, store, bankID, RecallMatch{ID: "exp", Text: "an experience", Type: "experience", MentionedAt: &later})

	scoped := MentalModel{
		ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment",
		Trigger: map[string]any{"fact_types": []any{"world"}}, LastMemorySeenAt: timePtr(written),
	}
	if store.IsStale(context.Background(), bankID, scoped) {
		t.Error("a write of another fact type must not make the model stale")
	}
	matching := MentalModel{
		ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment",
		Trigger: map[string]any{"fact_types": []any{"experience"}}, LastMemorySeenAt: timePtr(written),
	}
	if !store.IsStale(context.Background(), bankID, matching) {
		t.Error("a write of a matching fact type must make the model stale")
	}
}

// The watermark falls back to the refresh time for a model refreshed before the
// watermark column existed, rather than treating it as having seen nothing.
func TestWatermarkFallsBackToRefreshTime(t *testing.T) {
	store, bankID, written := stalenessFixture(t)
	model := MentalModel{
		ID: "mm1", BankID: bankID, Subtype: SubtypePinned, SourceQuery: "deployment",
		LastRefreshedAt: written.Add(time.Minute),
	}
	if store.IsStale(context.Background(), bankID, model) {
		t.Error("the refresh time must stand in when no watermark was stamped")
	}
}

// An untagged model defaults to matching any memory, while a tagged one
// defaults to the narrowest match.
func TestScopeDefaultsDependOnBeingTagged(t *testing.T) {
	unscoped := ScopeFromModel(MentalModel{ID: "a"})
	if unscoped.TagsMatch != TagsMatchAny {
		t.Errorf("an untagged model's match = %q, want any", unscoped.TagsMatch)
	}
	scoped := ScopeFromModel(MentalModel{ID: "b", Tags: []string{"ops"}})
	if scoped.TagsMatch != TagsMatchAllStrict {
		t.Errorf("a tagged model's match = %q, want all_strict", scoped.TagsMatch)
	}
}

// Explicit tag groups override the model's flat tags entirely.
func TestTagGroupsOverrideFlatTags(t *testing.T) {
	model := MentalModel{
		ID: "a", Tags: []string{"ops"},
		Trigger: map[string]any{"tag_groups": []any{
			map[string]any{"tags": []any{"a", "b"}, "match": "any"},
		}},
	}
	scope := ScopeFromModel(model)
	if len(scope.TagGroups) != 1 {
		t.Fatalf("tag groups = %+v, want the declared group", scope.TagGroups)
	}
	if len(scope.Tags) != 0 {
		t.Errorf("flat tags = %v, want them overridden by the groups", scope.Tags)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

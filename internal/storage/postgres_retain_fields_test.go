package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The retain write must persist every durable unit field, not just the text.
// The in-memory backend is the contract double the rest of the suite runs
// against, so a field it drops is a field no other test can see go missing;
// this pins the round trip directly. The PostgreSQL counterpart of this
// assertion lives in postgres_retain_fields_pgx_test.go and runs only when a
// live database is configured, because the Postgres write path cannot be
// exercised offline.
func TestRetainUnitPreservesDurableFields(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	mentioned := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	eventDate := time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC)
	occurredEnd := time.Date(2026, 2, 27, 18, 30, 0, 0, time.UTC)
	unit := MemoryUnit{
		BankID:      bank,
		Text:        "Alice hiked the ridge on Friday.",
		FactType:    TypeExperience,
		Context:     "a planning conversation about the weekend",
		MentionedAt: &mentioned,
		EventDate:   &eventDate,
		OccurredEnd: &occurredEnd,
		Metadata:    map[string]any{"where": "the ridge", "source": "note"},
	}
	stored, err := store.UpsertUnit(ctx, unit)
	if err != nil {
		t.Fatalf("upsert unit: %v", err)
	}

	got, err := store.GetMemory(ctx, bank, stored.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if got.Context != unit.Context {
		t.Errorf("context = %q, want %q", got.Context, unit.Context)
	}
	if got.MentionedAt == nil || !got.MentionedAt.Equal(mentioned) {
		t.Errorf("mentioned_at = %v, want %v", got.MentionedAt, mentioned)
	}
	if got.EventDate == nil || !got.EventDate.Equal(eventDate) {
		t.Errorf("event_date = %v, want %v", got.EventDate, eventDate)
	}
	if got.OccurredEnd == nil || !got.OccurredEnd.Equal(occurredEnd) {
		t.Errorf("occurred_end = %v, want %v", got.OccurredEnd, occurredEnd)
	}
	if !reflect.DeepEqual(got.Metadata, unit.Metadata) {
		t.Errorf("metadata = %#v, want %#v", got.Metadata, unit.Metadata)
	}
}

// An entity name longer than the cap is dropped at intake rather than stored:
// the entity table's btree index cannot hold a tuple that long, and a
// truncated name would become a registry identity that later junk could merge
// onto. The fact keeps its other entities and the retain still succeeds.
func TestRetainOverLongEntityNameIsDroppedAtIntake(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	longName := strings.Repeat("x", EntityNameMaxLength+23)

	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items: []RetainItem{{
			Content:         "A note that names one very long entity.",
			DocumentID:      "doc-long-name",
			Entities:        []RetainEntity{{Text: longName}},
			ResolveEntities: true,
		}},
	}); err != nil {
		t.Fatalf("retain with an over-long entity name: %v", err)
	}

	page, err := store.ListEntities(ctx, EntityQuery{BankID: bank})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if len(page.Entities) != 0 {
		t.Fatalf("an over-long name must not become an entity; got %+v", page.Entities)
	}
	// Resolving the over-long name directly creates nothing either: intake
	// rejects it wherever it arrives from.
	entity, err := store.ResolveEntity(ctx, bank, longName, EntityKindRegular, true)
	if err != nil {
		t.Fatalf("resolve over-long entity: %v", err)
	}
	if entity.ID != "" {
		t.Errorf("resolve of an over-long name produced entity %q, want none", entity.ID)
	}
}

// The cap counts characters, not bytes: a multibyte name within the cap is
// stored unchanged, and one past it is dropped rather than cut mid-character.
func TestRetainOverLongMultibyteEntityNameIsDropped(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	// Past the cap, so it is dropped: the stored name is never a partial rune.
	tooLong := strings.Repeat("日", EntityNameMaxLength+7)
	entity, err := store.ResolveEntity(ctx, bank, tooLong, EntityKindRegular, false)
	if err != nil {
		t.Fatalf("resolve over-long multibyte entity: %v", err)
	}
	if entity.ID != "" {
		t.Errorf("over-long multibyte name produced entity %q, want none", entity.ID)
	}

	// Within the cap, the name round-trips in full and stays valid UTF-8.
	withinCap := strings.Repeat("日", EntityNameMaxLength)
	entity, err = store.ResolveEntity(ctx, bank, withinCap, EntityKindRegular, false)
	if err != nil {
		t.Fatalf("resolve multibyte entity: %v", err)
	}
	if !utf8.ValidString(entity.CanonicalName) {
		t.Fatalf("canonical name is not valid UTF-8: %q", entity.CanonicalName)
	}
	if entity.CanonicalName != withinCap {
		t.Errorf("canonical name = %q, want %d copies of the rune", entity.CanonicalName, EntityNameMaxLength)
	}
}

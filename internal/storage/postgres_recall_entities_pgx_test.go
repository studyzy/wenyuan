//go:build pgx

package storage

import (
	"context"
	"slices"
	"testing"
)

// The PostgreSQL recall path resolves each returned fact's entities against the
// registry, inherits an observation's entities from its sources, and builds the
// name-keyed entity channel. The entity ids are written with fixed values so
// "in entity id order" is checked rather than assumed.
func TestPostgresRecallEntityChannelFromStoredPostings(t *testing.T) {
	store := postgresVectorStore(t)
	pg := store.(*PostgresStore)
	ctx := context.Background()
	const bank = "w13-entities"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "w13-entities"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	qualified := quoteIdent(pg.schema)
	for id, name := range map[string]string{
		w13Alice:  "Alice Zhang",
		w13Berlin: "Berlin Office",
		w13Cairo:  "Cairo Depot",
	} {
		if _, err := pg.db.ExecContext(ctx,
			`INSERT INTO `+qualified+`.entities (id, canonical_name, bank_id) VALUES ($1::uuid,$2,$3)`,
			id, name, bank); err != nil {
			t.Fatalf("insert entity %s: %v", name, err)
		}
	}

	units := []MemoryUnit{
		{ID: newID(), BankID: bank, Text: "Zephyrine alpha release.", FactType: TypeWorld},
		{ID: newID(), BankID: bank, Text: "Zephyrine beta release.", FactType: TypeWorld},
		{ID: newID(), BankID: bank, Text: "Limestone delta baseline.", FactType: TypeWorld},
	}
	for i := range units {
		if _, err := store.UpsertUnit(ctx, units[i]); err != nil {
			t.Fatalf("upsert unit %d: %v", i, err)
		}
	}
	observation := MemoryUnit{
		ID: newID(), BankID: bank, Text: "Zephyrine gamma summary.",
		FactType: TypeObservation, SourceMemoryIDs: []string{units[2].ID},
	}
	if _, err := store.UpsertUnit(ctx, observation); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	// Posted Berlin Office first, so a result that merely preserves insertion
	// order would lead with the wrong name.
	for _, entityID := range []string{w13Berlin, w13Alice} {
		if err := store.UpsertUnitEntity(ctx, units[0].ID, entityID); err != nil {
			t.Fatalf("post %s: %v", entityID, err)
		}
	}
	if err := store.UpsertUnitEntity(ctx, units[2].ID, w13Cairo); err != nil {
		t.Fatalf("post source entity: %v", err)
	}

	result, err := store.Recall(ctx, RecallQuery{BankID: bank, Query: "Zephyrine", IncludeEntities: true, Limit: 10})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	byID := matchesByID(result.Matches)
	if got := byID[units[0].ID].Entities; !slices.Equal(got, []string{"Alice Zhang", "Berlin Office"}) {
		t.Fatalf("direct postings must be rendered in entity id order: %v", got)
	}
	if got := byID[observation.ID].Entities; !slices.Equal(got, []string{"Cairo Depot"}) {
		t.Fatalf("an observation must inherit its source's entities: %v", got)
	}
	if got := byID[units[1].ID].Entities; got != nil {
		t.Fatalf("a fact with no entity must report null, got %v", got)
	}

	states := map[string]EntityState{}
	for _, state := range result.EntityStates {
		states[state.Name] = state
	}
	if len(states) != 3 {
		t.Fatalf("entity channel = %+v, want the three mentioned entities", states)
	}
	for id, name := range map[string]string{w13Alice: "Alice Zhang", w13Berlin: "Berlin Office", w13Cairo: "Cairo Depot"} {
		state, ok := states[name]
		if !ok {
			t.Fatalf("entity channel is missing %q: %+v", name, states)
		}
		if state.ID != id || state.CanonicalName != name || len(state.Observations) != 0 {
			t.Fatalf("entity %q = %+v, want id %s with no observations", name, state, id)
		}
	}
}

// A recall that did not ask for entities resolves none on the SQL path either.
func TestPostgresRecallEntityChannelAbsentUnlessRequested(t *testing.T) {
	store := postgresVectorStore(t)
	pg := store.(*PostgresStore)
	ctx := context.Background()
	const bank = "w13-entities-off"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(pg.schema)+`.entities (id, canonical_name, bank_id) VALUES ($1::uuid,$2,$3)`,
		w13Alice, "Alice Zhang", bank); err != nil {
		t.Fatalf("insert entity: %v", err)
	}
	unit := MemoryUnit{ID: newID(), BankID: bank, Text: "Zephyrine alpha release.", FactType: TypeWorld}
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertUnitEntity(ctx, unit.ID, w13Alice); err != nil {
		t.Fatal(err)
	}
	result, err := store.Recall(ctx, RecallQuery{BankID: bank, Query: "Zephyrine", Limit: 10})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.EntityStates) != 0 {
		t.Fatalf("an unrequested entity channel must stay empty: %+v", result.EntityStates)
	}
	for _, match := range result.Matches {
		if match.Entities != nil {
			t.Fatalf("entities resolved without being requested: %+v", match.Entities)
		}
	}
}

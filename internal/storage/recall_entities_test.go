package storage

import (
	"context"
	"slices"
	"testing"
)

// Fixed entity ids, so "ordered by entity id" is a checkable claim rather than
// whatever order the registry happened to hand back.
const (
	w13Alice  = "aaaaaaaa-0000-4000-8000-000000000001"
	w13Berlin = "bbbbbbbb-0000-4000-8000-000000000002"
	w13Cairo  = "cccccccc-0000-4000-8000-000000000003"
)

// seedEntityUnit stores a unit and its direct entity postings. The postings are
// recorded in the order given, which lets a test hand them over out of entity
// id order.
func seedEntityUnit(t *testing.T, s *MemoryStore, unit MemoryUnit, entityIDs ...string) {
	t.Helper()
	s.SeedUnit(unit)
	if len(entityIDs) == 0 {
		return
	}
	s.mu.Lock()
	s.unitEntities[unit.ID] = append(s.unitEntities[unit.ID], entityIDs...)
	s.mu.Unlock()
}

// seedEntityRegistry registers named entities with chosen ids.
func seedEntityRegistry(s *MemoryStore, bankID string, entries map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, name := range entries {
		s.entities[id] = Entity{ID: id, BankID: bankID, CanonicalName: name}
	}
}

// recallEntityFixture is the shared three-case fixture: a fact with two direct
// postings recorded out of id order, a fact with none, and an observation that
// inherits the postings of a source the query does not surface.
func recallEntityFixture(t *testing.T, s *MemoryStore, bankID string) {
	t.Helper()
	if _, err := s.CreateBank(context.Background(), Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatal(err)
	}
	seedEntityRegistry(s, bankID, map[string]string{
		w13Alice:  "Alice Zhang",
		w13Berlin: "Berlin Office",
		w13Cairo:  "Cairo Depot",
	})
	seedEntityUnit(t, s, MemoryUnit{ID: "unit-direct", BankID: bankID, Text: "Zephyrine alpha release.", FactType: TypeWorld},
		w13Berlin, w13Alice)
	seedEntityUnit(t, s, MemoryUnit{ID: "unit-plain", BankID: bankID, Text: "Zephyrine beta release.", FactType: TypeWorld})
	seedEntityUnit(t, s, MemoryUnit{ID: "unit-source", BankID: bankID, Text: "Limestone delta baseline.", FactType: TypeWorld},
		w13Cairo)
	seedEntityUnit(t, s, MemoryUnit{
		ID: "unit-inherit", BankID: bankID, Text: "Zephyrine gamma summary.",
		FactType: TypeObservation, SourceMemoryIDs: []string{"unit-source"},
	})
}

func matchesByID(matches []RecallMatch) map[string]RecallMatch {
	out := make(map[string]RecallMatch, len(matches))
	for _, m := range matches {
		out[m.ID] = m
	}
	return out
}

// A recall asked for entities reports each fact's canonical names in entity id
// order, inherits an observation's entities from its source when it has none of
// its own, and leaves a fact with no entity null. The name-keyed channel is a
// view of the same facts and carries no observations: the per-entity summaries
// now live in the mental models.
func TestRecallEntityChannelFromStoredPostings(t *testing.T) {
	s := NewMemoryStore("public")
	recallEntityFixture(t, s, "w13")
	result, err := s.Recall(context.Background(),
		RecallQuery{BankID: "w13", Query: "Zephyrine", IncludeEntities: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	byID := matchesByID(result.Matches)
	if got := byID["unit-direct"].Entities; !slices.Equal(got, []string{"Alice Zhang", "Berlin Office"}) {
		t.Fatalf("direct postings must be rendered in entity id order: %v", got)
	}
	if got := byID["unit-inherit"].Entities; !slices.Equal(got, []string{"Cairo Depot"}) {
		t.Fatalf("an observation must inherit its source's entities: %v", got)
	}
	if got := byID["unit-plain"].Entities; got != nil {
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
		if state.ID != id || state.CanonicalName != name {
			t.Fatalf("entity %q = %+v, want id %s", name, state, id)
		}
		if len(state.Observations) != 0 {
			t.Fatalf("entity %q must carry no observations, got %+v", name, state.Observations)
		}
	}
}

// A recall that did not ask for entities resolves none, so both the facts and
// the channel stay empty.
func TestRecallEntityChannelAbsentUnlessRequested(t *testing.T) {
	s := NewMemoryStore("public")
	recallEntityFixture(t, s, "w13")
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "w13", Query: "Zephyrine", Limit: 10})
	if err != nil {
		t.Fatal(err)
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

// Entity ordering follows the entity id, not the order the postings were
// written in: the fixture records Berlin Office before Alice Zhang, and the
// result must still lead with Alice Zhang.
func TestEntityRefsSortByEntityID(t *testing.T) {
	got := entityRefsFor([]string{w13Berlin, w13Alice, w13Berlin}, map[string]Entity{
		w13Alice:  {ID: w13Alice, CanonicalName: "Alice Zhang"},
		w13Berlin: {ID: w13Berlin, CanonicalName: "Berlin Office"},
	})
	want := []EntityRef{{ID: w13Alice, CanonicalName: "Alice Zhang"}, {ID: w13Berlin, CanonicalName: "Berlin Office"}}
	if !slices.Equal(got, want) {
		t.Fatalf("entity refs = %+v, want %+v", got, want)
	}
}

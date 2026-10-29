package history

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestIdSampleCountsEverythingAndCapsTheIDs mirrors the reference's own
// test_id_sample_counts_everything_and_caps_the_ids: the count is exact, the
// list is capped.
func TestIdSampleCountsEverythingAndCapsTheIDs(t *testing.T) {
	ids := make([]string, 0, maxIDs+5)
	for index := 0; index < maxIDs+5; index++ {
		ids = append(ids, paddedID(index))
	}
	ids = append(ids, "o0000")

	sample := Of(ids)
	if sample.Count != maxIDs+5 {
		t.Fatalf("count = %d, want %d", sample.Count, maxIDs+5)
	}
	if len(sample.IDs) != maxIDs {
		t.Fatalf("ids len = %d, want %d", len(sample.IDs), maxIDs)
	}
	if sample.IDs[0] != "o0000" {
		t.Fatalf("ids are not sorted: first = %q", sample.IDs[0])
	}
}

// paddedID renders oNNNN, the shape the reference's test uses.
func paddedID(index int) string {
	digits := "0123456789"
	return "o" + string([]byte{
		digits[(index/1000)%10], digits[(index/100)%10], digits[(index/10)%10], digits[index%10],
	})
}

// TestConsolidationChangesAreFiledUnderEverySourceDocument mirrors the
// reference's by_document transform: one event per document, carrying only the
// observations that stood on that document's facts.
func TestConsolidationChangesAreFiledUnderEverySourceDocument(t *testing.T) {
	changes := newObservationsChanges()
	changes.Created["new"] = []string{"f1", "f2"}
	changes.Updated["upd"] = []string{"f2", "f3"}
	changes.Removed["gone"] = []string{"f3"}

	events := changes.ByDocument(map[string]string{"f1": "doc-a", "f2": "doc-a", "f3": "doc-b"})
	if len(events) != 2 {
		t.Fatalf("documents = %v, want doc-a and doc-b", keysOf(events))
	}
	a, b := events["doc-a"], events["doc-b"]
	if a.DocumentID != "doc-a" || a.Kind != KindConsolidation {
		t.Fatalf("doc-a event = %+v", a)
	}
	if !reflect.DeepEqual(a.ObservationsCreated.IDs, []string{"new"}) {
		t.Fatalf("doc-a created = %v", a.ObservationsCreated.IDs)
	}
	if !reflect.DeepEqual(a.ObservationsUpdated.IDs, []string{"upd"}) {
		t.Fatalf("doc-a updated = %v", a.ObservationsUpdated.IDs)
	}
	if a.ObservationsRemoved.Count != 0 {
		t.Fatalf("doc-a removed = %v, want none", a.ObservationsRemoved)
	}
	if b.ObservationsCreated.Count != 0 {
		t.Fatalf("doc-b created = %v, want none", b.ObservationsCreated)
	}
	if !reflect.DeepEqual(b.ObservationsUpdated.IDs, []string{"upd"}) {
		t.Fatalf("doc-b updated = %v", b.ObservationsUpdated.IDs)
	}
	if !reflect.DeepEqual(b.ObservationsRemoved.IDs, []string{"gone"}) {
		t.Fatalf("doc-b removed = %v", b.ObservationsRemoved.IDs)
	}
}

// TestDeletionFilesEachDocumentUnderItsOwnFactsObservations mirrors the
// reference's deletion_events test: a fact with no document is filed nowhere,
// fact_id is named only for a single fact, and each document gets the
// observations its own facts held.
func TestDeletionFilesEachDocumentUnderItsOwnFactsObservations(t *testing.T) {
	changes := newObservationsChanges()
	changes.Removed["o1"] = []string{"f1", "x"}
	changes.Removed["o2"] = []string{"f2"}
	changes.Removed["o3"] = []string{"f3"}

	events := DeletionEvents(map[string]string{"f1": "a", "f2": "a", "f3": "b", "f4": ""}, changes)
	byDocument := map[string]Event{}
	for _, event := range events {
		byDocument[event.DocumentID] = event
	}
	if len(byDocument) != 2 {
		t.Fatalf("documents = %v, want a and b only", keysOf(byDocument))
	}
	a, b := byDocument["a"], byDocument["b"]
	if a.FactsRemoved != 2 || a.FactID != nil {
		t.Fatalf("a facts_removed=%d fact_id=%v, want 2 and nil", a.FactsRemoved, a.FactID)
	}
	if !reflect.DeepEqual(a.ObservationsRemoved.IDs, []string{"o1", "o2"}) {
		t.Fatalf("a removed = %v", a.ObservationsRemoved.IDs)
	}
	if b.FactsRemoved != 1 || b.FactID == nil || *b.FactID != "f3" {
		t.Fatalf("b facts_removed=%d fact_id=%v, want 1 and f3", b.FactsRemoved, b.FactID)
	}
	if !reflect.DeepEqual(b.ObservationsRemoved.IDs, []string{"o3"}) {
		t.Fatalf("b removed = %v", b.ObservationsRemoved.IDs)
	}
}

// TestNotesOutsideATrackedOperationAreDropped mirrors the reference's
// test_notes_outside_a_tracked_operation_are_dropped: a note costs nothing
// without a tracked operation, and a context with one keeps every note.
func TestNotesOutsideATrackedOperationAreDropped(t *testing.T) {
	ctx := context.Background()
	NoteObservationsRemoved(ctx, map[string][]string{"x": {"f"}}, []string{"f"})
	NoteObservationWritten(ctx, "z", []string{"f"}, false)

	ctx, changes := TrackChanges(ctx)
	NoteObservationsRemoved(ctx, map[string][]string{"x": {"f"}}, []string{"f"})
	NoteObservationWritten(ctx, "y", []string{"f"}, true)
	NoteObservationWritten(ctx, "z", []string{"f"}, false)

	if !reflect.DeepEqual(changes.Removed, map[string][]string{"x": {"f"}}) {
		t.Fatalf("removed = %v", changes.Removed)
	}
	if !reflect.DeepEqual(changes.Created, map[string][]string{"y": {"f"}}) {
		t.Fatalf("created = %v", changes.Created)
	}
	if !reflect.DeepEqual(changes.Updated, map[string][]string{"z": {"f"}}) {
		t.Fatalf("updated = %v", changes.Updated)
	}
	if _, ok := changes.SweptFacts["f"]; !ok || len(changes.SweptFacts) != 1 {
		t.Fatalf("swept facts = %v", changes.SweptFacts)
	}
	if !ChangesTracked(ctx) || ChangesTracked(context.Background()) {
		t.Fatal("ChangesTracked disagrees with the bound context")
	}
}

// TestRetainEventKindComesFromTheWriteMode pins the precedence the reference
// applies: a forced re-extract is a reprocess, then an append, then a retain.
func TestRetainEventKindComesFromTheWriteMode(t *testing.T) {
	cases := []struct {
		updateMode string
		forced     bool
		want       Kind
	}{
		{"", false, KindRetain},
		{"replace", false, KindRetain},
		{"append", false, KindAppend},
		{"append", true, KindReprocess},
		{"", true, KindReprocess},
	}
	for _, testCase := range cases {
		event := RetainEvent("doc", testCase.updateMode, testCase.forced, 3, nil)
		if event.Kind != testCase.want {
			t.Errorf("mode=%q forced=%v: kind = %q, want %q", testCase.updateMode, testCase.forced, event.Kind, testCase.want)
		}
		if event.FactsAdded != 3 {
			t.Errorf("mode=%q: facts_added = %d, want 3", testCase.updateMode, event.FactsAdded)
		}
	}
}

// TestRetainEventCarriesWhatTheSweepRemoved pins the facts_removed and
// observations_removed a retain's tracked changes contribute.
func TestRetainEventCarriesWhatTheSweepRemoved(t *testing.T) {
	changes := newObservationsChanges()
	changes.SweptFacts["f1"] = struct{}{}
	changes.SweptFacts["f2"] = struct{}{}
	changes.Removed["o1"] = []string{"f1"}

	event := RetainEvent("doc", "", false, 1, changes)
	if event.FactsRemoved != 2 {
		t.Fatalf("facts_removed = %d, want 2", event.FactsRemoved)
	}
	if !reflect.DeepEqual(event.ObservationsRemoved.IDs, []string{"o1"}) {
		t.Fatalf("observations_removed = %v", event.ObservationsRemoved)
	}
}

// TestStoredEventHasTheModelsMembers pins the stored JSON shape: the model's
// members, samples always an object with count and ids, and no operation_id.
func TestStoredEventHasTheModelsMembers(t *testing.T) {
	event := NewEvent("doc", KindFactEdited).WithFact("fact").WithRemoved(Of([]string{"b", "a", "a"}))
	stored := storedEvents([]Event{event})
	if len(stored) != 1 {
		t.Fatalf("stored = %v", stored)
	}
	entry := stored[0]
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["operation_id"]; ok {
		t.Fatalf("stored event carries operation_id: %s", raw)
	}
	for _, key := range []string{
		"document_id", "kind", "created_at", "fact_id", "facts_added", "facts_removed",
		"observations_created", "observations_updated", "observations_removed",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("stored event is missing %q: %s", key, raw)
		}
	}
	if decoded["fact_id"] != "fact" {
		t.Fatalf("fact_id = %v", decoded["fact_id"])
	}
	removed := decoded["observations_removed"].(map[string]any)
	if removed["count"].(float64) != 2 {
		t.Fatalf("removed count = %v, want 2 (deduplicated)", removed["count"])
	}
	created := decoded["observations_created"].(map[string]any)
	if ids, ok := created["ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("created sample = %v, want an empty ids list", created)
	}
}

// TestPageRendersTheServedShape pins the read side: a stored object becomes the
// model with the operation filled in, defaults for an absent member, and the
// served timestamp form.
func TestPageRendersTheServedShape(t *testing.T) {
	storedAt := "2026-10-29T10:00:00.500000+00:00"
	page := StoredPage{
		DocumentID: "doc", Total: 1, Limit: 50, Offset: 0,
		Events: []StoredEvent{{
			OperationID: "op-1",
			Members: map[string]any{
				"document_id": "doc", "kind": "retain", "created_at": storedAt,
				"facts_added":          float64(2),
				"observations_removed": map[string]any{"count": float64(1), "ids": []any{"o1"}},
			},
		}},
	}
	response := Page(page)
	if response.Total != 1 || response.Limit != 50 || response.DocumentID != "doc" {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Items) != 1 {
		t.Fatalf("items = %v", response.Items)
	}
	item := response.Items[0]
	if item.Kind != KindRetain || item.FactsAdded != 2 {
		t.Fatalf("item = %+v", item)
	}
	if item.CreatedAt != storedAt {
		t.Fatalf("created_at = %q, want %q", item.CreatedAt, storedAt)
	}
	if item.OperationID == nil || *item.OperationID != "op-1" {
		t.Fatalf("operation_id = %v", item.OperationID)
	}
	if item.FactID != nil {
		t.Fatalf("fact_id = %v, want nil", item.FactID)
	}
	if !reflect.DeepEqual(item.ObservationsRemoved.IDs, []string{"o1"}) || item.ObservationsRemoved.Count != 1 {
		t.Fatalf("removed = %+v", item.ObservationsRemoved)
	}
	if item.ObservationsCreated.Count != 0 || item.ObservationsCreated.IDs == nil {
		t.Fatalf("created default = %+v, want count 0 with an empty ids list", item.ObservationsCreated)
	}
	raw, err := json.Marshal(response.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, fragment := range []string{`"observations_created":{"count":0,"ids":[]}`, `"fact_id":null`, `"operation_id":"op-1"`} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("served item is missing %s: %s", fragment, got)
		}
	}
}

// keysOf lists the map's keys, for a readable failure.
func keysOf[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

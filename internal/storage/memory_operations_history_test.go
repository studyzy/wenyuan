package storage

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/history"
)

// historyFixture seeds a bank with one document and one fact in it, and returns
// the store, the bank and the fact.
func historyFixture(t *testing.T, autoConsolidation bool) (*MemoryStore, string, string) {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryStore("public")
	store.SetAutoConsolidation(autoConsolidation)
	bank := "hist-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "History"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Bob moved to Lisbon in March.", DocumentID: "visit-notes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) == 0 {
		t.Fatalf("retain stored no fact: %#v", outcome.ItemUnitIDs)
	}
	return store, bank, outcome.ItemUnitIDs[0][0]
}

// historyEvents reads the document's timeline, failing on a read error.
func historyEvents(t *testing.T, store *MemoryStore, bank, documentID string) []history.Event {
	t.Helper()
	page, err := store.ListDocumentEvents(context.Background(), bank, documentID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]history.Event, 0, len(page.Events))
	for _, stored := range page.Events {
		events = append(events, historyEvent(stored))
	}
	return events
}

// historyEvent projects one stored event for assertions.
func historyEvent(stored StoredDocumentEvent) history.Event {
	return history.Page(history.StoredPage{Events: []history.StoredEvent{
		{OperationID: stored.OperationID, Members: stored.Members},
	}}).Items[0]
}

// putHistoryOperation records a completed operation to file events on.
func putHistoryOperation(t *testing.T, store *MemoryStore, bank string) Operation {
	t.Helper()
	op, err := store.PutOperation(context.Background(), Operation{
		BankID: bank, OperationType: OperationTypeConsolidate, Status: StatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// findEvent returns the newest event of a kind, or nil.
func findEvent(events []history.Event, kind history.Kind) *history.Event {
	for index := range events {
		if events[index].Kind == kind {
			return &events[index]
		}
	}
	return nil
}

// Clearing one memory's observations records observations_cleared on the
// consolidation the clear queues — and records nothing when automatic
// consolidation is off, because then nothing is queued to record it on.
func TestClearObservationsRecordsOnTheQueuedConsolidation(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		autoConsolidation bool
		wantEvent         bool
	}{
		{"auto consolidation on", true, true},
		{"auto consolidation off", false, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			store, bank, factID := historyFixture(t, testCase.autoConsolidation)
			observationID, err := store.CreateObservation(ctx, bank, "Bob lives in Lisbon.", []string{factID})
			if err != nil {
				t.Fatal(err)
			}
			removed, err := store.DeleteObservations(ctx, bank, factID)
			if err != nil {
				t.Fatal(err)
			}
			if removed != 1 {
				t.Fatalf("clear removed %d observations, want 1", removed)
			}

			events := historyEvents(t, store, bank, "visit-notes")
			cleared := findEvent(events, history.KindObservationsCleared)
			if !testCase.wantEvent {
				if cleared != nil {
					t.Fatalf("a clear with auto consolidation off recorded %+v", *cleared)
				}
				return
			}
			if cleared == nil {
				t.Fatalf("no observations_cleared event in %+v", events)
			}
			if cleared.FactID == nil || *cleared.FactID != factID {
				t.Fatalf("fact_id = %v, want %s", cleared.FactID, factID)
			}
			if got := cleared.ObservationsRemoved.IDs; len(got) != 1 || got[0] != observationID {
				t.Fatalf("observations_removed = %v, want [%s]", got, observationID)
			}
			if cleared.OperationID == nil || *cleared.OperationID == "" {
				t.Fatal("the clear event names no operation")
			}
			op, err := store.GetOperation(ctx, bank, *cleared.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if op.OperationType != OperationTypeConsolidate {
				t.Fatalf("recorded on a %q operation, want a consolidation", op.OperationType)
			}
		})
	}
}

// A consolidation pass's observation writes are filed under the documents their
// source facts came from, and a pass with no operation records nothing.
func TestConsolidationEventsFileUnderTheSourceDocument(t *testing.T) {
	ctx := context.Background()
	store, bank, factID := historyFixture(t, true)
	observationID, err := store.CreateObservation(ctx, bank, "Bob lives in Lisbon.", []string{factID})
	if err != nil {
		t.Fatal(err)
	}
	// A pass with no operation leaves no history: this is the "no operation, no
	// history" rule the reference applies to a synchronous write.
	ctxNoOperation, changes := history.TrackChanges(ctx)
	history.NoteObservationWritten(ctxNoOperation, observationID, []string{factID}, true)
	if events := historyEvents(t, store, bank, "visit-notes"); len(events) != 0 {
		t.Fatalf("an untracked pass recorded %+v", events)
	}

	// The same writes, filed on the operation that ran the pass.
	op := putHistoryOperation(t, store, bank)
	history.RecordConsolidationEvents(ctx, store, bank, changes, op.OperationID)
	page, err := store.ListDocumentEvents(ctx, bank, "visit-notes", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %+v, want one consolidation", page.Events)
	}
	event := historyEvent(page.Events[0])
	if event.Kind != history.KindConsolidation || event.DocumentID != "visit-notes" {
		t.Fatalf("event = %+v", event)
	}
	if got := event.ObservationsCreated.IDs; len(got) != 1 || got[0] != observationID {
		t.Fatalf("observations_created = %v, want [%s]", got, observationID)
	}
	if event.OperationID == nil || *event.OperationID != op.OperationID {
		t.Fatalf("operation_id = %v, want %s", event.OperationID, op.OperationID)
	}
}

// The history pages over operations, newest first, and a document with none
// reads an empty page rather than an error.
func TestListDocumentEventsPagesOperations(t *testing.T) {
	ctx := context.Background()
	store, bank, factID := historyFixture(t, true)
	first, err := store.ListDocumentEvents(ctx, bank, "visit-notes", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 0 || len(first.Events) != 0 {
		t.Fatalf("a quiet document read %+v", first)
	}

	// Two operations, each carrying one event for the document.
	for range []int{1, 2} {
		op := putHistoryOperation(t, store, bank)
		history.AppendOperationEvents(ctx, store, op.OperationID,
			history.NewEvent("visit-notes", history.KindRetain).WithFact(factID))
	}
	page, err := store.ListDocumentEvents(ctx, bank, "visit-notes", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("total = %d, want 2 operations", page.Total)
	}
	if len(page.Events) != 1 {
		t.Fatalf("a limit of 1 returned %d events", len(page.Events))
	}
	if page.Limit != 1 || page.Offset != 0 {
		t.Fatalf("page envelope = %+v", page)
	}
}

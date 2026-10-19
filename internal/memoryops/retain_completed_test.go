package memoryops

import (
	"context"
	"net/http"
	"regexp"
	"testing"
)

// eventCalls returns the calls that raised one event type, in the order the
// handler made them.
func (r *recordingDefenseNotifier) eventCalls(event string) []map[string]any {
	out := make([]map[string]any, 0, len(r.calls))
	for i, raised := range r.events {
		if raised == event {
			out = append(out, r.calls[i])
		}
	}
	return out
}

// hexUUID is uuid4().hex: a bare 32-character hexadecimal identifier, which is
// what the reference's event carries when the retain has no operation of its
// own.
var hexUUID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// A synchronous retain raises one retain.completed delivery per content item,
// each carrying only what that item contributed — the document it named, its
// tags, and how many units that document owns — while the events of one retain
// share an operation id.
func TestRetainCompletedRaisesOneEventPerItem(t *testing.T) {
	notifier := &recordingDefenseNotifier{}
	mux, _ := defenseServer(t, nil, notifier)

	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"Alice moved to Berlin.","document_id":"doc-a","tags":["a","b"]},{"content":"Bob plays the cello."}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	events := notifier.eventCalls("retain.completed")
	if len(events) != 2 {
		t.Fatalf("retain.completed events = %d, want one per content item", len(events))
	}

	named := events[0]
	if named["document_id"] != "doc-a" {
		t.Fatalf("document_id = %v, want the item's own document", named["document_id"])
	}
	tags, ok := named["tags"].(TagList)
	if !ok || len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("tags = %v, want the item's tags", named["tags"])
	}
	if named["memory_unit_count"] != 1 {
		t.Fatalf("memory_unit_count = %v, want the document's own count", named["memory_unit_count"])
	}
	for _, absent := range []string{"unit_ids", "items_count", "status"} {
		if value, present := named[absent]; present {
			t.Fatalf("the reference's data carries no %s, got %v", absent, value)
		}
	}

	// An item that named no document reports no document and no count: there is
	// no document to count against, and the reference leaves both members unset
	// rather than sending null.
	anonymous := events[1]
	if len(anonymous) != 1 {
		t.Fatalf("event for an item without a document = %v, want only the operation id", anonymous)
	}

	operationID, ok := named["operation_id"].(string)
	if !ok || !hexUUID.MatchString(operationID) {
		t.Fatalf("operation_id = %v, want uuid4().hex", named["operation_id"])
	}
	if anonymous["operation_id"] != operationID {
		t.Fatalf("operation ids differ: %v and %v", operationID, anonymous["operation_id"])
	}
}

// Both events of a retain that named one document report that document's whole
// count — the number the Documents API serves — rather than the count of the
// units the item itself wrote.
func TestRetainCompletedCountsTheDocumentNotTheItem(t *testing.T) {
	notifier := &recordingDefenseNotifier{}
	mux, store := defenseServer(t, nil, notifier)

	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"Alice moved to Berlin.","document_id":"doc-a"},`+
			`{"content":"She works at Acme.","document_id":"doc-a"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	document, err := store.GetDocument(context.Background(), "b1", "doc-a")
	if err != nil {
		t.Fatal(err)
	}
	if document.MemoryUnitCount == 0 {
		t.Fatal("the fixture document owns no units, so the count proves nothing")
	}
	events := notifier.eventCalls("retain.completed")
	if len(events) != 2 {
		t.Fatalf("retain.completed events = %d, want one per content item", len(events))
	}
	for i, event := range events {
		if event["memory_unit_count"] != document.MemoryUnitCount {
			t.Fatalf("event %d memory_unit_count = %v, want the document's %d",
				i, event["memory_unit_count"], document.MemoryUnitCount)
		}
	}
}

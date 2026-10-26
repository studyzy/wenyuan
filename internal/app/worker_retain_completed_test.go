package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// queuedRetainCompleted returns the retain.completed event bodies one bank's
// queued webhook deliveries carry, parsed back from the bytes a receiver would
// have verified. Order is not asserted: the events are keyed by the document
// they name.
func queuedRetainCompleted(t *testing.T, store storage.Store, bankID string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: bankID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeWebhookDelivery {
			continue
		}
		body, _ := op.Payload["body"].(string)
		if body == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(body), &event); err != nil {
			t.Fatalf("delivery body is not the event JSON: %v (%s)", err, body)
		}
		if event["event"] == "retain.completed" {
			events = append(events, event)
		}
	}
	return events
}

// An asynchronous retain raises the same retain.completed events the synchronous
// path does: one delivery per submitted content item, all sharing the operation
// that ran the write, each carrying the document that item named, its tags, and
// how many memory units the document owns afterwards.
func TestAsyncRetainRaisesCompletionEvents(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	application, err := New(ctx, testConfig(), Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "w14-async", Name: "w14-async"}); err != nil {
		t.Fatal(err)
	}
	// A receiver the delivery guard refuses: the event is still queued (that is
	// what this test reads), only the attempt is a permanent no-op, so nothing
	// leaves the machine.
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w14-hook", BankID: "w14-async", URL: "http://127.0.0.1/hook",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"items":[{"content":"Alice moved to Berlin.","document_id":"w14-a","tags":["a","b"]},` +
		`{"content":"Bob plays the cello.","document_id":"w14-b"}],"async":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/w14-async/memories", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	drainUntilSettled(t, store, "w14-async")

	// The operation the worker ran is the child retain operation, and it is the
	// operation id every event of this retain carries.
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "w14-async", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	operationID := ""
	for _, op := range page.Operations {
		if op.OperationType == storage.OperationTypeRetain {
			operationID = op.OperationID
		}
	}
	if operationID == "" {
		t.Fatal("no retain operation ran the queued write")
	}

	events := queuedRetainCompleted(t, store, "w14-async")
	if len(events) != 2 {
		t.Fatalf("retain.completed events = %d, want one per content item", len(events))
	}
	byDocument := make(map[string]map[string]any, len(events))
	for _, event := range events {
		if event["bank_id"] != "w14-async" {
			t.Fatalf("bank_id = %v", event["bank_id"])
		}
		if event["status"] != "completed" {
			t.Fatalf("status = %v, want completed", event["status"])
		}
		if event["operation_id"] != operationID {
			t.Fatalf("operation_id = %v, want the retain operation %s", event["operation_id"], operationID)
		}
		data, ok := event["data"].(map[string]any)
		if !ok {
			t.Fatalf("data = %v, want an object", event["data"])
		}
		documentID, _ := data["document_id"].(string)
		byDocument[documentID] = data
	}

	named, ok := byDocument["w14-a"]
	if !ok {
		t.Fatalf("no event for w14-a: %v", byDocument)
	}
	if named["memory_unit_count"] != float64(1) {
		t.Fatalf("memory_unit_count = %v, want the document's own count", named["memory_unit_count"])
	}
	tags, ok := named["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("tags = %v, want the item's tags", named["tags"])
	}
	if _, ok := byDocument["w14-b"]; !ok {
		t.Fatalf("no event for w14-b: %v", byDocument)
	}
	for _, absent := range []string{"items_count", "unit_ids", "operation_ids"} {
		if _, present := named[absent]; present {
			t.Fatalf("event data carries %q, which the reference never sends", absent)
		}
	}
}

// A queued retain that never writes raises no completion event: the reference's
// outbox callback runs inside the retain transaction, so a rolled-back write
// leaves no delivery behind.
func TestAsyncRetainRaisesNoEventWhenTheWriteFails(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "w14-fail", Name: "w14-fail"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w14-hook", BankID: "w14-fail", URL: "http://127.0.0.1/hook",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// A child retain operation whose payload cannot be decoded never reaches a
	// write, so it can raise nothing.
	if _, err := store.EnqueueOperation(ctx, storage.EnqueueRequest{
		Operation: storage.Operation{
			OperationID:   "w14-bad",
			BankID:        "w14-fail",
			OperationType: storage.OperationTypeRetain,
			Payload:       map[string]any{"retain_request": "not-an-object"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	drainUntilSettled(t, store, "w14-fail")
	if events := queuedRetainCompleted(t, store, "w14-fail"); len(events) != 0 {
		t.Fatalf("a failed write raised %d events, want none", len(events))
	}
}

// A finished queued retain leaves the reference's `progress` object on its
// operation.
//
// The reference's heartbeat merges a `progress` member into the operation's
// result_metadata, and the snapshot its retain leaves after the batch commits is
// `stage="storing"`, `processed`/`total`, `detail={"facts_committed": n}` with an
// `at` timestamp (engine/retain/orchestrator.py:2866-2879, written by
// engine/memory_engine.py:5160-5179). This service recorded no progress at all
// for a retain, so the operation list answered `progress: null` where the
// reference answers an object.
func TestQueuedRetainRecordsTheStoringProgress(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	application, err := New(ctx, testConfig(), Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "prog-bank", Name: "prog-bank"}); err != nil {
		t.Fatal(err)
	}
	body := `{"items":[{"content":"Alice moved to Berlin."}],"async":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/prog-bank/memories", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drainUntilSettled(t, store, "prog-bank")

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "prog-bank", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var retain *storage.Operation
	for i := range page.Operations {
		if page.Operations[i].OperationType == storage.OperationTypeRetain {
			retain = &page.Operations[i]
		}
	}
	if retain == nil {
		t.Fatal("no retain operation ran the queued write")
	}
	progress, ok := retain.ResultMetadata["progress"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata has no progress object: %v", retain.ResultMetadata)
	}
	for _, key := range []string{"stage", "at", "processed", "total", "detail"} {
		if _, present := progress[key]; !present {
			t.Fatalf("progress = %v, want the reference's %q member", progress, key)
		}
	}
	if progress["stage"] != "storing" {
		t.Fatalf("stage = %v, want storing", progress["stage"])
	}
	detail, ok := progress["detail"].(map[string]any)
	if !ok {
		t.Fatalf("detail = %v, want an object", progress["detail"])
	}
	if _, present := detail["facts_committed"]; !present {
		t.Fatalf("detail = %v, want facts_committed", detail)
	}
}

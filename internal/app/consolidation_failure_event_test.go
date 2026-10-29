package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The failure half of the consolidation event is the reference's own body: the
// same envelope a completed pass fires, with status "failed" and a data object
// carrying only the error, because every count member is unset and the
// reference's event model drops an unset member rather than sending it as null.
// The bytes come from a live delivery of the reference's failed pass.
func TestWebhookEventBodyConsolidationFailure(t *testing.T) {
	at := time.Date(2026, 10, 17, 21, 9, 36, 952104000, time.UTC)
	body, err := marshalWebhookEvent("consolidation.completed", "parity-w12-ref",
		"d544c9d2-a6a2-461e-b69d-131c40f45443", "", "failed", at,
		map[string]any{"error_message": "TypeError: 'int' object is not iterable"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event":"consolidation.completed","bank_id":"parity-w12-ref",` +
		`"operation_id":"d544c9d2-a6a2-461e-b69d-131c40f45443","status":"failed",` +
		`"timestamp":"2026-10-17T21:09:36.952104Z",` +
		`"data":{"error_message":"TypeError: 'int' object is not iterable"}}`
	if string(body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", body, want)
	}
}

// A consolidation that fails fires the event its subscribers registered for:
// the same consolidation.completed a completed pass fires, with status "failed"
// and the error in place of the counts. Without this the failure paths
// (retryable, terminal, and wall-clock alike) went silent, where the reference
// reports every one of them.
func TestFailedConsolidationFiresTheFailureEvent(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: "b1", Items: []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://example.test/hook",
		Secret: "s", EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	op := storage.Operation{OperationID: "op-c", BankID: "b1", OperationType: storage.OperationTypeConsolidate}

	// The model is available (the failure under test is the store read); the
	// no-model selection is its own outcome and fires nothing.
	err := archiveProcessor{store: brokenFactsStore{MemoryStore: store}, provider: &scriptedProvider{}}.Process(ctx, op)
	if err == nil {
		t.Fatal("a pass whose store read failed must fail")
	}

	body := consolidationDeliveryBody(t, store, "b1")
	var event struct {
		Event       string                     `json:"event"`
		BankID      string                     `json:"bank_id"`
		OperationID string                     `json:"operation_id"`
		Status      string                     `json:"status"`
		Data        map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("body = %s: %v", body, err)
	}
	if event.Event != "consolidation.completed" || event.Status != "failed" {
		t.Fatalf("event = %s/%s, want consolidation.completed/failed", event.Event, event.Status)
	}
	if event.BankID != "b1" || event.OperationID != "op-c" {
		t.Fatalf("envelope = %s/%s, want the failed operation", event.BankID, event.OperationID)
	}
	// The counts are unset on a failed pass, so the data object holds the error
	// alone — no null placeholder for a count nobody can report.
	if len(event.Data) != 1 {
		t.Fatalf("data = %v, want only the error", event.Data)
	}
	var message string
	if raw, ok := event.Data["error_message"]; !ok {
		t.Fatalf("data = %v, want error_message", event.Data)
	} else if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "store unavailable") {
		t.Fatalf("error_message = %q, want the pass's own error", message)
	}
}

// The completed half of the same event is unchanged by the failure case sharing
// its emitter: a pass that finished still reports what it applied.
func TestCompletedConsolidationFiresTheCounts(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: "b1", Items: []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://example.test/hook",
		Secret: "s", EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	op := storage.Operation{OperationID: "op-c", BankID: "b1", OperationType: storage.OperationTypeConsolidate}
	if err := (archiveProcessor{store: store, provider: &scriptedProvider{}}).Process(ctx, op); err != nil {
		t.Fatalf("consolidation with a model: %v", err)
	}
	if got := string(consolidationDeliveryBody(t, store, "b1")); !strings.Contains(got, `"status":"completed"`) ||
		!strings.Contains(got, `"data":{"observations_created":1,"observations_updated":0,"observations_deleted":0}`) {
		t.Fatalf("completed body = %s", got)
	}
}

// consolidationDeliveryBody returns the body of the one queued webhook delivery
// of a bank.
func consolidationDeliveryBody(t *testing.T, store storage.Store, bankID string) []byte {
	t.Helper()
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: bankID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, queued := range page.Operations {
		if queued.OperationType != storage.OperationTypeWebhookDelivery {
			continue
		}
		body, _ := queued.Payload["body"].(string)
		return []byte(body)
	}
	t.Fatal("no webhook delivery was queued for the failed consolidation")
	return nil
}

// brokenFactsStore refuses the pass's fact read, which is how a consolidation
// that fails outright is produced: a provider-level failure is absorbed as a
// per-fact failure inside the pass, while a store that cannot answer ends it
// with an error.
type brokenFactsStore struct {
	*storage.MemoryStore
}

func (brokenFactsStore) ConsolidationFacts(context.Context, string, int) ([]storage.ConsolidationFact, error) {
	return nil, errors.New("store unavailable")
}

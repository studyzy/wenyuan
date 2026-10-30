package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/webhooks"
)

// The Memory Defense notifier creates exactly one delivery per subscribed
// webhook, through the same enqueue path as every other event.
func TestMemoryDefenseNotifierEnqueuesDelivery(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://example.test/hook",
		Secret: "s", EventTypes: []string{"memory_defense.triggered"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w2", BankID: "b1", URL: "https://example.test/other",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	webhookNotifier{store: store}.EnqueueWebhookEvent(ctx, "b1", "memory_defense.triggered", map[string]any{"action": "redact"})

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeWebhookDelivery {
			continue
		}
		deliveries++
		if op.Payload["webhook_id"] != "w1" {
			t.Fatalf("delivery for the wrong webhook: %v", op.Payload)
		}
		if op.Payload["event_type"] != "memory_defense.triggered" {
			t.Fatalf("delivery event type = %v", op.Payload["event_type"])
		}
		data, _ := op.Payload["data"].(map[string]any)
		if data["action"] != "redact" {
			t.Fatalf("delivery data = %v", data)
		}
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

// A completed retain raises retain.completed for the webhooks subscribed to it,
// and the delivery carries the units the write created.
func TestRetainCompletedEventIsRaised(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://example.test/hook",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	webhookNotifier{store: store}.EnqueueWebhookEvent(ctx, "b1", "retain.completed", map[string]any{"document_id": "doc-1"})

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeWebhookDelivery {
			continue
		}
		deliveries++
		if eventType, _ := op.Payload["event_type"].(string); eventType != "retain.completed" {
			t.Fatalf("event type = %q", eventType)
		}
		if documentID, _ := op.Payload["data"].(map[string]any)["document_id"].(string); documentID != "doc-1" {
			t.Fatalf("payload = %+v", op.Payload)
		}
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want one", deliveries)
	}
}

// A delivery's outcome is recorded on its operation, so an operator can tell a
// refusing endpoint from an unreachable one.
func TestWebhookDeliveryRecordsReceiverStatus(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer receiver.Close()
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL, EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-1", BankID: "b1", OperationType: storage.OperationTypeWebhookDelivery,
		Status:    storage.StatusPending,
		Payload:   map[string]any{"webhook_id": "w1", "event_type": "retain.completed"},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	consumer := NewQueueConsumer(store, nil, nil, nil, config.WorkerConfig{
		ID: "w1", MaxRetries: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		Lease: time.Minute, Concurrency: 1, PollInterval: time.Millisecond,
	}, config.WorkerTuning{}, webhooks.NewGuard(webhooks.GuardConfig{AllowPrivateHosts: true, AllowInsecure: true}), ExtractionSettings{}, false, false, false, "public", nil)
	// Drive the real consumer for long enough to settle the queued delivery,
	// then stop it: the settlement path under test is the one a deployment runs.
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(runCtx) }()
	var page storage.WebhookDeliveryPage
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		page, err = store.ListWebhookDeliveries(ctx, "b1", "w1", 10, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Deliveries) == 1 && page.Deliveries[0].LastResponseStatus != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	consumer.Stop()
	cancel()
	if len(page.Deliveries) != 1 {
		t.Fatalf("deliveries = %d, want one", len(page.Deliveries))
	}
	delivery := page.Deliveries[0]
	if delivery.LastResponseStatus == nil || *delivery.LastResponseStatus != http.StatusTeapot {
		t.Fatalf("status code = %v, want the receiver's %d", delivery.LastResponseStatus, http.StatusTeapot)
	}
	// A receiver's rejection is retried on the reference's ladder, not failed on
	// the spot: the attempt is spent and the next rung scheduled.
	if delivery.Status != string(storage.StatusPending) {
		t.Fatalf("status = %q, want a retried delivery still pending", delivery.Status)
	}
	if delivery.Attempts != 2 {
		t.Fatalf("attempts = %d, want the attempt to have been spent", delivery.Attempts)
	}
	if delivery.NextRetryAt == nil {
		t.Fatal("a retried delivery must carry its next attempt time")
	}
}

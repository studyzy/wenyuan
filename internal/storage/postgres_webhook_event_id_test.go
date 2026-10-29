//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// The event id a delivery was queued with survives the database: the delivery
// listing reads it back from the queued task payload, so it is the same value the
// receiver saw in the body and in X-Hindsight-Event-Id, and it is empty for a
// delivery queued before event ids existed.
func TestPostgresWebhookDeliveryEventID(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	webhook, err := store.PutWebhook(ctx, Webhook{
		ID: pgUUID("w-evid"), BankID: "vec-bank", URL: "https://example.test/hook",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("put webhook: %v", err)
	}
	now := time.Now().UTC()
	oldDelivery, newDelivery := pgUUID("pg-delivery-old"), pgUUID("pg-delivery-new")
	for i, tc := range []struct{ id, eventID string }{
		{oldDelivery, ""},
		{newDelivery, "evt-pg-1"},
	} {
		if _, err := store.PutOperation(ctx, Operation{
			OperationID:   tc.id,
			BankID:        "vec-bank",
			OperationType: OperationTypeWebhookDelivery,
			Status:        StatusCompleted,
			Payload: map[string]any{
				"webhook_id": webhook.ID, "event_type": "retain.completed",
				"url": "https://example.test/hook", "event_id": tc.eventID,
			},
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("put delivery %s: %v", tc.id, err)
		}
	}

	page, err := store.ListWebhookDeliveries(ctx, "vec-bank", webhook.ID, 10, "")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	byID := make(map[string]WebhookDelivery, len(page.Deliveries))
	for _, delivery := range page.Deliveries {
		byID[delivery.ID] = delivery
	}
	if got := byID[newDelivery]; got.EventID != "evt-pg-1" {
		t.Fatalf("stamped delivery event_id = %q, want evt-pg-1", got.EventID)
	}
	if got := byID[oldDelivery]; got.EventID != "" {
		t.Fatalf("unstamped delivery event_id = %q, want empty", got.EventID)
	}
}

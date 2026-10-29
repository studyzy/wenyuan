package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The delivery listing carries event_id, null for a delivery queued before event
// ids existed and the value it was stamped with otherwise. The id is the one the
// receiver saw in its body and in X-Hindsight-Event-Id.
func TestWebhookDeliveriesExposeEventID(t *testing.T) {
	mux, store := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"
	created := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook","event_types":["retain.completed"]}`)
	var webhook Webhook
	if err := json.Unmarshal(created.Body.Bytes(), &webhook); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ id, eventID string }{
		{"op-old", ""},
		{"op-new", "evt-listed"},
	} {
		time.Sleep(1200 * time.Microsecond)
		if _, err := store.PutOperation(context.Background(), storage.Operation{
			OperationID:   tc.id,
			BankID:        "b1",
			OperationType: storage.OperationTypeWebhookDelivery,
			Status:        storage.StatusCompleted,
			Payload: map[string]any{
				"webhook_id": webhook.ID, "event_type": "retain.completed",
				"url": "https://example.test/hook", "event_id": tc.eventID,
			},
			CreatedAt: timeAt(i),
			UpdatedAt: timeAt(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	rec := send(mux, http.MethodGet, base+"/"+webhook.ID+"/deliveries", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deliveries: %d (body %s)", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []struct {
			ID      string  `json:"id"`
			EventID *string `json:"event_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	// Newest first: the stamped delivery, then the unstamped one.
	if page.Items[0].ID != "op-new" || page.Items[0].EventID == nil || *page.Items[0].EventID != "evt-listed" {
		t.Fatalf("stamped delivery = %+v, want event_id evt-listed", page.Items[0])
	}
	if page.Items[1].ID != "op-old" || page.Items[1].EventID != nil {
		t.Fatalf("unstamped delivery = %+v, want a null event_id", page.Items[1])
	}
	// The member is present as null rather than absent, which is what the
	// reference's model serializes for a None default.
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if value, present := raw.Items[1]["event_id"]; !present || value != nil {
		t.Fatalf("an unstamped delivery must carry a null event_id: %v", raw.Items[1])
	}
}

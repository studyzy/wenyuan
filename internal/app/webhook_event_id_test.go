package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/webhooks"
)

// delivered is one request a test receiver observed.
type delivered struct {
	body    []byte
	headers http.Header
}

// recordingReceiver is an HTTP endpoint that records every request it gets and
// answers with the status the test named.
func recordingReceiver(t *testing.T, status int) (*httptest.Server, func() []delivered) {
	t.Helper()
	var mu sync.Mutex
	var seen []delivered
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, delivered{body: body, headers: r.Header.Clone()})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, func() []delivered {
		mu.Lock()
		defer mu.Unlock()
		return append([]delivered(nil), seen...)
	}
}

// deliveryWithID queues one delivery of a pre-rendered body, which is what the
// enqueue path leaves behind: the bytes the signature covers.
func deliveryWithID(t *testing.T, store storage.Store, opID, webhookID, body, eventID string) storage.Operation {
	t.Helper()
	op, err := store.PutOperation(context.Background(), storage.Operation{
		OperationID: opID, BankID: "b1",
		OperationType: storage.OperationTypeWebhookDelivery,
		Status:        storage.StatusPending,
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Payload: map[string]any{
			"webhook_id": webhookID, "event_type": "consolidation.completed",
			"event_id": eventID, "body": body,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// The event envelope leads with event_id, the reference's first model member,
// and a member the emitter left out of the data object is still absent.
func TestWebhookEventBodyCarriesEventID(t *testing.T) {
	at := time.Date(2026, 10, 17, 20, 27, 46, 609832000, time.UTC)
	body, err := marshalWebhookEvent("consolidation.completed", "b1", "op-1", "evt-1", "completed", at,
		map[string]any{"observations_created": 2})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event_id":"evt-1","event":"consolidation.completed","bank_id":"b1","operation_id":"op-1",` +
		`"status":"completed","timestamp":"2026-10-17T20:27:46.609832Z",` +
		`"data":{"observations_created":2}}`
	if string(body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", body, want)
	}
}

// A body rendered without an id — the re-render a delivery queued before event
// ids existed goes through — omits the member rather than sending an empty one.
func TestWebhookEventBodyOmitsEmptyEventID(t *testing.T) {
	body, err := marshalWebhookEvent("retain.completed", "b1", "op-1", "", "completed",
		time.Date(2026, 10, 17, 20, 27, 46, 0, time.UTC), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(body), `{"event_id"`) || strings.Contains(string(body), `"event_id"`) {
		t.Fatalf("an event with no id must not carry the member: %s", body)
	}
	if !strings.HasPrefix(string(body), `{"event":"retain.completed"`) {
		t.Fatalf("body = %s", body)
	}
}

// Every delivery of one fired event carries the same event id — across the
// bank's own webhook and the deployment-level target — while each is its own
// delivery, so the two ids tell a receiver different things. Both are readable
// from the delivery listing.
func TestWebhookEventIDSharedAcrossWebhooks(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"w1", "w2"} {
		if _, err := store.PutWebhook(ctx, storage.Webhook{
			ID: id, BankID: "b1", URL: "https://example.test/" + id,
			EventTypes: []string{"consolidation.completed"}, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	processor := archiveProcessor{store: store, webhookGuard: webhooks.NewGuard(webhooks.GuardConfig{
		Global: &webhooks.GlobalTarget{
			URL: "https://global.test/receiver", EventTypes: []string{"consolidation.completed"},
		},
	})}
	processor.enqueueWebhookEvent(ctx, "b1", "consolidation.completed", map[string]any{})

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	eventIDs := make(map[string]bool)
	deliveryIDs := make(map[string]bool)
	queued := 0
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeWebhookDelivery {
			continue
		}
		queued++
		eventID, _ := op.Payload["event_id"].(string)
		if eventID == "" {
			t.Fatalf("delivery %s carries no event id: %v", op.OperationID, op.Payload)
		}
		eventIDs[eventID] = true
		deliveryIDs[op.OperationID] = true
		body, _ := op.Payload["body"].(string)
		if !strings.HasPrefix(body, `{"event_id":"`+eventID+`"`) {
			t.Fatalf("the rendered body must name the delivery's event id first: %s", body)
		}
	}
	if queued != 3 {
		t.Fatalf("queued %d deliveries, want one per subscriber (2 bank + 1 global)", queued)
	}
	if len(eventIDs) != 1 {
		t.Fatalf("event ids = %v, want one shared id for the event", eventIDs)
	}
	if len(deliveryIDs) != 3 {
		t.Fatalf("delivery ids = %v, want one per delivery", deliveryIDs)
	}

	// The delivery listing exposes the same event id, and each delivery's own id.
	listed, err := store.ListWebhookDeliveries(ctx, "b1", "w1", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Deliveries) != 1 {
		t.Fatalf("listed %d deliveries for w1, want 1", len(listed.Deliveries))
	}
	for want := range eventIDs {
		if listed.Deliveries[0].EventID != want {
			t.Fatalf("listed event_id = %q, want %q", listed.Deliveries[0].EventID, want)
		}
	}
}

// A delivery sends X-Hindsight-Event-Id and X-Hindsight-Delivery-Id, and the
// signature still covers the exact bytes on the wire — which now include the
// event id, so a receiver verifying over the raw body sees the same id.
func TestWebhookDeliverySendsEventAndDeliveryIDs(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	receiver, requests := recordingReceiver(t, http.StatusOK)
	const secret = "s3cret"
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL, Secret: secret,
		EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := marshalWebhookEvent("consolidation.completed", "b1", "op-src", "evt-1", "completed",
		time.Now().UTC(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	op := deliveryWithID(t, store, "op-delivery", "w1", string(body), "evt-1")
	if err := (archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}).Process(ctx, op); err != nil {
		t.Fatalf("delivery: %v", err)
	}

	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("receiver saw %d requests, want 1", len(seen))
	}
	got := seen[0]
	if value := got.headers.Get("X-Hindsight-Event-Id"); value != "evt-1" {
		t.Fatalf("X-Hindsight-Event-Id = %q, want evt-1", value)
	}
	if value := got.headers.Get("X-Hindsight-Delivery-Id"); value != "op-delivery" {
		t.Fatalf("X-Hindsight-Delivery-Id = %q, want op-delivery", value)
	}
	if value := got.headers.Get("X-Hindsight-Event"); value != "consolidation.completed" {
		t.Fatalf("X-Hindsight-Event = %q", value)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(got.body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); got.headers.Get("X-Hindsight-Signature") != want {
		t.Fatal("the signature must cover the raw body, including the event id")
	}
	var decoded struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.EventID != "evt-1" {
		t.Fatalf("body event_id = %q, want evt-1", decoded.EventID)
	}
}

// A retry repeats both ids: the queued payload is not rebuilt between attempts,
// so the receiver sees the delivery it already saw twice rather than two events.
func TestWebhookRetryRepeatsEventAndDeliveryIDs(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	receiver, requests := recordingReceiver(t, http.StatusServiceUnavailable)
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL,
		EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := marshalWebhookEvent("consolidation.completed", "b1", "op-src", "evt-1", "completed",
		time.Now().UTC(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	op := deliveryWithID(t, store, "op-delivery", "w1", string(body), "evt-1")
	processor := archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}
	if err := processor.Process(ctx, op); err == nil {
		t.Fatal("a 5xx must be retried, not reported as delivered")
	}
	// The retry is the same operation with its attempt count bumped, which is
	// what the worker hands back to the processor.
	op.RetryCount++
	if err := processor.Process(ctx, op); err == nil {
		t.Fatal("a 5xx must still be retried")
	}
	for i, got := range requests() {
		if value := got.headers.Get("X-Hindsight-Event-Id"); value != "evt-1" {
			t.Fatalf("attempt %d: X-Hindsight-Event-Id = %q, want evt-1", i, value)
		}
		if value := got.headers.Get("X-Hindsight-Delivery-Id"); value != "op-delivery" {
			t.Fatalf("attempt %d: X-Hindsight-Delivery-Id = %q, want op-delivery", i, value)
		}
	}
}

// A delivery queued before event ids existed sends no empty id header, which is
// what the reference does for a task its manager never stamped.
func TestWebhookDeliveryWithoutEventIDSendsNoHeader(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	receiver, requests := recordingReceiver(t, http.StatusOK)
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL,
		EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := marshalWebhookEvent("consolidation.completed", "b1", "op-src", "", "completed",
		time.Now().UTC(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	op := deliveryWithID(t, store, "op-delivery", "w1", string(body), "")
	if err := (archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}).Process(ctx, op); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("receiver saw %d requests, want 1", len(seen))
	}
	if _, present := seen[0].headers["X-Hindsight-Event-Id"]; present {
		t.Fatal("a delivery with no event id must not send the header at all")
	}
	if value := seen[0].headers.Get("X-Hindsight-Delivery-Id"); value != "op-delivery" {
		t.Fatalf("X-Hindsight-Delivery-Id = %q, want the delivery's own id", value)
	}
}

// A configured header must not be able to forge the id headers or a signature;
// the service-controlled ones win. Unrelated custom headers still pass through.
func TestWebhookCustomHeadersCannotOverrideIDs(t *testing.T) {
	cases := map[string]string{
		"lowercase":  "x-hindsight-event-id",
		"mixedcase":  "X-Hindsight-Event-ID",
		"delivery":   "x-hindsight-delivery-id",
		"event_type": "X-Hindsight-Event",
		"signature":  "X-Hindsight-Signature",
		"hub":        "X-Hub-Signature-256",
		"signature2": "X-Hindsight-Signature-V2",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			ctx := context.Background()
			if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
				t.Fatal(err)
			}
			receiver, requests := recordingReceiver(t, http.StatusOK)
			if _, err := store.PutWebhook(ctx, storage.Webhook{
				ID: "w1", BankID: "b1", URL: receiver.URL, Secret: "s",
				EventTypes: []string{"consolidation.completed"}, Enabled: true,
				HTTPConfig: map[string]any{
					"headers": map[string]any{header: "forged", "X-Custom": "kept"},
				},
			}); err != nil {
				t.Fatal(err)
			}
			body, err := marshalWebhookEvent("consolidation.completed", "b1", "op-src", "evt-real", "completed",
				time.Now().UTC(), map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			op := deliveryWithID(t, store, "op-delivery", "w1", string(body), "evt-real")
			if err := (archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}).Process(ctx, op); err != nil {
				t.Fatalf("delivery: %v", err)
			}
			seen := requests()
			if len(seen) != 1 {
				t.Fatalf("receiver saw %d requests, want 1", len(seen))
			}
			got := seen[0].headers
			if value := got.Get("X-Hindsight-Event-Id"); value != "evt-real" {
				t.Fatalf("X-Hindsight-Event-Id = %q, want evt-real", value)
			}
			if value := got.Get("X-Hindsight-Delivery-Id"); value != "op-delivery" {
				t.Fatalf("X-Hindsight-Delivery-Id = %q, want op-delivery", value)
			}
			if value := got.Get("X-Hindsight-Event"); value != "consolidation.completed" {
				t.Fatalf("X-Hindsight-Event = %q", value)
			}
			for _, name := range []string{"X-Hindsight-Signature", "X-Hub-Signature-256", "X-Hindsight-Signature-V2"} {
				if got.Get(name) == "forged" {
					t.Fatalf("%s was forged by a configured header", name)
				}
			}
			if got.Get("X-Custom") != "kept" {
				t.Fatal("an unrelated custom header must still pass through")
			}
		})
	}
}

package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/webhooks"
	"github.com/studyzy/wenyuan/internal/worker"
)

// deliveryTestGuard allows the private httptest receivers so only the redirect
// behavior is under test.
func deliveryTestGuard() *webhooks.Guard {
	return webhooks.NewGuard(webhooks.GuardConfig{AllowPrivateHosts: true, AllowInsecure: true})
}

func webhookDeliveryOperation(id string) storage.Operation {
	return storage.Operation{
		OperationID:   "op-" + id,
		BankID:        "b1",
		OperationType: storage.OperationTypeWebhookDelivery,
		Payload:       map[string]any{"webhook_id": id, "event_type": "retain.completed"},
	}
}

// A receiver that answers with a redirect must not be able to steer the
// delivery elsewhere: the guard vetted the registered URL, not the hop, so the
// redirect is refused as a delivery failure and the target is never reached.
func TestWebhookDeliveryDoesNotFollowRedirect(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/metadata", http.StatusTemporaryRedirect)
	}))
	defer receiver.Close()
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL, EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	processor := archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}
	err := processor.Process(ctx, webhookDeliveryOperation("w1"))
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("redirect delivery error = %v, want a permanent refusal", err)
	}
	if internalHits.Load() != 0 {
		t.Fatal("the redirect target was reached; a hop the guard never vetted must not be followed")
	}
}

// A target that fails the guard at delivery time is refused before a request is
// made, so a URL that was accepted when stored cannot be delivered after the
// policy or its resolution changed.
func TestWebhookDeliveryRefusesRevalidation(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	// A link-local address the strict guard refuses; it is stored directly,
	// modelling a target that passed at registration and no longer does.
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://169.254.169.254/latest/meta-data/",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	processor := archiveProcessor{store: store} // nil guard => the strict default
	err := processor.Process(ctx, webhookDeliveryOperation("w1"))
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("re-validation error = %v, want a permanent refusal", err)
	}
	if !errors.Is(err, storage.ErrWebhookURLNotAllowed) {
		t.Fatalf("error = %v, want it to carry ErrWebhookURLNotAllowed", err)
	}
}

// A global target is delivered alongside a bank's own webhooks and independent
// of them: one subscribed event produces one delivery to the deployment-level
// receiver carrying its endpoint and secret, plus one per subscribed bank
// webhook.
func TestGlobalWebhookEnqueuesAlongsideBankWebhook(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://example.test/hook",
		EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	processor := archiveProcessor{store: store, webhookGuard: webhooks.NewGuard(webhooks.GuardConfig{
		Global: &webhooks.GlobalTarget{
			URL: "https://global.test/receiver", Secret: "g-secret",
			EventTypes: []string{"consolidation.completed"},
		},
	})}
	processor.enqueueWebhookEvent(ctx, "b1", "consolidation.completed", map[string]any{})

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var bankDelivery, globalDelivery map[string]any
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeWebhookDelivery {
			continue
		}
		if isGlobal, _ := op.Payload["global"].(bool); isGlobal {
			globalDelivery = op.Payload
		} else {
			bankDelivery = op.Payload
		}
	}
	if bankDelivery == nil || bankDelivery["webhook_id"] != "w1" {
		t.Fatalf("the bank's own webhook must still be delivered: %v", bankDelivery)
	}
	if globalDelivery == nil {
		t.Fatal("a subscribed global target must produce a delivery")
	}
	if globalDelivery["url"] != "https://global.test/receiver" || globalDelivery["secret"] != "g-secret" {
		t.Fatalf("global delivery = %v, want the global endpoint and secret", globalDelivery)
	}
}

// The global target only receives events that pass its filter.
func TestGlobalWebhookRespectsEventFilter(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	processor := archiveProcessor{store: store, webhookGuard: webhooks.NewGuard(webhooks.GuardConfig{
		Global: &webhooks.GlobalTarget{
			URL: "https://global.test/receiver", EventTypes: []string{"consolidation.completed"},
		},
	})}
	processor.enqueueWebhookEvent(ctx, "b1", "retain.completed", map[string]any{})

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range page.Operations {
		if isGlobal, _ := op.Payload["global"].(bool); isGlobal {
			t.Fatalf("an event outside the filter must not reach the global target: %v", op.Payload)
		}
	}
}

// A global delivery is signed with the global secret, using the same HMAC
// scheme a per-bank webhook uses, and reaches the global endpoint.
func TestGlobalWebhookDeliverySignature(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	const secret = "global-secret"
	var verified atomic.Bool
	var gotEvent atomic.Value
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		verified.Store(r.Header.Get("X-Hindsight-Signature") == want)
		gotEvent.Store(r.Header.Get("X-Hindsight-Event"))
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	body, err := marshalWebhookEvent("consolidation.completed", "b1", "", "completed", time.Now().UTC(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	op := storage.Operation{
		OperationID: "op-global", BankID: "b1",
		OperationType: storage.OperationTypeWebhookDelivery,
		Payload: map[string]any{
			"global": true, "event_type": "consolidation.completed",
			"url": receiver.URL, "secret": secret, "body": string(body),
		},
	}
	if err := (archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}).Process(ctx, op); err != nil {
		t.Fatalf("global delivery: %v", err)
	}
	if !verified.Load() {
		t.Fatal("the global delivery was not signed with the global secret")
	}
	if gotEvent.Load() != "consolidation.completed" {
		t.Fatalf("event header = %v, want the event type", gotEvent.Load())
	}
}

// With response-body exposure on, a delivery that answered records the
// receiver's body; off, the delivery records only the status.
func TestGlobalWebhookResponseBodyExposure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expose bool
	}{
		{"on", true},
		{"off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			ctx := context.Background()
			if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
				t.Fatal(err)
			}
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("receiver-body"))
			}))
			defer receiver.Close()

			body, err := marshalWebhookEvent("consolidation.completed", "b1", "", "completed", time.Now().UTC(), map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			op, err := store.PutOperation(ctx, storage.Operation{
				OperationID: "op-body", BankID: "b1",
				OperationType: storage.OperationTypeWebhookDelivery, Status: storage.StatusPending,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				Payload: map[string]any{
					"global": true, "event_type": "consolidation.completed",
					"url": receiver.URL, "secret": "s", "body": string(body),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			processor := archiveProcessor{store: store,
				webhookGuard: webhooks.NewGuard(webhooks.GuardConfig{
					AllowPrivateHosts: true, AllowInsecure: true,
					Global: &webhooks.GlobalTarget{URL: receiver.URL, ExposeResponseBody: tc.expose},
				})}
			runConsumerForOperation(t, store, processor, op.OperationID)

			page, err := store.ListWebhookDeliveries(ctx, "b1", "", 10, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Deliveries) != 1 {
				t.Fatalf("recorded %d deliveries, want 1", len(page.Deliveries))
			}
			delivery := page.Deliveries[0]
			if tc.expose {
				if delivery.LastResponseBody == nil || *delivery.LastResponseBody != "receiver-body" {
					t.Fatalf("last_response_body = %v, want the receiver's body", delivery.LastResponseBody)
				}
			} else if delivery.LastResponseBody != nil {
				t.Fatalf("last_response_body = %q, want it withheld", *delivery.LastResponseBody)
			}
			if delivery.LastResponseStatus == nil || *delivery.LastResponseStatus != http.StatusOK {
				t.Fatalf("last_response_status = %v, want the answered status", delivery.LastResponseStatus)
			}
		})
	}
}

// runConsumerForOperation drains the queued operation through a real consumer,
// so a reported outcome is persisted exactly as it is in production, and
// returns once the operation reaches a terminal state.
func runConsumerForOperation(t *testing.T, store storage.Store, processor worker.Processor, opID string) {
	t.Helper()
	consumer := worker.New(store, processor, worker.Config{Concurrency: 1, PollInterval: 2 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = consumer.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := store.GetOperation(ctx, "b1", opID)
		if err == nil && op.Status.Terminal() {
			cancel()
			<-done
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("operation %s did not settle", opID)
}

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
			// The store always keeps the bounded body: the reference persists it
			// on every attempt and decides visibility when the delivery is read.
			if delivery.LastResponseBody == nil || *delivery.LastResponseBody != "receiver-body" {
				t.Fatalf("last_response_body = %v, want the receiver's body", delivery.LastResponseBody)
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

// A registration's http_config is the delivery shape it asked for: the method
// selects a bodyless GET, and its headers and query parameters are applied. The
// Service-controlled headers still win, so a custom header cannot forge the
// event type.
func TestWebhookDeliveryHonoursHTTPConfig(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	type seen struct {
		method, query, custom, event, body string
	}
	var got atomic.Value
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Store(seen{
			method: r.Method, query: r.URL.RawQuery,
			custom: r.Header.Get("X-Custom"), event: r.Header.Get("X-Hindsight-Event"),
			body: string(body),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL, EventTypes: []string{"retain.completed"}, Enabled: true,
		HTTPConfig: map[string]any{
			"method": "GET", "timeout_seconds": 7,
			"headers": map[string]any{"X-Custom": "v"},
			"params":  map[string]any{"q": "2"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	op := webhookDeliveryOperation("w1")
	op.Payload["body"] = `{"event":"retain.completed"}`
	processor := archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}
	if err := processor.Process(ctx, op); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	captured, _ := got.Load().(seen)
	if captured.method != http.MethodGet {
		t.Fatalf("method = %q, want GET (http_config.method)", captured.method)
	}
	if captured.query != "q=2" {
		t.Fatalf("query = %q, want the configured params", captured.query)
	}
	if captured.custom != "v" {
		t.Fatalf("X-Custom = %q, want the configured header", captured.custom)
	}
	if captured.event != "retain.completed" {
		t.Fatalf("X-Hindsight-Event = %q, want the event type", captured.event)
	}
	if captured.body != "" {
		t.Fatalf("body = %q, want none on a GET", captured.body)
	}
}

// A receiver's rejection is retried on the reference's ladder rather than failed
// on the spot, and the recorded cause is the reference's own wording: the
// status, the endpoint and the receiver's explanation. The delivery only fails
// permanently once the attempts are spent.
func TestWebhookDeliveryRetriesReceiverRejection(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"no such hook"}`))
	}))
	defer receiver.Close()
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: receiver.URL, EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	processor := archiveProcessor{store: store, webhookGuard: deliveryTestGuard()}

	op := webhookDeliveryOperation("w1")
	op.Payload["body"] = `{"event":"retain.completed"}`
	err := processor.Process(ctx, op)
	if err == nil {
		t.Fatal("a 404 must not be delivered as a success")
	}
	if worker.IsPermanent(err) {
		t.Fatalf("a 404 must be retried, got a permanent failure: %v", err)
	}
	want := "HTTP 404 from " + receiver.URL + `: {"detail":"no such hook"}`
	if err.Error() != want {
		t.Fatalf("cause = %q, want %q", err.Error(), want)
	}

	// Once the ladder's rungs are spent the next failure is terminal.
	op.RetryCount = len(webhookRetryDelays)
	err = processor.Process(ctx, op)
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("after the attempts are spent the delivery must fail permanently, got %v", err)
	}
}

// The delivery backoff ladder is the reference's, rung for rung:
// RETRY_DELAYS = [5, 300, 1800, 7200, 18000] with
// MAX_ATTEMPTS = len(RETRY_DELAYS) + 1 (webhooks/manager.py:22-23). A retry
// after the first failure waits RETRY_DELAYS[retry_count], and the attempt that
// exhausts the ladder is terminal rather than waiting again.
func TestWebhookRetryLadderMatchesTheReference(t *testing.T) {
	want := []time.Duration{
		5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour,
	}
	if len(webhookRetryDelays) != len(want) {
		t.Fatalf("ladder has %d rungs, want %d", len(webhookRetryDelays), len(want))
	}
	for i, expected := range want {
		if webhookRetryDelays[i] != expected {
			t.Fatalf("rung %d = %v, want %v", i, webhookRetryDelays[i], expected)
		}
		if got := webhookRetryDelay(i); got != expected {
			t.Fatalf("webhookRetryDelay(%d) = %v, want %v", i, got, expected)
		}
	}
	// The reference makes len(RETRY_DELAYS)+1 attempts in total: the first try,
	// then one per rung. A retry_count equal to the rung count means those
	// attempts are spent.
	op := storage.Operation{OperationType: "webhook_delivery", RetryCount: len(webhookRetryDelays)}
	err := webhookRetryOrFail(op, errors.New("boom"))
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("a spent ladder must fail permanently, got %v", err)
	}
	// One rung short still schedules.
	op.RetryCount = len(webhookRetryDelays) - 1
	err = webhookRetryOrFail(op, errors.New("boom"))
	if err == nil || worker.IsPermanent(err) {
		t.Fatalf("the last rung must still schedule a retry, got %v", err)
	}
}

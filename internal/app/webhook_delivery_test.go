package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

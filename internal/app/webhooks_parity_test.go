package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// webhookApp composes an application whose guard permits a test target, so the
// route behavior can be exercised without reaching a real network.
func webhookApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig()
	// The guard would otherwise refuse the reserved test address, which is the
	// point of the guard; the route behavior is what this exercises.
	cfg.WebhookAllowPrivateHosts = true
	app, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "wh-bank", Name: "wh-bank"}); err != nil {
		t.Fatal(err)
	}
	return app
}

// test-webhook-lifecycle: a webhook can be created, read, listed, updated and
// deleted through the served handler, and its delivery list is addressable.
func TestWebhookLifecycle(t *testing.T) {
	app := webhookApp(t)
	base := "/v1/default/banks/wh-bank/webhooks"

	create := send(app, http.MethodPost, base,
		`{"url":"https://hooks.example.com/endpoint","secret":"s3cret","event_types":["retain.completed"]}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d (body %s)", create.Code, create.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create must return an id: %v", created)
	}
	// The secret must be present and null, never readable back, and there is no
	// has_secret field.
	if value, leaked := created["secret"]; !leaked || value != nil {
		t.Fatalf("the response must carry a null secret: %v", created)
	}
	if _, present := created["has_secret"]; present {
		t.Fatalf("the response must not carry has_secret: %v", created)
	}

	// The reference exposes no single-webhook read: only the list, the create,
	// the update and the delete. A GET on the member path is therefore not
	// routable, and the surface must not offer one.
	rec := send(app, http.MethodGet, base+"/"+id, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("member GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	// List.
	rec = send(app, http.MethodGet, base, "")
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("list returned %d webhooks, want 1", len(listed.Items))
	}

	// Update one field without restating the rest.
	rec = send(app, http.MethodPatch, base+"/"+id, `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d (body %s)", rec.Code, rec.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["enabled"] != false {
		t.Fatalf("update did not apply: %v", updated)
	}

	// The delivery list is addressable even with no deliveries yet.
	rec = send(app, http.MethodGet, base+"/"+id+"/deliveries", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deliveries = %d (body %s)", rec.Code, rec.Body.String())
	}

	// Delete, and a repeated delete is not-found.
	rec = send(app, http.MethodDelete, base+"/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec = send(app, http.MethodDelete, base+"/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("repeated delete = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// test-webhook-url-guard-blocks-internal-targets: the guard refuses a target
// inside the service's own network before anything is stored, even when the
// bank and the request are otherwise valid.
func TestWebhookGuardBlocksInternalTargets(t *testing.T) {
	base := "/v1/default/banks/wh-bank/webhooks"

	// A guard that permits private hosts is configured for the fixture, so the
	// loopback case is exercised through a fresh application with the default.
	strict, err := New(context.Background(), testConfig(),
		Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Store.CreateBank(context.Background(), storage.Bank{BankID: "wh-bank", Name: "wh-bank"}); err != nil {
		t.Fatal(err)
	}

	// The guard follows the reference: http and https are both acceptable
	// schemes, and what is refused is a private or link-local target (or a
	// scheme that is not a web request at all). A public host is therefore not
	// in this list — refusing it would be stricter than the reference.
	blocked := []string{
		`{"url":"https://127.0.0.1/hook"}`,
		`{"url":"https://169.254.169.254/latest/meta-data"}`,
		`{"url":"https://10.0.0.5/hook"}`,
		`{"url":"file:///etc/passwd"}`,
	}
	for _, body := range blocked {
		rec := send(strict, http.MethodPost, base, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s must be refused, got %d (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// test-webhook-accepts-unknown-event: the reference puts no enum on
// event_types, so an event name it does not know is stored verbatim rather than
// refused.
func TestWebhookAcceptsUnknownEvent(t *testing.T) {
	app := webhookApp(t)
	rec := send(app, http.MethodPost, "/v1/default/banks/wh-bank/webhooks",
		`{"url":"https://hooks.example.com/endpoint","event_types":["nonsense.event"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unknown event = %d, want %d (body %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// test-webhook-not-found: the family reports not-found for an unknown webhook
// and for an unknown bank.
func TestWebhookNotFound(t *testing.T) {
	app := webhookApp(t)
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"unknown webhook", http.MethodDelete, "/v1/default/banks/wh-bank/webhooks/11111111-1111-1111-1111-111111111111"},
		{"unknown webhook deliveries", http.MethodGet, "/v1/default/banks/wh-bank/webhooks/11111111-1111-1111-1111-111111111111/deliveries"},
		{"unknown bank", http.MethodGet, "/v1/default/banks/ghost/webhooks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(app, tc.method, tc.path, "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, http.StatusNotFound)
			}
		})
	}
}

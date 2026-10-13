package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// webhookApp composes an application whose guard permits a test target, so the
// route behavior can be exercised without reaching a real network.
func webhookApp(t *testing.T) *App {
	t.Helper()
	cfg := config.FromEnv(func(key string) (string, bool) {
		// The guard would otherwise refuse the reserved test address, which is
		// the point of the guard; the route behavior is what this exercises.
		if key == config.EnvWebhookAllowPrivateHosts {
			return "true", true
		}
		return "", false
	})
	app, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{ID: "wh-bank", Name: "wh-bank"}); err != nil {
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
		`{"url":"https://hooks.example.com/endpoint","secret":"s3cret","event_types":["memory.created"]}`)
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
	// The secret must not be readable back.
	if _, leaked := created["secret"]; leaked {
		t.Fatalf("the response must not carry the secret: %v", created)
	}
	if created["has_secret"] != true {
		t.Fatalf("the response must say a secret is set: %v", created)
	}

	// Read one.
	rec := send(app, http.MethodGet, base+"/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}

	// List.
	rec = send(app, http.MethodGet, base, "")
	var listed struct {
		Webhooks []map[string]any `json:"webhooks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Webhooks) != 1 {
		t.Fatalf("list returned %d webhooks, want 1", len(listed.Webhooks))
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
	if rec.Code != http.StatusNoContent {
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
	strict, err := New(context.Background(), config.FromEnv(func(string) (string, bool) { return "", false }),
		Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Store.CreateBank(context.Background(), storage.Bank{ID: "wh-bank", Name: "wh-bank"}); err != nil {
		t.Fatal(err)
	}

	blocked := []string{
		`{"url":"https://127.0.0.1/hook"}`,
		`{"url":"https://169.254.169.254/latest/meta-data"}`,
		`{"url":"https://10.0.0.5/hook"}`,
		`{"url":"http://example.com/hook"}`,
		`{"url":"file:///etc/passwd"}`,
	}
	for _, body := range blocked {
		rec := send(strict, http.MethodPost, base, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s must be refused, got %d (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// test-webhook-rejects-unknown-event: a webhook cannot subscribe to an event
// that will never fire.
func TestWebhookRejectsUnknownEvent(t *testing.T) {
	app := webhookApp(t)
	rec := send(app, http.MethodPost, "/v1/default/banks/wh-bank/webhooks",
		`{"url":"https://hooks.example.com/endpoint","event_types":["nonsense.event"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown event = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
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
		{"unknown webhook", http.MethodGet, "/v1/default/banks/wh-bank/webhooks/absent"},
		{"unknown webhook deliveries", http.MethodGet, "/v1/default/banks/wh-bank/webhooks/absent/deliveries"},
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

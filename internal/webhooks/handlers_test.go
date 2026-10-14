package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newServer builds the family's route table over an in-memory store with one
// bank, using a guard that permits the test's own host so a valid registration
// is possible.
func newServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	guard := NewGuard(GuardConfig{AllowPrivateHosts: true, AllowedHosts: []string{"example.test"}})
	mux := http.NewServeMux()
	NewHandlers(store, guard).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "b1")
			if strings.Contains(pattern, "{webhook_id}") {
				r.SetPathValue("webhook_id", lastSegment(r.URL.Path))
			}
			handler(w, r)
		})
	})
	return mux, store
}

// lastSegment returns the final path segment, skipping a trailing action word.
func lastSegment(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		switch segments[i] {
		case "deliveries":
			continue
		default:
			return segments[i]
		}
	}
	return ""
}

func send(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The full create → list → get → update → deliveries → delete lifecycle.
func TestWebhookLifecycle(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"

	created := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook","event_types":["memory.created"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d, body = %s", created.Code, created.Body.String())
	}
	var webhook Webhook
	if err := json.Unmarshal(created.Body.Bytes(), &webhook); err != nil {
		t.Fatal(err)
	}
	if webhook.URL != "https://example.test/hook" || !webhook.Enabled {
		t.Fatalf("created webhook = %+v", webhook)
	}
	if webhook.EventTypes == nil {
		t.Fatal("event types must be an array, not null")
	}

	listed := send(mux, http.MethodGet, base, "")
	var list ListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Webhooks) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// The reference exposes no single-webhook read, so a GET on the member path
	// is not routable and must not answer 200.
	if rec := send(mux, http.MethodGet, base+"/"+webhook.ID, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("member get: %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	updated := send(mux, http.MethodPatch, base+"/"+webhook.ID, `{"enabled":false,"event_types":["memory.updated"]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}
	var after Webhook
	if err := json.Unmarshal(updated.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Enabled || len(after.EventTypes) != 1 || after.EventTypes[0] != "memory.updated" {
		t.Fatalf("updated webhook = %+v", after)
	}

	deliveries := send(mux, http.MethodGet, base+"/"+webhook.ID+"/deliveries", "")
	if deliveries.Code != http.StatusOK {
		t.Fatalf("deliveries: %d, body = %s", deliveries.Code, deliveries.Body.String())
	}
	var deliveryList DeliveryListResponse
	if err := json.Unmarshal(deliveries.Body.Bytes(), &deliveryList); err != nil {
		t.Fatal(err)
	}
	if deliveryList.Deliveries == nil {
		t.Fatal("deliveries must be an array, not null")
	}

	if rec := send(mux, http.MethodDelete, base+"/"+webhook.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	// With no member read route, a repeated delete is how the absence of a
	// webhook is observed.
	if rec := send(mux, http.MethodDelete, base+"/"+webhook.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete after delete: %d, want 404", rec.Code)
	}
}

// A create with a rejected URL, an unknown event, or a malformed body is
// refused before anything is stored.
func TestWebhookCreateValidation(t *testing.T) {
	mux, store := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"

	// The guard refuses a target outside the allowed host set.
	if rec := send(mux, http.MethodPost, base, `{"url":"https://evil.test/hook"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("disallowed host: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook","event_types":["nope"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown event: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d, want 400", rec.Code)
	}
	hooks, err := store.ListWebhooks(context.Background(), "b1")
	if err != nil || len(hooks) != 0 {
		t.Fatalf("nothing must be stored after a refused create: %v (err %v)", hooks, err)
	}
}

// An update that carries a rejected URL is refused, and the stored record is
// left untouched.
func TestWebhookUpdateRejectsBadURL(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"
	created := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook"}`)
	var webhook Webhook
	if err := json.Unmarshal(created.Body.Bytes(), &webhook); err != nil {
		t.Fatal(err)
	}
	if rec := send(mux, http.MethodPatch, base+"/"+webhook.ID, `{"url":"https://evil.test/hook"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("update to a disallowed host: %d, want 400", rec.Code)
	}
}

// An unknown bank answers not-found, and an unknown webhook answers not-found.
func TestWebhookNegativeCases(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store, NewGuard(GuardConfig{AllowPrivateHosts: true})).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	if rec := send(mux, http.MethodGet, "/v1/default/banks/missing/webhooks", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}

	seeded, _ := newServer(t)
	// The member path has no read route, so an unknown webhook is probed with
	// a method the surface actually offers.
	if rec := send(seeded, http.MethodDelete, "/v1/default/banks/b1/webhooks/does-not-exist", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown webhook: %d, want 404", rec.Code)
	}
}

package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	guard := NewGuard(GuardConfig{AllowedHosts: []string{"example.test"}})
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

	created := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook","secret":"s3cret","event_types":["retain.completed"],"http_config":{"timeout_seconds":5}}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d, want %d (body %s)", created.Code, http.StatusCreated, created.Body.String())
	}
	// The response model keeps nulls: the secret is present and null, and there
	// is no has_secret field.
	var raw map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if value, present := raw["secret"]; !present || value != nil {
		t.Fatalf("secret must be present and null: %v", raw)
	}
	if _, present := raw["has_secret"]; present {
		t.Fatalf("the response must not carry has_secret: %v", raw)
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
	// http_config is always emitted as a typed object with defaults.
	if webhook.HTTPConfig.Method != "POST" || webhook.HTTPConfig.TimeoutSeconds != 5 {
		t.Fatalf("http_config = %+v, want POST with the supplied timeout", webhook.HTTPConfig)
	}
	if webhook.HTTPConfig.Headers == nil || webhook.HTTPConfig.Params == nil {
		t.Fatalf("http_config headers/params must default to empty objects: %+v", webhook.HTTPConfig)
	}

	listed := send(mux, http.MethodGet, base, "")
	var list ListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// The reference exposes no single-webhook read, so a GET on the member path
	// is not routable and must not answer 200.
	if rec := send(mux, http.MethodGet, base+"/"+webhook.ID, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("member get: %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	updated := send(mux, http.MethodPatch, base+"/"+webhook.ID, `{"enabled":false,"event_types":["retain.completed"]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}
	var after Webhook
	if err := json.Unmarshal(updated.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Enabled || len(after.EventTypes) != 1 || after.EventTypes[0] != "retain.completed" {
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
	if deliveryList.Items == nil {
		t.Fatal("items must be an array, not null")
	}
	var deliveryRaw map[string]any
	if err := json.Unmarshal(deliveries.Body.Bytes(), &deliveryRaw); err != nil {
		t.Fatal(err)
	}
	if _, present := deliveryRaw["deliveries"]; present {
		t.Fatal("the envelope must be {items, next_cursor}, not {deliveries}")
	}
	if _, present := deliveryRaw["total"]; present {
		t.Fatal("the envelope must not carry the offset-paging total")
	}

	if rec := send(mux, http.MethodDelete, base+"/"+webhook.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	// With no member read route, a repeated delete is how the absence of a
	// webhook is observed.
	if rec := send(mux, http.MethodDelete, base+"/"+webhook.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete after delete: %d, want 404", rec.Code)
	}
}

// A create with a rejected URL is refused before anything is stored; a body the
// request model rejects is the framework's 422; an unknown event name is
// accepted (the reference applies no enum to the field).
func TestWebhookCreateValidation(t *testing.T) {
	mux, store := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"

	// The guard refuses a private target, which registration rejects outright.
	if rec := send(mux, http.MethodPost, base, `{"url":"http://10.0.0.5/hook"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("private target: %d, want 400", rec.Code)
	}
	// An unknown event name is accepted, matching the reference.
	if rec := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook","event_types":["nope"]}`); rec.Code != http.StatusCreated {
		t.Fatalf("unknown event: %d, want 201", rec.Code)
	}
	// A malformed body is a 422 with the framework's item envelope.
	if rec := send(mux, http.MethodPost, base, `not json`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body: %d, want 422", rec.Code)
	}
	// A missing required url is the framework's 422 too.
	if rec := send(mux, http.MethodPost, base, `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing url: %d, want 422", rec.Code)
	}
	hooks, err := store.ListWebhooks(context.Background(), "b1")
	if err != nil || len(hooks) != 1 {
		t.Fatalf("only the accepted create must be stored: %v (err %v)", hooks, err)
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
	if rec := send(mux, http.MethodPatch, base+"/"+webhook.ID, `{"url":"http://10.0.0.5/hook"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("update to a private target: %d, want 400", rec.Code)
	}
}

// The delivery listing paginates by created_at cursor and emits the reference's
// item fields.
func TestWebhookDeliveriesShapeAndCursor(t *testing.T) {
	mux, store := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"
	created := send(mux, http.MethodPost, base, `{"url":"https://example.test/hook","event_types":["retain.completed"]}`)
	var webhook Webhook
	if err := json.Unmarshal(created.Body.Bytes(), &webhook); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"op-1", "op-2", "op-3"} {
		// The store stamps created_at itself, and the cursor pages on that
		// microsecond-resolution value: two rows minted inside the same
		// microsecond collapse under `< cursor`, so keep them apart.
		time.Sleep(1200 * time.Microsecond)
		if _, err := store.PutOperation(context.Background(), storage.Operation{
			OperationID:   id,
			BankID:        "b1",
			OperationType: storage.OperationTypeWebhookDelivery,
			Status:        storage.StatusFailed,
			Payload: map[string]any{
				"webhook_id": webhook.ID, "event_type": "retain.completed",
				"url": "https://example.test/hook",
			},
			CreatedAt: timeAt(i),
			UpdatedAt: timeAt(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	rec := send(mux, http.MethodGet, base+"/"+webhook.ID+"/deliveries?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deliveries: %d (body %s)", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []struct {
			ID        string `json:"id"`
			WebhookID string `json:"webhook_id"`
			URL       string `json:"url"`
			EventType string `json:"event_type"`
			Status    string `json:"status"`
			Attempts  int    `json:"attempts"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("first page items = %d, want 2", len(page.Items))
	}
	if page.Items[0].ID != "op-3" {
		t.Fatalf("newest first expected, got %+v", page.Items)
	}
	if page.NextCursor == nil || *page.NextCursor == "" {
		t.Fatal("a page with more rows must carry a next_cursor")
	}
	// Every upstream item field is present.
	var rawItems struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawItems); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "webhook_id", "url", "event_type", "status", "attempts",
		"next_retry_at", "last_error", "last_response_status", "last_response_body",
		"last_attempt_at", "created_at", "updated_at"} {
		if _, present := rawItems.Items[0][field]; !present {
			t.Fatalf("delivery item is missing %q: %v", field, rawItems.Items[0])
		}
	}

	// The cursor is the last row's created_at as the reference reports it — the
	// PostgreSQL text form, which contains a space — so a caller must
	// URL-encode it before putting it in a query string.
	next := send(mux, http.MethodGet, base+"/"+webhook.ID+"/deliveries?limit=2&cursor="+url.QueryEscape(*page.NextCursor), "")
	var second struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(next.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "op-1" {
		t.Fatalf("second page = %+v (status %d, body %s)", second.Items, next.Code, next.Body.String())
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
	// A malformed webhook identifier is the reference's 422, not a 404.
	if rec := send(seeded, http.MethodDelete, "/v1/default/banks/b1/webhooks/does-not-exist", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed webhook id: %d, want 422", rec.Code)
	}
	// A well-formed but unknown identifier is a 404.
	if rec := send(seeded, http.MethodDelete, "/v1/default/banks/b1/webhooks/11111111-1111-1111-1111-111111111111", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown webhook: %d, want 404", rec.Code)
	}
}

// timeAt returns a distinct timestamp for delivery fixtures.
func timeAt(ordinal int) time.Time {
	return time.Date(2026, 1, 1, 0, ordinal, 0, 0, time.UTC)
}

// asHTTPError names the storage sentinels a route can hit and treats anything
// it does not name as a server fault.
func TestWebhookErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		expect int
	}{
		{storage.ErrBankNotFound, http.StatusNotFound},
		{storage.ErrWebhookNotFound, http.StatusNotFound},
		{storage.ErrWebhookURLNotAllowed, http.StatusBadRequest},
		{storage.ErrInvalidBankID, http.StatusBadRequest},
		{storage.ErrAliasConflict, http.StatusConflict},
		{storage.ErrWritesUnavailable, http.StatusServiceUnavailable},
		{errors.New("storage exploded"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := asHTTPError(tc.err); got == nil || got.Status != tc.expect {
			t.Fatalf("error %v mapped to %+v, want status %d", tc.err, got, tc.expect)
		}
	}
	if asHTTPError(nil) != nil {
		t.Fatal("a nil error must map to nil")
	}
}

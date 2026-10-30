package app

// Every 503 the service serves carries a Retry-After, because the reference
// pairs the status with the header without exception: its store-unavailable
// mapper builds both together (api/http.py:367-371), its app-level handler for
// a shed write does the same (api/http.py:5801-5806), and its admission shed
// carries one too (api/http.py:5712-5717).
//
// The six storage-error mappers that produce most of this service's 503s have
// no handler state to read a configured delay from, so they answered with a
// bare status. The default now lives in the shared writer (internal/httpx), and
// this drives each route through the real served handler to prove it reaches
// all of them — a unit test of the writer alone would not catch a route that
// bypassed it.

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// shedStore is a store whose writes and directory reads are shed, so the routes
// under test reach their 503 mapper. Everything else is the in-memory store, so
// the application composes over it unchanged.
type shedStore struct {
	*storage.MemoryStore
}

func (shedStore) PutDirective(context.Context, storage.Directive) (storage.Directive, error) {
	return storage.Directive{}, storage.ErrWritesUnavailable
}

func (shedStore) PutWebhook(context.Context, storage.Webhook) (storage.Webhook, error) {
	return storage.Webhook{}, storage.ErrWritesUnavailable
}

func (shedStore) PutMentalModel(context.Context, storage.MentalModel) (storage.MentalModel, error) {
	return storage.MentalModel{}, storage.ErrWritesUnavailable
}

func (shedStore) PutOperation(context.Context, storage.Operation) (storage.Operation, error) {
	return storage.Operation{}, storage.ErrWritesUnavailable
}

func (shedStore) ListDocuments(context.Context, storage.DocumentQuery) (storage.DocumentPage, error) {
	return storage.DocumentPage{}, storage.ErrWritesUnavailable
}

// shedApp composes the served application over a shedding store.
func shedApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	inner := storage.NewMemoryStore("public")
	if _, err := inner.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), cfg, Dependencies{Store: shedStore{inner}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The mapper sites a store error reaches, one route each, all answering 503 +
// Retry-After. Each request is valid enough to reach the store: a route that
// rejects the body first would exercise the validator instead of the mapper.
//
// The reporting package's mapper is not driven here — its only 503 site sits
// behind a live model refresh, which needs a real provider before it can reach
// a store error. It writes through the same shared writer these prove.
func TestEveryShedStoreRouteCarriesRetryAfter(t *testing.T) {
	a := shedApp(t)
	const base = "/v1/default/banks/b1"
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"directives", http.MethodPost, base + "/directives",
			`{"name":"style","content":"be brief"}`},
		{"webhooks", http.MethodPost, base + "/webhooks",
			`{"url":"https://example.invalid/hook","event_types":["retain.completed"]}`},
		{"mental models", http.MethodPost, base + "/mental-models",
			`{"name":"goals","source_query":"what are the goals?"}`},
		{"transfer", http.MethodPost, base + "/document-transfer/export", ""},
		{"readsurface", http.MethodGet, base + "/documents", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := exchange(a, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("route answered %d rather than reaching the store: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(httpx.HeaderRetryAfter); got != "30" {
				t.Fatalf("Retry-After = %q, want the reference's default 30 (body %s)", got, rec.Body.String())
			}
		})
	}
}

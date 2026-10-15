package memoryops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// recallCaptureStore records the recall query the handler resolved, so a test
// can assert the configuration it was built from without a live backend.
type recallCaptureStore struct {
	*storage.MemoryStore
	last storage.RecallQuery
}

func (s *recallCaptureStore) Recall(ctx context.Context, q storage.RecallQuery) (storage.RecallResult, error) {
	s.last = q
	return s.MemoryStore.Recall(ctx, q)
}

func newMuxFor(store storage.Store) *http.ServeMux {
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}

func postWithOverrides(t *testing.T, mux *http.ServeMux, path, body string, overrides map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if overrides != nil {
		req = httpx.WithTenantOverrides(req, overrides)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A key the tenant alone defines reaches the recall path: the tenant's
// enable_text_search toggle disables the keyword arm even though the bank names
// nothing.
func TestTenantOverrideReachesRecall(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	capture := &recallCaptureStore{MemoryStore: store}
	mux := newMuxFor(capture)

	rec := postWithOverrides(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"alpha"}`, map[string]any{"enable_text_search": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !containsString(capture.last.DisabledArms, storage.ArmKeyword) {
		t.Fatalf("disabled arms = %v, want the tenant's keyword arm disabled", capture.last.DisabledArms)
	}
}

// A bank override still wins over the tenant's: the bank names a chunk size the
// tenant contradicts, and the bank's value is the one that reaches segmentation.
func TestBankOverrideWinsOverTenant(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	bankConfig := map[string]any{"retain_chunk_size": float64(5)}
	if _, err := store.UpdateBank(context.TODO(), "b1", storage.BankPatch{Config: &bankConfig}); err != nil {
		t.Fatal(err)
	}
	mux := newMuxFor(store)

	rec := postWithOverrides(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"alpha bravo charlie delta"}]}`,
		map[string]any{"retain_chunk_size": float64(1000)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListChunks(context.TODO(), storage.ChunkQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total <= 1 {
		t.Fatalf("chunks = %d, want the bank's small chunk size to win over the tenant's", page.Total)
	}
}

// A key only the tenant defines reaches the retain path: with no bank config the
// tenant's small chunk size is what splits the write.
func TestTenantOverrideReachesRetain(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := newMuxFor(store)

	rec := postWithOverrides(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"alpha bravo charlie delta"}]}`,
		map[string]any{"retain_chunk_size": float64(5)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListChunks(context.TODO(), storage.ChunkQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total <= 1 {
		t.Fatalf("chunks = %d, want the tenant's small chunk size to split the text", page.Total)
	}
}

// No tenant layer leaves an existing route's behaviour unchanged: a recall with
// no overrides resolves exactly as a recall with an explicitly empty override
// map.
func TestNoTenantLayerLeavesRecallUnchanged(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha", Type: "world"})
	mux := newMuxFor(store)

	without := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha"}`)
	empty := postWithOverrides(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha"}`, map[string]any{})
	if without.Code != empty.Code || without.Body.String() != empty.Body.String() {
		t.Fatalf("empty tenant layer changed the response: %s vs %s", without.Body.String(), empty.Body.String())
	}
}

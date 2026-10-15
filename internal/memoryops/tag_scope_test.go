package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

func postWithScope(t *testing.T, mux *http.ServeMux, path, body string, scope []httpx.TagGroup) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if scope != nil {
		req = httpx.WithTagScope(req, scope)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func recallIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(response.Results))
	for _, item := range response.Results {
		ids = append(ids, item.ID)
	}
	return ids
}

// A resolved scope narrows a recall that asks for a wider tag set: the caller
// can select less than its scope, never more.
func TestTagScopeNarrowsRecall(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha", Type: "world", Tags: []string{"a"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "m2", Text: "alpha", Type: "world", Tags: []string{"a", "b"}})

	// Without the scope the wider request matches both members.
	unscoped := recallIDs(t, post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","tags":["a","b"]}`))
	if len(unscoped) != 2 {
		t.Fatalf("unscoped matches = %v, want both", unscoped)
	}

	scope := []httpx.TagGroup{{Tags: []string{"b"}}}
	scoped := recallIDs(t, postWithScope(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","tags":["a","b"]}`, scope))
	if len(scoped) != 1 || scoped[0] != "m2" {
		t.Fatalf("scoped matches = %v, want only m2", scoped)
	}
}

// A recall that asks for no tags still sees only the scope, so an unscoped
// caller never reads outside its confinement.
func TestTagScopeConfinesUntaggedRecall(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha", Type: "world", Tags: []string{"a"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "m2", Text: "alpha", Type: "world", Tags: []string{"a", "b"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "untagged", Text: "alpha", Type: "world"})

	all := recallIDs(t, post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha"}`))
	if len(all) != 3 {
		t.Fatalf("unscoped recall = %v, want all three", all)
	}

	scope := []httpx.TagGroup{{Tags: []string{"a"}}}
	scoped := recallIDs(t, postWithScope(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha"}`, scope))
	if len(scoped) != 2 {
		t.Fatalf("scoped recall = %v, want the two tagged memories", scoped)
	}
	for _, id := range scoped {
		if id == "untagged" {
			t.Fatalf("scope admitted an untagged memory: %v", scoped)
		}
	}
}

// An empty scope changes nothing.
func TestEmptyTagScopeChangesNothing(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha", Type: "world", Tags: []string{"a"}})

	plain := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha"}`)
	withEmpty := postWithScope(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha"}`, []httpx.TagGroup{})
	if plain.Body.String() != withEmpty.Body.String() {
		t.Fatalf("an empty scope changed the response: %s vs %s", plain.Body.String(), withEmpty.Body.String())
	}
}

// countingStore records the write and read calls a handler makes.
type countingStore struct {
	storage.Store
	retains int
	recalls int
}

func (s *countingStore) RetainWrite(ctx context.Context, req storage.RetainRequest) (storage.RetainOutcome, error) {
	s.retains++
	return s.Store.RetainWrite(ctx, req)
}

func (s *countingStore) Recall(ctx context.Context, q storage.RecallQuery) (storage.RecallResult, error) {
	s.recalls++
	return s.Store.Recall(ctx, q)
}

func countingServer(t *testing.T) (*http.ServeMux, *countingStore) {
	t.Helper()
	inner := storage.NewMemoryStore("public")
	if _, err := inner.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	spy := &countingStore{Store: inner}
	h := NewHandlers(spy, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux, spy
}

// A registered validator denying a retain answers the validator's status and
// stores nothing.
func TestDeniedRetainStoresNothing(t *testing.T) {
	mux, spy := countingServer(t)
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(&denyValidator{operation: httpx.OperationRetain, status: http.StatusForbidden, reason: "frozen"})
	handler := httpx.Chain(mux, httpx.NullDropMiddleware(false), httpx.OperationValidationMiddleware(host))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories",
		strings.NewReader(`{"items":[{"content":"hello"}]}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if spy.retains != 0 {
		t.Fatalf("denied retain reached the store %d times", spy.retains)
	}
}

// A registered validator denying a recall answers the validator's status and
// reads nothing.
func TestDeniedRecallReadsNothing(t *testing.T) {
	mux, spy := countingServer(t)
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(&denyValidator{operation: httpx.OperationRecall, status: http.StatusForbidden, reason: "no reads"})
	handler := httpx.Chain(mux, httpx.NullDropMiddleware(false), httpx.OperationValidationMiddleware(host))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall",
		strings.NewReader(`{"query":"q"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if spy.recalls != 0 {
		t.Fatalf("denied recall reached the store %d times", spy.recalls)
	}
}

// denyValidator denies exactly one operation and allows the rest.
type denyValidator struct {
	operation string
	status    int
	reason    string
}

func (v *denyValidator) ValidateOperation(_ context.Context, op httpx.OperationRequest) httpx.OperationDecision {
	if op.Operation == v.operation {
		return httpx.OperationDecision{Allowed: false, Reason: v.reason, Status: v.status}
	}
	return httpx.OperationDecision{Allowed: true}
}

func (v *denyValidator) ResolveTagScope(_ context.Context, _ string) []httpx.TagGroup { return nil }

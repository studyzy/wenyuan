package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// countingScope counts how often the team plane is asked, which is how a
// personal read is proved not to resolve a team scope. The scope it answers
// with is what a member's bank resolves to, so the contrast read proves the
// resolver under this harness is genuinely reachable.
type countingScope struct {
	fallback *storage.FallbackScope
	calls    int
}

func (c *countingScope) Resolve(ctx context.Context, callerTenantID, bankID string) (*storage.FallbackScope, error) {
	c.calls++
	return c.fallback, nil
}

// withSchemaHandler puts a tenant schema on the request context, which is what
// the serving stack's tenant middleware does. Without it teamFallback returns at
// its SchemaFrom guard and never consults the resolver at all.
type withSchemaHandler struct {
	schema string
	next   http.Handler
}

func (h withSchemaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, err := storage.WithSchema(r.Context(), h.schema)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.next.ServeHTTP(w, r.WithContext(ctx))
}

// newScopedServer is countingServer with a team resolver wired in. The mux is
// served behind a schema-carrying handler.
func newScopedServer(t *testing.T, scope TeamScopeResolver) (http.Handler, *countingStore) {
	t.Helper()
	inner := storage.NewMemoryStore("public")
	if _, err := inner.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	store := &countingStore{Store: inner}
	h := NewHandlers(store, 5, 30000)
	h.TeamScope = scope
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return withSchemaHandler{schema: "acme", next: mux}, store
}

// A scope this service does not serve yet is refused before retrieval: the
// refusal must not cost a recall.
func TestRecallRefusesBoth(t *testing.T) {
	mux, store := newScopedServer(t, &countingScope{})
	rec := postTo(t, mux, "/v1/default/banks/b1/memories/recall?scope=both", `{"query":"x"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.recalls != 0 {
		t.Fatalf("store received %d recall(s), want 0", store.recalls)
	}
	var got struct {
		Detail []struct {
			Loc []any `json:"loc"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Detail) == 0 {
		t.Fatalf("no validation item: %s", rec.Body.String())
	}
	loc := got.Detail[0].Loc
	if len(loc) != 2 || loc[0] != "query" || loc[1] != "scope" {
		t.Fatalf("loc = %v, want [query scope]", loc)
	}
}

// A request that states no scope asks the team plane, so a resolver that would
// redirect the read is consulted; a personal read is answered by the caller's
// own bank without asking the team plane at all.
func TestRecallScopePersonalSkipsTheTeamScope(t *testing.T) {
	scope := &countingScope{fallback: &storage.FallbackScope{Schema: "acme", BankID: "acme::b1"}}
	mux, _ := newScopedServer(t, scope)

	noScope := postTo(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"x"}`)
	if noScope.Code != http.StatusOK {
		t.Fatalf("no-scope status = %d, body = %s", noScope.Code, noScope.Body.String())
	}
	if scope.calls == 0 {
		t.Fatalf("resolver consulted no time on a read with no scope, want it consulted")
	}

	scope.calls = 0
	personal := postTo(t, mux, "/v1/default/banks/b1/memories/recall?scope=personal", `{"query":"x"}`)
	if personal.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", personal.Code, personal.Body.String())
	}
	if scope.calls != 0 {
		t.Fatalf("resolver consulted %d time(s) on a personal read, want 0", scope.calls)
	}
}

// postTo is post against an arbitrary handler, for a mux wrapped in middleware.
func postTo(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

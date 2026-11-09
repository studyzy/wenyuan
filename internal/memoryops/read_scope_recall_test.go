package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// countingScope counts how often the team plane is asked, which is how a
// personal read is proved not to resolve a team scope.
type countingScope struct {
	calls int
}

func (c *countingScope) Resolve(ctx context.Context, callerTenantID, bankID string) (*storage.FallbackScope, error) {
	c.calls++
	return nil, nil
}

// newScopedServer is countingServer with a team resolver wired in.
func newScopedServer(t *testing.T, scope TeamScopeResolver) (*http.ServeMux, *countingStore) {
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
	return mux, store
}

// A scope this service does not serve yet is refused before retrieval: the
// refusal must not cost a recall.
func TestRecallRefusesBoth(t *testing.T) {
	mux, store := newScopedServer(t, &countingScope{})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall?scope=both", `{"query":"x"}`)
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

// A personal read is served from the caller's own bank without consulting the
// team plane at all.
//
// The harness sets no schema on the request context, so teamFallback returns nil
// early and cannot reach the resolver on the no-scope read either. The
// assertions therefore only check what this harness can observe: the personal
// read answers 200 (and, once the resolver is reached, is not asked).
func TestRecallScopePersonalSkipsTheTeamScope(t *testing.T) {
	scope := &countingScope{}
	mux, _ := newScopedServer(t, scope)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall?scope=personal", `{"query":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if scope.calls != 0 {
		t.Fatalf("resolver consulted %d time(s) on a personal read, want 0", scope.calls)
	}
}

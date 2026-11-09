package reporting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TestListMemoriesScopeRequests pins the listing's three accepted states and
// its two refusals.
func TestListMemoriesScopeRequests(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	for _, query := range []string{"", "?scope=team", "?scope=personal", "?scope="} {
		rec := send(mux, http.MethodGet, base+"/memories/list"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list%s = %d, want 200: %s", query, rec.Code, rec.Body.String())
		}
	}
	for _, query := range []string{"?scope=both", "?scope=bogus", "?scope=PERSONAL"} {
		rec := send(mux, http.MethodGet, base+"/memories/list"+query, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("list%s = %d, want 422: %s", query, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"query"`) || !strings.Contains(body, `"scope"`) {
			t.Fatalf("list%s error body does not locate query.scope: %s", query, body)
		}
	}
	if rec := send(mux, http.MethodGet, base+"/memories/list?scope=both", ""); !strings.Contains(rec.Body.String(), "not supported yet") {
		t.Fatalf("list?scope=both body = %s, want the not-supported message", rec.Body.String())
	}
}

// listCountingScope counts how often the team plane is asked, which is how a
// personal listing is proved not to resolve a team scope.
type listCountingScope struct {
	fallback *storage.FallbackScope
	calls    int
}

func (c *listCountingScope) Resolve(ctx context.Context, callerTenantID, bankID string) (*storage.FallbackScope, error) {
	c.calls++
	return c.fallback, nil
}

// listWithSchemaHandler puts a tenant schema on the request context, which is
// what the serving stack's tenant middleware does. Without it teamFallback
// returns at its SchemaFrom guard and never consults the resolver at all.
type listWithSchemaHandler struct {
	schema string
	next   http.Handler
}

func (h listWithSchemaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, err := storage.WithSchema(r.Context(), h.schema)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.next.ServeHTTP(w, r.WithContext(ctx))
}

// newListScopeServer is newServer with a team resolver wired in, served behind
// a schema-carrying handler so the fallback is genuinely reachable.
func newListScopeServer(t *testing.T, scope TeamScopeResolver) *listWithSchemaHandler {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	handlers := NewHandlers(store)
	handlers.TeamScope = scope
	mux := http.NewServeMux()
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			handler(w, r)
		})
	})
	return &listWithSchemaHandler{schema: "acme", next: mux}
}

// A listing that states no scope asks the team plane, so a resolver that would
// redirect the read is consulted; a personal listing is answered by the
// caller's own bank without asking the team plane at all.
func TestListMemoriesScopePersonalSkipsTheTeamScope(t *testing.T) {
	scope := &listCountingScope{fallback: &storage.FallbackScope{Schema: "acme", BankID: "acme::" + bank}}
	server := newListScopeServer(t, scope)
	path := "/v1/default/banks/" + bank + "/memories/list"

	noScope := sendHandler(t, server, http.MethodGet, path)
	if noScope.Code != http.StatusOK {
		t.Fatalf("no-scope status = %d, body = %s", noScope.Code, noScope.Body.String())
	}
	if scope.calls == 0 {
		t.Fatalf("resolver consulted no time on a listing with no scope, want it consulted")
	}

	scope.calls = 0
	personal := sendHandler(t, server, http.MethodGet, path+"?scope=personal")
	if personal.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", personal.Code, personal.Body.String())
	}
	if scope.calls != 0 {
		t.Fatalf("resolver consulted %d time(s) on a personal listing, want 0", scope.calls)
	}
}

// sendHandler issues a GET through an arbitrary handler, for a mux wrapped in
// middleware.
func sendHandler(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

package readsurface

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// entityOrderServer builds the graph/entity routes over a store holding no
// bank, so every request addresses a bank that does not exist. familyRoutes
// pins bank_id to the fixture's bank, which would hide the ordering these tests
// are about, so the bank comes from the request path instead.
func entityOrderServer(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	seedFixture(t, store)
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			rest := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
			if bank, _, ok := strings.Cut(rest, "/"); ok {
				r.SetPathValue("bank_id", bank)
			}
			if strings.Contains(pattern, "{entity_id}") {
				r.SetPathValue("entity_id", lastSegment(r.URL.Path))
			}
			handler(w, r)
		})
	})
	return mux
}

// The graph route's `limit` is a `Query(...)` parameter in the reference, so it
// is validated before the bank is looked up.
func TestGraphValidatesLimitBeforeTheBank(t *testing.T) {
	mux := entityOrderServer(t)
	for path, want := range map[string]string{
		"/v1/default/banks/absent/graph?limit=-1":  "greater_than_equal",
		"/v1/default/banks/absent/graph?limit=abc": "int_parsing",
	} {
		rec := send(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s body %q lacks %s", path, rec.Body.String(), want)
		}
	}
	// With a valid limit the unknown bank is still the 404.
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/graph", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("plain graph = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// The graph route declares no `tag_groups`, and the framework ignores a query
// parameter no route declares, so a malformed one must not become a 400.
func TestGraphIgnoresUndeclaredTagGroups(t *testing.T) {
	mux := entityOrderServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/graph?tag_groups=bogus", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("graph?tag_groups=bogus = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The entity routes' `Query(...)` parameters are validated before the bank, and
// all of a request's failures are reported in one envelope, in declaration
// order: limit, offset, tags_match.
func TestListEntitiesValidatesBeforeTheBank(t *testing.T) {
	mux := entityOrderServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/entities?limit=-1", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=-1 = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/entities?offset=-1", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("offset=-1 = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// Both bad: one envelope, limit first.
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/entities?limit=-1&tags_match=bogus", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("combined = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	items := detailItems(t, rec.Body.Bytes())
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %s", len(items), rec.Body.String())
	}
	first := items[0]["loc"].([]any)
	if first[len(first)-1] != "limit" {
		t.Fatalf("first item loc = %v, want limit", first)
	}
}

// `tag_groups` is parsed before the bank is looked up, so a malformed tree is
// the 400 even for a bank that does not exist.
func TestEntitiesTagGroupsPrecedesTheBank(t *testing.T) {
	mux := entityOrderServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/entities?tag_groups=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tag_groups=bogus = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid tag_groups") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// `tags` with `tag_groups` is rejected too, and also precedes the bank.
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/entities?tags=a&tag_groups=[]", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tags+tag_groups = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mutually exclusive") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// The tree decodes BEFORE the exclusivity refusal, so an invalid tree wins
	// the detail (api/http.py:292-301 validates, then refuses `tags`).
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/entities?tags=a&tag_groups=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tags+invalid tag_groups = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid tag_groups") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// Absence, blank and `null` are three different requests. The reference's
// shared decoder returns early on `raw is None or not raw.strip()`
// (api/http.py:283), so an absent parameter and a whitespace-only one are both
// no filter, while a `null` document reaches the JSON adapter and is its 400.
func TestTagGroupsDistinguishesAbsentFromEmptyAndNull(t *testing.T) {
	mux := entityOrderServer(t)
	for path, want := range map[string]int{
		"/v1/default/banks/b1/entities":                          http.StatusOK,
		"/v1/default/banks/b1/entities?tag_groups=":              http.StatusOK,
		"/v1/default/banks/b1/entities?tag_groups=%20":           http.StatusOK,
		"/v1/default/banks/b1/entities?tag_groups=null":          http.StatusBadRequest,
		"/v1/default/banks/b1/entities?tag_groups=%7B%7D":        http.StatusBadRequest,
		"/v1/default/banks/b1/entities?tag_groups=%5B%5D":        http.StatusOK,
		"/v1/default/banks/b1/entities?tags=work&tag_groups=":    http.StatusOK,
		"/v1/default/banks/b1/entities?tags=work&tag_groups=%20": http.StatusOK,
	} {
		rec := send(mux, http.MethodGet, path, "")
		if rec.Code != want {
			t.Fatalf("%s = %d, want %d: %s", path, rec.Code, want, rec.Body.String())
		}
	}
}

// The entity routes rank their `Query(...)` parameters above the entity id's
// own parse failure: a bad `tags_match` or `tag_groups` is reported even when
// the id is not a UUID.
func TestGetEntityValidatesQueryParamsBeforeTheID(t *testing.T) {
	mux := entityOrderServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/entities/not-a-uuid?tags_match=bogus", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tags_match before id = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodGet, "/v1/default/banks/b1/entities/not-a-uuid?tag_groups=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tag_groups before id = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid tag_groups") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// With valid parameters the id's own failure stands.
	rec = send(mux, http.MethodGet, "/v1/default/banks/b1/entities/not-a-uuid", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("plain invalid id = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "is not a valid UUID") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// A fuzzy leaf is resolved against the bank's own vocabulary before the read,
// so a filter of `topic:typecsript` reaches the memories tagged
// `topic:typescript` — the reference resolves it above every SQL builder.
func TestEntityListResolvesFuzzyTagGroups(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := familyRoutes(store)
	// Seed a tagged memory through the retain pipeline so the tag exists in the
	// bank's vocabulary and one entity carries it.
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bank,
		Items: []storage.RetainItem{{
			Content:    "Carol reviews the typecsript migration.",
			DocumentID: "doc-ts",
			Entities:   []storage.RetainEntity{{Text: "Carol", Kind: "PERSON"}},
			Tags:       []string{"topic:typescript"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	fuzzy := `[{"tags":["topic:typecsript"],"resolve":"fuzzy"}]`
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/entities?tag_groups="+url.QueryEscape(fuzzy), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fuzzy = %d: %s", rec.Code, rec.Body.String())
	}
	var fuzzyPage struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fuzzyPage); err != nil {
		t.Fatal(err)
	}
	exact := `[{"tags":["topic:typescript"],"resolve":"exact"}]`
	rec = send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/entities?tag_groups="+url.QueryEscape(exact), "")
	var exactPage struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exactPage); err != nil {
		t.Fatal(err)
	}
	if fuzzyPage.Total == 0 {
		t.Fatalf("fuzzy filter matched nothing; the leaf was compared literally")
	}
	if fuzzyPage.Total != exactPage.Total {
		t.Fatalf("fuzzy total %d != exact total %d", fuzzyPage.Total, exactPage.Total)
	}
}

// An empty compound group (`{"and":[]}` and friends) is a filter that states no
// constraint. The reference answers it with a 500: its SQL builder emits an empty
// conjunction and Postgres rejects the statement. We deliberately do not
// reproduce that -- a malformed query string surfaced as a crash is a defect,
// not a contract, and a drop-in service should not inherit one -- so the route
// answers normally.
//
// This test pins that it answers, and does not pin *which* entities come back:
// the two backends disagree there today (Postgres treats the empty group as
// match-all, the in-memory one as match-nothing). That disagreement is a
// storage-layer matter, recorded in the audit ledger rather than settled here.
// What must not happen is a 500, or a regression into one when the reference's
// behavior is next revisited.
func TestEmptyCompoundTagGroupAnswersInsteadOfCrashing(t *testing.T) {
	store := storage.NewMemoryStore("public")
	seedFixture(t, store)
	mux := familyRoutes(store)
	for _, tree := range []string{
		`[{"and":[]}]`,
		`[{"or":[]}]`,
		`[{"and":[{"and":[]}]}]`,
		`[{"not":{"and":[]}}]`,
	} {
		rec := send(mux, http.MethodGet,
			"/v1/default/banks/"+bank+"/entities?tag_groups="+url.QueryEscape(tree), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200 (the reference's 500 is a defect we do not copy): %s",
				tree, rec.Code, rec.Body.String())
		}
		var page struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("%s: %v (%s)", tree, err, rec.Body.String())
		}
		// A 200 with a well-formed page is the whole contract here.
		if page.Total < 0 {
			t.Fatalf("%s total = %d", tree, page.Total)
		}
	}
}

// Neither graph route declares `offset` in the reference (api_graph takes
// `limit` alone, api_entity_graph takes `limit`/`min_count`/tags), and the
// framework ignores a query parameter no route declares. Reading it here would
// answer 422 to a request the reference serves.
func TestGraphRoutesIgnoreUndeclaredOffset(t *testing.T) {
	mux := entityOrderServer(t)
	for _, path := range []string{
		"/v1/default/banks/b1/graph?offset=abc",
		"/v1/default/banks/b1/entities/graph?offset=abc",
	} {
		rec := send(mux, http.MethodGet, path, "")
		// The answer is whatever the bank lookup says (404 here); what matters is
		// that `offset` was not validated on the way in.
		if rec.Code == http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want `offset` ignored: %s", path, rec.Code, rec.Body.String())
		}
	}
}

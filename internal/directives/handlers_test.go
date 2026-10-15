package directives

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newServer builds the route table over an in-memory store holding one bank.
func newServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			// The real router supplies path values; the bare mux needs them set.
			r.SetPathValue("bank_id", "b1")
			if strings.Contains(pattern, "{directive_id}") {
				r.SetPathValue("directive_id", strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/default/banks/b1/directives/"), ""))
			}
			handler(w, r)
		})
	})
	return mux, store
}

// send issues one request against the family's route table.
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

// The full create → list → get → update → delete lifecycle through the routes.
func TestDirectiveLifecycle(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"

	created := send(mux, http.MethodPost, base, `{"name":"tone","content":"be terse","priority":5}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d, body = %s", created.Code, created.Body.String())
	}
	var directive Directive
	if err := json.Unmarshal(created.Body.Bytes(), &directive); err != nil {
		t.Fatal(err)
	}
	if directive.Name != "tone" || !directive.IsActive || directive.Priority != 5 {
		t.Fatalf("created directive = %+v", directive)
	}
	if directive.Tags == nil {
		t.Fatal("tags must be an array, not null")
	}

	listed := send(mux, http.MethodGet, base, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list: %d", listed.Code)
	}
	var list ListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Total != 1 {
		t.Fatalf("list = %+v", list)
	}

	got := send(mux, http.MethodGet, base+"/"+directive.ID, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d, body = %s", got.Code, got.Body.String())
	}

	updated := send(mux, http.MethodPatch, base+"/"+directive.ID, `{"priority":9,"is_active":false}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}
	var after Directive
	if err := json.Unmarshal(updated.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Priority != 9 || after.IsActive {
		t.Fatalf("updated directive = %+v", after)
	}

	deleted := send(mux, http.MethodDelete, base+"/"+directive.ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete: %d", deleted.Code)
	}
	var deletedBody DeleteResponse
	if err := json.Unmarshal(deleted.Body.Bytes(), &deletedBody); err != nil {
		t.Fatal(err)
	}
	if deletedBody.Status != "deleted" {
		t.Fatalf("delete body = %+v", deletedBody)
	}
	if got := send(mux, http.MethodGet, base+"/"+directive.ID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", got.Code)
	}
}

// A create without a name or without content is a structured 422.
func TestDirectiveCreateValidation(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	for _, body := range []string{`{"content":"x"}`, `{"name":"x"}`, `{}`} {
		rec := send(mux, http.MethodPost, base, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("create %s: %d, want 422", body, rec.Code)
		}
		var failure struct {
			Detail []struct {
				Loc []string `json:"loc"`
				Msg string   `json:"msg"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if len(failure.Detail) == 0 || failure.Detail[0].Loc[0] != "body" {
			t.Fatalf("create %s detail = %+v", body, failure.Detail)
		}
	}
	if rec := send(mux, http.MethodPost, base, `not json`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body: %d, want 422", rec.Code)
	}
}

// The list defaults to active-only and orders by priority, and untagged
// directives stay in scope when a tag filter is supplied.
func TestDirectiveListDefaultsAndOrdering(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	// A low-priority active directive, a high-priority one created first, an
	// inactive one, and an untagged global directive.
	send(mux, http.MethodPost, base, `{"name":"high","content":"h","priority":9}`)
	send(mux, http.MethodPost, base, `{"name":"low","content":"l","priority":1}`)
	send(mux, http.MethodPost, base, `{"name":"off","content":"x","priority":50,"is_active":false}`)
	send(mux, http.MethodPost, base, `{"name":"global","content":"g","priority":5}`)

	rec := send(mux, http.MethodGet, base, "")
	var list ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	// The inactive directive is excluded by default; the rest are priority-ordered.
	if list.Total != 3 {
		t.Fatalf("default list must exclude inactive directives: %+v", list)
	}
	if list.Items[0].Name != "high" || list.Items[1].Name != "global" || list.Items[2].Name != "low" {
		t.Fatalf("directives must be priority-ordered: %+v", list.Items)
	}
	if list.Limit != 100 {
		t.Fatalf("default limit = %d, want 100", list.Limit)
	}

	// active_only=false includes the inactive directive.
	rec = send(mux, http.MethodGet, base+"?active_only=false", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 4 {
		t.Fatalf("active_only=false list = %+v", list)
	}

	// An invalid tags_match is a structured 422 on the query parameter.
	rec = send(mux, http.MethodGet, base+"?tags_match=all_strict", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid tags_match: %d, want 422", rec.Code)
	}
	// An out-of-range limit is a structured 422.
	if rec := send(mux, http.MethodGet, base+"?limit=0", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=0: %d, want 422", rec.Code)
	}
}

// A tag filter admits untagged (global) directives plus any tagged directive
// whose tags match the requested scope.
func TestDirectiveTagScoping(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	send(mux, http.MethodPost, base, `{"name":"global","content":"g"}`)
	send(mux, http.MethodPost, base, `{"name":"team","content":"t","tags":["team"]}`)
	send(mux, http.MethodPost, base, `{"name":"other","content":"o","tags":["other"]}`)

	rec := send(mux, http.MethodGet, base+"?tags=team", "")
	var list ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, item := range list.Items {
		names[item.Name] = true
	}
	if !names["global"] || !names["team"] || names["other"] {
		t.Fatalf("tag scoping = %+v", list.Items)
	}
}

// An explicit is_active of false is honored, which is what makes deactivation
// possible at creation time.
func TestDirectiveCreateHonorsExplicitInactive(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/directives",
		`{"name":"n","content":"c","is_active":false}`)
	var directive Directive
	if err := json.Unmarshal(rec.Body.Bytes(), &directive); err != nil {
		t.Fatal(err)
	}
	if directive.IsActive {
		t.Fatal("an explicit false must not be overridden by the default")
	}
}

// An unknown bank answers not-found, so the route cannot be used to probe.
func TestDirectiveUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodGet, "/v1/default/banks/missing/directives", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}
}

// A missing directive identifier answers not-found rather than an empty result.
func TestDirectiveMissingMember(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/directives/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing directive: %d, want 404", rec.Code)
	}
}

// asHTTPError maps each storage error to its envelope.
func TestDirectiveErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		expect int
	}{
		{storage.ErrBankNotFound, http.StatusNotFound},
		{storage.ErrDirectiveNotFound, http.StatusNotFound},
		{nil, 0},
	}
	for _, tc := range cases {
		got := asHTTPError(tc.err)
		if tc.err == nil {
			if got != nil {
				t.Fatalf("nil error must map to nil")
			}
			continue
		}
		if got == nil || got.Status != tc.expect {
			t.Fatalf("error %v mapped to %+v, want status %d", tc.err, got, tc.expect)
		}
	}
}

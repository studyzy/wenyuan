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
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "b1", Name: "Bank"}); err != nil {
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
	if created.Code != http.StatusCreated {
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
	if len(list.Directives) != 1 || list.BankID != "b1" {
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
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", deleted.Code)
	}
	if got := send(mux, http.MethodGet, base+"/"+directive.ID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", got.Code)
	}
}

// A create without a name or without content is rejected.
func TestDirectiveCreateValidation(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	for _, body := range []string{`{"content":"x"}`, `{"name":"x"}`, `{}`} {
		if rec := send(mux, http.MethodPost, base, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("create %s: %d, want 400", body, rec.Code)
		}
	}
	if rec := send(mux, http.MethodPost, base, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d, want 400", rec.Code)
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

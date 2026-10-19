package readsurface

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// paramOrderServer builds the read routes over a store holding no bank, so every
// request below addresses a bank that does not exist.
func paramOrderServer(t *testing.T) *http.ServeMux {
	t.Helper()
	// familyRoutes pins bank_id to the fixture's bank, which is right for the
	// lifecycle tests but hides the unknown-bank behaviour these tests are
	// about, so the bank is taken from the request path instead.
	store := storage.NewMemoryStore("public")
	seedFixture(t, store)
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	handlers.Reprocess = reprocessor{store: store}
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			rest := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
			if bank, _, ok := strings.Cut(rest, "/"); ok {
				r.SetPathValue("bank_id", bank)
			}
			for _, param := range []string{"document_id", "chunk_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	return mux
}

// detailItems decodes the framework's structured 422 body.
func detailItems(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var envelope struct {
		Detail []map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode detail: %v (%s)", err, body)
	}
	return envelope.Detail
}

// The document listing's query parameters are resolved before the bank is
// looked up, because the reference declares them as `Query(...)` and the
// framework binds them ahead of the handler body. A bad parameter is therefore
// the 422 even for a bank that does not exist.
func TestListDocumentsValidatesBeforeTheBankLookup(t *testing.T) {
	mux := paramOrderServer(t)
	cases := map[string]string{
		"/v1/default/banks/absent/documents?limit=-1":         "greater_than_equal",
		"/v1/default/banks/absent/documents?limit=abc":        "int_parsing",
		"/v1/default/banks/absent/documents?offset=-1":        "greater_than_equal",
		"/v1/default/banks/absent/documents?time_field=bogus": "literal_error",
	}
	for path, want := range cases {
		rec := send(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s body %q lacks %s", path, rec.Body.String(), want)
		}
	}
}

// The start/end dates are parsed inside the reference's handler, so they also
// precede the bank lookup — but they come *after* the `Query` parameters, which
// is why a bad limit still wins over a bad start_date.
func TestListDocumentsDateParamsFollowQueryParamsButPrecedeTheBank(t *testing.T) {
	mux := paramOrderServer(t)
	// A bad start_date beats the unknown-bank 404.
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/documents?start_date=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start_date=bogus = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid start_date") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A bad limit beats the bad start_date.
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/documents?limit=-1&start_date=bogus", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=-1&start_date=bogus = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// One 422 lists every rejected `Query` parameter, in the order the route
// declares them — a single failure at a time would be a different body.
func TestListDocumentsReportsEveryBadParameterInOneEnvelope(t *testing.T) {
	mux := paramOrderServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/documents?limit=-1&time_field=bogus", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	items := detailItems(t, rec.Body.Bytes())
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %s", len(items), rec.Body.String())
	}
	// time_field is declared before limit.
	first := items[0]["loc"].([]any)
	second := items[1]["loc"].([]any)
	if first[len(first)-1] != "time_field" || second[len(second)-1] != "limit" {
		t.Fatalf("order = %v then %v, want time_field then limit", first, second)
	}
}

// The chunk listing's page size is a `Query(...)` parameter, so it is validated
// before the document is looked up.
func TestListChunksValidatesPageParamsBeforeTheDocument(t *testing.T) {
	mux := paramOrderServer(t)
	cases := map[string]string{
		"/v1/default/banks/absent/documents/x/chunks?limit=0":    "greater_than_equal",
		"/v1/default/banks/absent/documents/x/chunks?limit=1001": "less_than_equal",
		"/v1/default/banks/absent/documents/x/chunks?limit=abc":  "int_parsing",
		"/v1/default/banks/absent/documents/x/chunks?offset=-1":  "greater_than_equal",
	}
	for path, want := range cases {
		rec := send(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s body %q lacks %s", path, rec.Body.String(), want)
		}
	}
	// With valid parameters the missing document is still the 404.
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/documents/x/chunks", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("plain chunks = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// The tag listing validates `source`, `limit` and `offset` before the bank, in
// declaration order, and lists all failures together.
func TestListTagsValidatesBeforeTheBankLookup(t *testing.T) {
	mux := paramOrderServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/absent/tags?limit=-1", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=-1 = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/tags?source=bogus", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("source=bogus = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// Both bad: one envelope, source first because the route declares it first.
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/tags?source=bogus&limit=-1", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("combined = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	items := detailItems(t, rec.Body.Bytes())
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %s", len(items), rec.Body.String())
	}
	first := items[0]["loc"].([]any)
	if first[len(first)-1] != "source" {
		t.Fatalf("first item loc = %v, want source", first)
	}
	// With valid parameters the unknown bank is still the 404.
	rec = send(mux, http.MethodGet, "/v1/default/banks/absent/tags", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("plain tags = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

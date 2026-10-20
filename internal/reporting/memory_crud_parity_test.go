package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference binds `q`, `type` and `document_id` straight from the request
// model and its store treats only "" as absent (api/http.py:5815-5930 →
// engine/memories/pg/curation.py:89-180). A whitespace-padded value is
// therefore a filter that matches nothing, not one to be trimmed away.
func TestMemoryListDoesNotTrimFilters(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	// An all-whitespace search is a term nothing contains; trimming would have
	// turned it into "no search" and returned the whole page.
	if got := listItemCount(t, mux, base+"/memories/list?q=%20%20%20"); got != 0 {
		t.Fatalf("blank q listed %d items, want 0", got)
	}
	// A padded term is matched literally, so a term the text does not contain
	// padded finds nothing — trimming would have matched it as "deployment".
	if got := listItemCount(t, mux, base+"/memories/list?q=%20deployments"); got != 0 {
		t.Fatalf("padded q listed %d items, want 0", got)
	}
	// A padded fact type is a type no unit carries.
	if got := listItemCount(t, mux, base+"/memories/list?type=%20world"); got != 0 {
		t.Fatalf("padded type listed %d items, want 0", got)
	}
	// A padded document id names no document.
	if got := listItemCount(t, mux, base+"/memories/list?document_id=%20doc-1"); got != 0 {
		t.Fatalf("padded document_id listed %d items, want 0", got)
	}
	// The unpadded forms still filter.
	if got := listItemCount(t, mux, base+"/memories/list?type=world"); got != 1 {
		t.Fatalf("type=world listed %d items, want 1", got)
	}
	if got := listItemCount(t, mux, base+"/memories/list?document_id=doc-1"); got != 1 {
		t.Fatalf("document_id=doc-1 listed %d items, want 1", got)
	}
}

// A limit the reference's request model accepts but its driver cannot bind is a
// 500 carrying the driver's own text, not the framework's 422
// (asyncpg pgproto/codecs/int.pyx:109-111 → prepared_stmt.pyx:205-211).
func TestMemoryListLimitBeyondInt64IsDriverError(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/memories/list?limit=9223372036854775808", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("out-of-range limit = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	want := "invalid input for query argument $2: 9223372036854775808 (value out of int64 range)"
	if got := decodeObject(t, rec)["detail"]; got != want {
		t.Fatalf("detail = %v, want %q", got, want)
	}
	// The largest bindable value is still a normal page.
	if rec := send(mux, http.MethodGet, base+"/memories/list?limit=9223372036854775807", ""); rec.Code != http.StatusOK {
		t.Fatalf("max int64 limit = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// A value that is not an integer at all keeps the framework's 422.
	if rec := send(mux, http.MethodGet, base+"/memories/list?limit=abc", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-integer limit = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// DELETE .../memories/{id}/observations is the one by-id memory route that does
// not normalize the id (api/http.py:9581-9606): a malformed one reaches the
// driver, and the route's catch-all answers 500 with the driver's uuid error.
// GET/PATCH/history normalize it and answer the named 400 instead
// (engine/memory_engine.py:13145-13190, 11916-11950, 13363-13400).
func TestClearMemoryObservationsDoesNotNormalizeID(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	const source = "66666666-6666-4666-8666-666666666666"
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: source, BankID: bank, Text: "the deployment runs on Tuesdays", FactType: storage.TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}
	// An observation resting on that fact is what the clear removes.
	if _, err := store.CreateObservation(ctx, bank, "deployments happen weekly", []string{source}); err != nil {
		t.Fatal(err)
	}
	mux := reportingMux(store)
	base := "/v1/default/banks/" + bank
	const bad = "not-a-uuid"

	rec := send(mux, http.MethodDelete, base+"/memories/"+bad+"/observations", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("clear bad id = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if got := decodeObject(t, rec)["detail"]; got != "badly formed hexadecimal UUID string" {
		t.Fatalf("detail = %v, want the driver's uuid message", got)
	}
	// A well-formed id still clears and reports its count.
	rec = send(mux, http.MethodDelete, base+"/memories/"+source+"/observations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear known id = %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeObject(t, rec)["deleted_count"]; got != float64(1) {
		t.Fatalf("deleted_count = %v, want 1", got)
	}
	// Clearing again finds nothing left to remove.
	rec = send(mux, http.MethodDelete, base+"/memories/"+source+"/observations", "")
	if got := decodeObject(t, rec)["deleted_count"]; got != float64(0) {
		t.Fatalf("second clear deleted_count = %v, want 0", got)
	}
}

// The documenting control: the normalizing routes answer their named 400 for
// the same id the clear route lets through.
func TestMemoryByIDRoutesNameABadIdentifier(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	const bad = "not-a-uuid"
	want := "Invalid memory_id: '" + bad + "' is not a valid UUID"

	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, base + "/memories/" + bad, ""},
		{http.MethodGet, base + "/memories/" + bad + "/history", ""},
		{http.MethodPatch, base + "/memories/" + bad, `{"text":"x"}`},
	} {
		rec := send(mux, probe.method, probe.path, probe.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s = %d, want 400: %s", probe.method, probe.path, rec.Code, rec.Body.String())
		}
		if got := decodeObject(t, rec)["detail"]; got != want {
			t.Fatalf("%s %s detail = %v, want %q", probe.method, probe.path, got, want)
		}
	}
}

// listItemCount returns how many items one listing page carries.
func listItemCount(t *testing.T, mux *http.ServeMux, path string) int {
	t.Helper()
	rec := send(mux, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body.String())
	}
	items, _ := decodeObject(t, rec)["items"].([]any)
	// Guard against a body that is not a listing at all.
	if !strings.Contains(rec.Body.String(), `"total"`) {
		t.Fatalf("%s is not a listing: %s", path, rec.Body.String())
	}
	return len(items)
}

// The framework reads the body before it instantiates the model
// (api/http.py:6092-6110 → UpdateMemoryRequest at :2777-2858): an absent body is
// the `missing` item for the required body field, while a value of the wrong
// type names the value that arrived.
func TestMemoryUpdateBodyShape(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	const unit = "44444444-4444-4444-4444-444444444444"

	rec := send(mux, http.MethodPatch, base+"/memories/"+unit, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("absent body = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	want := []map[string]any{{"type": "missing", "loc": []any{"body"}, "msg": "Field required", "input": nil}}
	if got := decodeObject(t, rec)["detail"]; !sameJSON(got, want) {
		t.Fatalf("absent body detail = %v, want %v", got, want)
	}

	// A rejected member names the value the client sent, not a zero value.
	rec = send(mux, http.MethodPatch, base+"/memories/"+unit, `{"text":5}`)
	if got := decodeObject(t, rec)["detail"]; !sameJSON(got, []map[string]any{{
		"type": "string_type", "loc": []any{"body", "text"},
		"msg": "Input should be a valid string", "input": float64(5),
	}}) {
		t.Fatalf("text type detail = %v", got)
	}
	rec = send(mux, http.MethodPatch, base+"/memories/"+unit, `{"entities":5}`)
	if got := decodeObject(t, rec)["detail"]; !sameJSON(got, []map[string]any{{
		"type": "list_type", "loc": []any{"body", "entities"},
		"msg": "Input should be a valid list", "input": float64(5),
	}}) {
		t.Fatalf("entities type detail = %v", got)
	}
}

// A client that repeats a scalar parameter binds its FINAL occurrence, which is
// what the framework's request model does; the list route must not bind the
// first one instead.
func TestMemoryListRepeatedScalarBindsLast(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	// limit: the last repeated value is the page size.
	body := decodeObject(t, send(mux, http.MethodGet, base+"/memories/list?limit=1&limit=50", ""))
	if got := body["limit"]; got != float64(50) {
		t.Fatalf("repeated limit = %v, want the final 50", got)
	}
	body = decodeObject(t, send(mux, http.MethodGet, base+"/memories/list?limit=50&limit=1", ""))
	if got := body["limit"]; got != float64(1) {
		t.Fatalf("reversed repeated limit = %v, want the final 1", got)
	}
	// type: the last one filters.
	if got := listFactTypes(t, mux, base+"/memories/list?type=world&type=observation"); !sameJSON(got, []any{"observation"}) {
		t.Fatalf("repeated type listed %v, want the final type", got)
	}
	if got := listFactTypes(t, mux, base+"/memories/list?type=observation&type=world"); !sameJSON(got, []any{"world"}) {
		t.Fatalf("reversed repeated type listed %v, want the final type", got)
	}
}

// listFactTypes returns the fact types of one listing page.
func listFactTypes(t *testing.T, mux *http.ServeMux, path string) []any {
	t.Helper()
	items, _ := decodeObject(t, send(mux, http.MethodGet, path, ""))["items"].([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any)["fact_type"])
	}
	return out
}

// sameJSON compares two decoded JSON values structurally.
func sameJSON(left, right any) bool {
	l, err1 := json.Marshal(left)
	r, err2 := json.Marshal(right)
	return err1 == nil && err2 == nil && string(l) == string(r)
}

// A clear that removed observations submits exactly one consolidation when the
// bank consolidates automatically (engine/memory_engine.py:11879-11882: only
// `deleted_count > 0`, then `if config.enable_auto_consolidation`); a clear
// that removed nothing submits nothing, and a bank with the switch off never
// submits.
func TestClearObservationsSubmitsConsolidationWhenAutoEnabled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		auto      any
		observing bool
		wantOps   int
	}{
		{name: "auto on, observations removed", auto: true, observing: true, wantOps: 1},
		{name: "auto off, observations removed", auto: false, observing: true, wantOps: 0},
		{name: "auto on, nothing removed", auto: true, observing: false, wantOps: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			ctx := context.Background()
			if _, err := store.CreateBank(ctx, storage.Bank{
				BankID: bank, Name: "Bank", Config: map[string]any{"enable_auto_consolidation": tc.auto},
			}); err != nil {
				t.Fatal(err)
			}
			const source = "66666666-6666-4666-8666-666666666666"
			if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
				ID: source, BankID: bank, Text: "the deployment runs on Tuesdays", FactType: storage.TypeWorld,
			}); err != nil {
				t.Fatal(err)
			}
			if tc.observing {
				if _, err := store.CreateObservation(ctx, bank, "deployments happen weekly", []string{source}); err != nil {
					t.Fatal(err)
				}
			}
			mux := reportingMux(store)
			rec := send(mux, http.MethodDelete, "/v1/default/banks/"+bank+"/memories/"+source+"/observations", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("clear = %d: %s", rec.Code, rec.Body.String())
			}
			ops, err := store.ListOperations(ctx, storage.OperationQuery{BankID: bank})
			if err != nil {
				t.Fatal(err)
			}
			if len(ops.Operations) != tc.wantOps {
				t.Fatalf("queued %d operations, want %d", len(ops.Operations), tc.wantOps)
			}
		})
	}
}

// The listing joins `unit_entities` alone, without an observation's inherited
// postings, and the reference's SQL there orders by `ue.unit_id` only
// (engine/memories/pg/curation.py:303-311), so within one unit the names arrive
// in whatever order the engine produced them rather than alphabetically. This
// pins the listing to that rule: an observation carries no entities, and a unit
// whose names are not in alphabetical order keeps its own order instead of being
// re-sorted into one the reference never produces.
func TestMemoryListEntitiesAreTheUnitsOwnUnsortedPostings(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	const unit = "66666666-6666-6666-6666-666666666666"
	const source = "77777777-7777-7777-7777-777777777777"
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: source, BankID: bank, Text: "the migration window opens at midnight", FactType: storage.TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}
	// Deliberately not alphabetical: "Migration" sorts after "Deployment".
	if err := store.SetUnitEntities(ctx, bank, source, []string{"Deployment", "Migration"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: unit, BankID: bank, Text: "migrations follow deployments", FactType: storage.TypeObservation,
		SourceMemoryIDs: []string{source},
	}); err != nil {
		t.Fatal(err)
	}
	mux := reportingMux(store)
	cases := []struct {
		path string
		want string
	}{
		// The posting's own order survives; an alphabetical sort would flip it.
		{path: "/v1/default/banks/" + bank + "/memories/list?type=world", want: "Deployment, Migration"},
		// An observation has no postings, so the listing shows no entities even
		// though its detail view inherits its source's.
		{path: "/v1/default/banks/" + bank + "/memories/list?type=observation", want: ""},
	}
	for _, testCase := range cases {
		rec := send(mux, http.MethodGet, testCase.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", testCase.path, rec.Code, rec.Body.String())
		}
		items, _ := decodeObject(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%s listed %d items, want 1", testCase.path, len(items))
		}
		item, _ := items[0].(map[string]any)
		if got, _ := item["entities"].(string); got != testCase.want {
			t.Fatalf("%s entities = %q, want %q", testCase.path, got, testCase.want)
		}
	}
}

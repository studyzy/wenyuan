package reporting

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// curatedUnitID is the world fact memoryCurationServer seeds for the curation
// tests.
const curatedUnitID = "66666666-6666-4666-8666-666666666666"

// memoryCurationServer is a server over a bank holding one curated world fact,
// which is the row the PATCH state machine operates on.
func memoryCurationServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: curatedUnitID, BankID: bank, Text: "curated fact", FactType: storage.TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}
	return reportingMux(store), store
}

// emptyBankServer is a server over a bank with no memory units, for the cases
// whose answer does not depend on rows.
func emptyBankServer(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return muxWithStore(store)
}

// muxWithStore binds the family's routes to their real path parameters, so a
// request naming a bank the store does not hold reaches the handler as such
// (reportingMux pins every request onto the fixture bank).
func muxWithStore(store storage.Store) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v1/default/banks/") {
				rest := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
				parts := strings.Split(rest, "/")
				r.SetPathValue("bank_id", parts[0])
				for _, param := range []string{"memory_id", "operation_id", "node_id", "page_id", "chunk_id"} {
					if strings.Contains(pattern, "{"+param+"}") {
						r.SetPathValue(param, lastSegment(r.URL.Path))
					}
				}
			}
			handler(w, r)
		})
	})
	return mux
}

// The reference looks the bank up before its store validates `state`,
// `consolidation_state`, `entity_id` or the time window
// (engine/memory_engine.py:13216-13217 `_require_bank_exists`, then
// engine/memories/pg/curation.py:167-172 and engine/time_filter.py), so an
// unknown bank is a 404 naming it whatever else the query carries. The two
// `Literal` members and the two date parsers are resolved before the handler is
// called (api/http.py:5832-5843 `_parse_iso_datetime`), so those still answer
// first.
func TestMemoryListChecksTheBankBeforeTheStoreFilters(t *testing.T) {
	mux := emptyBankServer(t)
	base := "/v1/default/banks/ghost-bank/memories/list"
	for _, query := range []string{
		"?state=bogus",
		"?consolidation_state=bogus",
		"?entity_id=not-a-uuid",
		"?start_date=2025-01-01&end_date=2024-01-01",
		"?state=bogus&consolidation_state=bogus",
	} {
		rec := send(mux, http.MethodGet, base+query, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404: %s", query, rec.Code, rec.Body.String())
		}
		if got := decodeObject(t, rec)["detail"]; got != "Bank 'ghost-bank' not found" {
			t.Fatalf("%s detail = %v", query, got)
		}
	}
	// A malformed date is the handler argument's own failure, and the framework
	// evaluates it before the handler's first statement runs.
	rec := send(mux, http.MethodGet, base+"?start_date=bad&state=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad start_date = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := decodeObject(t, rec)["detail"]; got != "Invalid start_date: 'bad' is not an ISO-8601 datetime." {
		t.Fatalf("bad start_date detail = %v", got)
	}
	// The same validations still answer on a bank that exists.
	for _, want := range []struct {
		query  string
		status int
		detail string
	}{
		{"?state=bogus", http.StatusBadRequest, "Invalid state 'bogus': expected 'valid' or 'invalidated'."},
		{"?consolidation_state=bogus", http.StatusBadRequest, "Invalid consolidation_state 'bogus': expected 'failed', 'pending', or 'done'."},
		{"?entity_id=not-a-uuid", http.StatusBadRequest, "Invalid entity_id: 'not-a-uuid' is not a valid UUID"},
		{"?start_date=2025-01-01&end_date=2024-01-01", http.StatusBadRequest, "Invalid time window: end_date must be after start_date."},
	} {
		rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/memories/list"+want.query, "")
		if rec.Code != want.status {
			t.Fatalf("%s = %d, want %d: %s", want.query, rec.Code, want.status, rec.Body.String())
		}
		if got := decodeObject(t, rec)["detail"]; got != want.detail {
			t.Fatalf("%s detail = %v, want %q", want.query, got, want.detail)
		}
	}
}

// The page bounds occupy the reference's placeholders — bank_id is $1 and limit
// and offset are $2 and $3 (engine/memories/pg/curation.py:284-292) — so a bound
// only the driver can reject is reported under that number.
func TestMemoryListPageBoundsUseTheReferencePlaceholders(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank + "/memories/list"
	rec := send(mux, http.MethodGet, base+"?offset=9223372036854775808", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("out-of-range offset = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	want := "invalid input for query argument $3: 9223372036854775808 (value out of int64 range)"
	if got := decodeObject(t, rec)["detail"]; got != want {
		t.Fatalf("detail = %v, want %q", got, want)
	}
}

// A curation body may both edit a fact and invalidate it. The reference applies
// the edit to the live row and only then moves it into the archive
// (engine/memory_engine.py:12194-12260 edit, then :12350-12380 invalidate), so
// the archived row carries the edited text, not the text it had before.
func TestMemoryUpdateAppliesTheEditBeforeInvalidating(t *testing.T) {
	mux, store := memoryCurationServer(t)
	base := "/v1/default/banks/" + bank + "/memories/" + curatedUnitID
	rec := send(mux, http.MethodPatch, base, `{"text":"edited before archive","state":"invalidated","reason":"done"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeObject(t, rec)
	if body["text"] != "edited before archive" {
		t.Fatalf("response text = %v", body["text"])
	}
	if body["state"] != "invalidated" || body["invalidation_reason"] != "done" {
		t.Fatalf("response state = %v / %v", body["state"], body["invalidation_reason"])
	}
	unit, err := store.GetMemory(context.Background(), bank, curatedUnitID)
	if err != nil {
		t.Fatal(err)
	}
	if unit.Text != "edited before archive" {
		t.Fatalf("stored text = %q, want the edit", unit.Text)
	}
	if unit.InvalidatedAt == nil {
		t.Fatal("the unit is not archived")
	}
	if unit.EditedAt == nil {
		t.Fatal("the edit stamp is missing from the archived row")
	}
	// The edit's link/observation effects ran too, so the row is in the archive
	// and gone from the live list.
	rec = send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/memories/list?state=invalidated", "")
	if items, _ := decodeObject(t, rec)["items"].([]any); len(items) != 1 {
		t.Fatalf("archived listing = %v, want the one edited row", items)
	}
}

// A revert wins over an edit in the same body: the archived row is restored
// first, and a live row is not the row the edit was prepared from, so the edit
// is dropped exactly as the reference's phase 2 drops it.
func TestMemoryUpdateRevertDropsTheEdit(t *testing.T) {
	mux, store := memoryCurationServer(t)
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/"+bank+"/memories/"+curatedUnitID,
		`{"state":"invalidated"}`); rec.Code != http.StatusOK {
		t.Fatalf("invalidate = %d: %s", rec.Code, rec.Body.String())
	}
	rec := send(mux, http.MethodPatch, "/v1/default/banks/"+bank+"/memories/"+curatedUnitID,
		`{"state":"valid"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("revert = %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeObject(t, rec)["text"]; got != "curated fact" {
		t.Fatalf("reverted text = %v, want the original", got)
	}
	unit, err := store.GetMemory(context.Background(), bank, curatedUnitID)
	if err != nil {
		t.Fatal(err)
	}
	if unit.Text != "curated fact" {
		t.Fatalf("stored text = %q, want the original", unit.Text)
	}
	if unit.EditedAt != nil {
		t.Fatal("a dropped edit must not stamp the row")
	}
}

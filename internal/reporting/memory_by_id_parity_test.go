package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference's by-id memory methods normalize the identifier before they
// touch the store — `str(uuid.UUID(memory_id))` in engine/memory_engine.py:13247
// (`get_memory_unit`), :11916 (`update_memory_unit`) and :13363
// (`get_observation_history`). CPython's UUID accepts forms a strict 8-4-4-4-12
// parse does not, so a client that sends one must still reach the unit; passing
// the raw text to the driver instead answers a 500 that the reference never
// produces. The route-level falsifier is the reverse assertion:
// `TestMemoryByIDRoutesNameABadIdentifier` keeps the malformed case at 400.
func TestMemoryByIDRoutesNormalizeTheIdentifier(t *testing.T) {
	const unit = "44444444-4444-4444-4444-444444444444"
	for _, tc := range []struct{ name, id string }{
		{"urn uuid prefix", "urn:uuid:" + unit},
		{"bare urn prefix", "urn:" + unit},
		{"braced", "{" + unit + "}"},
		{"urn inside braces", "{urn:uuid:" + unit + "}"},
		{"uppercase", "44444444-4444-4444-4444-444444444444"},
		{"hyphen free", "44444444444444444444444444444444"},
		{"misgrouped", "--" + unit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := parityServer(t)
			base := "/v1/default/banks/" + bank
			for _, probe := range []struct{ method, path, body string }{
				{http.MethodGet, base + "/memories/" + tc.id, ""},
				{http.MethodGet, base + "/memories/" + tc.id + "/history", ""},
				{http.MethodPatch, base + "/memories/" + tc.id, `{"context":"curated"}`},
			} {
				rec := send(mux, probe.method, probe.path, probe.body)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s = %d, want 200: %s",
						probe.method, probe.path, rec.Code, rec.Body.String())
				}
				// The response carries the canonical spelling, not the one sent.
				if got := unitIDOf(t, rec, unit); got != unit {
					t.Fatalf("%s %s id = %v, want the canonical %s",
						probe.method, probe.path, got, unit)
				}
			}
		})
	}
}

// Whitespace is not part of CPython's leniency: it survives the prefix and
// brace rules, so the identifier is not a UUID and the reference answers its
// named 400 with the value exactly as sent (never trimmed, never a driver 500).
func TestMemoryByIDRoutesDoNotTrimTheIdentifier(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	const unit = "44444444-4444-4444-4444-444444444444"

	for _, sent := range []string{" " + unit, unit + " ", " " + unit + " "} {
		// The path is percent-encoded so the space survives the request line;
		// the value the handler reads is the decoded one, exactly as a client
		// would send it.
		escaped := url.PathEscape(sent)
		want := "Invalid memory_id: '" + sent + "' is not a valid UUID"
		for _, probe := range []struct{ method, path, body string }{
			{http.MethodGet, base + "/memories/" + escaped, ""},
			{http.MethodGet, base + "/memories/" + escaped + "/history", ""},
			{http.MethodPatch, base + "/memories/" + escaped, `{"text":"x"}`},
		} {
			rec := send(mux, probe.method, probe.path, probe.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %q = %d, want 400: %s", probe.method, sent, rec.Code, rec.Body.String())
			}
			if got := decodeObject(t, rec)["detail"]; got != want {
				t.Fatalf("%s %q detail = %v, want %q", probe.method, sent, got, want)
			}
		}
	}
}

// The history route is a miss for a unit curation moved to the invalidation
// archive, even though the detail route keeps serving that archived row.
//
// Measured against the live reference on 2026-11-03 on a fresh, still-live
// world fact: `PATCH {"state":"invalidated"}` then
// `GET .../memories/{id}/history` is 404 "Memory unit '<id>' not found", while
// `GET .../memories/{id}` stays 200 with `state:"invalidated"`. The reference
// moves the row out of `memory_units` (engine/memories/pg/writes.py:421-456) and
// `get_observation_history` reads only that table
// (engine/memory_engine.py:13846-13855).
//
// Falsifier: drop the invalidation guard from MemoryHistory, which is what makes
// this assert 404 rather than the `[]` a live non-observation fact gets.
func TestMemoryHistoryOfAnInvalidatedUnitIsNotFound(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	const unit = "55555555-5555-4555-8555-555555555555"
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: unit, BankID: bank, Text: "the deployment runs on Tuesdays", FactType: storage.TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}
	mux := reportingMux(store)
	base := "/v1/default/banks/" + bank

	// A live unit with no recorded change is an empty array.
	rec := send(mux, http.MethodGet, base+"/memories/"+unit+"/history", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "[]" {
		t.Fatalf("live history = %d %s, want 200 []", rec.Code, rec.Body.String())
	}
	invalid := "invalidated"
	reason := "no longer true"
	if _, err := store.UpdateMemory(ctx, bank, unit, storage.MemoryPatch{State: &invalid, Reason: &reason}); err != nil {
		t.Fatal(err)
	}
	rec = send(mux, http.MethodGet, base+"/memories/"+unit+"/history", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalidated history = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := decodeObject(t, rec)["detail"]; got != "Memory unit '"+unit+"' not found" {
		t.Fatalf("detail = %v", got)
	}
	// The detail route keeps serving the archived row, which is what makes the
	// asymmetry visible to a client.
	if rec := send(mux, http.MethodGet, base+"/memories/"+unit, ""); rec.Code != http.StatusOK {
		t.Fatalf("invalidated detail = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The list route's limit/offset are plain framework integers: the coercion
// accepts a zero fraction and single underscores between digits, and an empty
// value is a parsing failure rather than an absent parameter
// (engine/memories/pg/curation.py:89-100 → the listing's declared `int`).
func TestMemoryListIntegerParameterForms(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	for _, tc := range []struct {
		query  string
		status int
		limit  float64
	}{
		{query: "limit=5.0", status: http.StatusOK, limit: 5},
		{query: "limit=5.00", status: http.StatusOK, limit: 5},
		{query: "limit=1_0", status: http.StatusOK, limit: 10},
		{query: "limit=%2B5", status: http.StatusOK, limit: 5},
		{query: "limit=5", status: http.StatusOK, limit: 5},
		// A non-zero fraction is not an integer, and neither is an empty value.
		{query: "limit=5.5", status: http.StatusUnprocessableEntity},
		{query: "limit=_5", status: http.StatusUnprocessableEntity},
		{query: "limit=5_", status: http.StatusUnprocessableEntity},
		{query: "limit=5__0", status: http.StatusUnprocessableEntity},
		{query: "limit=5e2", status: http.StatusUnprocessableEntity},
		{query: "limit=", status: http.StatusUnprocessableEntity},
		{query: "offset=", status: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := send(mux, http.MethodGet, base+"/memories/list?"+tc.query, "")
			if rec.Code != tc.status {
				t.Fatalf("%s = %d, want %d: %s", tc.query, rec.Code, tc.status, rec.Body.String())
			}
			body := decodeObject(t, rec)
			if tc.status != http.StatusOK {
				// An empty value reports the empty string as the item's input,
				// which is what tells it apart from an absent parameter.
				items, _ := body["detail"].([]any)
				if len(items) != 1 {
					t.Fatalf("%s detail = %v, want one item", tc.query, body["detail"])
				}
				item := items[0].(map[string]any)
				if item["type"] != "int_parsing" {
					t.Fatalf("%s item type = %v, want int_parsing", tc.query, item["type"])
				}
				// The item names the value exactly as sent, which is how an
				// empty parameter differs from an absent one.
				raw := strings.SplitN(tc.query, "=", 2)[1]
				if input, present := item["input"]; !present || input != raw {
					t.Fatalf("%s input = %v (present %v), want %q", tc.query, input, present, raw)
				}
				return
			}
			if got := body["limit"]; got != tc.limit {
				t.Fatalf("%s limit = %v, want %v", tc.query, got, tc.limit)
			}
		})
	}
}

// The date parameters are validated with CPython's datetime.fromisoformat
// (api/http.py:350-380 `_parse_iso_datetime`), which accepts the basic
// separator-free date, an hour-only or basic clock, a comma fraction and the
// extended offsets — none of which a single RFC 3339 layout covers. A value the
// reference rejects keeps its 400 with the value as sent.
func TestMemoryListAcceptsPythonISODateForms(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	for _, value := range []string{
		"20240101",
		"2024-01-01",
		"2024-01-01T00:00",
		"2024-01-01T00",
		"2024-01-01T0000",
		"2024-01-01T00:00:00.5Z",
		"2024-01-01T00:00:00,5",
		"2024-01-01T00:00:00+05",
		"2024-01-01T00:00:00+0530",
		"2024-01-01T00:00:00+05:30",
		"20240101T000000",
		"2024-01-01 00:00",
	} {
		t.Run(value, func(t *testing.T) {
			rec := send(mux, http.MethodGet, base+"/memories/list?start_date="+url.QueryEscape(value), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("start_date=%s = %d, want 200: %s", value, rec.Code, rec.Body.String())
			}
		})
	}
	for _, value := range []string{"2024-01-01X00:00:00", "2024-0101", "2024-1-1", "2024-01-01T0:00", "now", "2024-01-01T24:00:00"} {
		t.Run("reject "+value, func(t *testing.T) {
			rec := send(mux, http.MethodGet, base+"/memories/list?start_date="+url.QueryEscape(value), "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("start_date=%s = %d, want 400: %s", value, rec.Code, rec.Body.String())
			}
			want := "Invalid start_date: '" + value + "' is not an ISO-8601 datetime."
			if got := decodeObject(t, rec)["detail"]; got != want {
				t.Fatalf("detail = %v, want %q", got, want)
			}
		})
	}
}

// A body carrying a numeric literal that overflows to infinity is not one the
// reference's model ever sees: CPython parses `1e999` as `inf` and then fails
// to serialize its own error, so the request ends at the ASGI server's
// plain-text fallback. The status is the same 500 either way, but the body is
// not the API's {"detail": …} envelope.
func TestMemoryUpdateInfinityBodyIsTheServerFallback(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	const unit = "44444444-4444-4444-4444-444444444444"

	for _, body := range []string{`{"text":1e999}`, `{"state":1e999}`, `{"entities":["a",1e999]}`} {
		rec := send(mux, http.MethodPatch, base+"/memories/"+unit, body)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s = %d, want 500: %s", body, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "Internal Server Error" {
			t.Fatalf("%s body = %q, want the plain fallback", body, got)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("%s content-type = %q", body, ct)
		}
	}
	// A merely malformed body keeps the framework's 422 item list.
	rec := send(mux, http.MethodPatch, base+"/memories/"+unit, `{"text":`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// unitIDOf reads the id member of a detail response. A history response is a
// bare array with no id of its own; the routes that normalize the identifier
// all resolve the same unit, so it reports the canonical expectation the caller
// compares against.
func unitIDOf(t *testing.T, rec *httptest.ResponseRecorder, unit string) any {
	t.Helper()
	if strings.HasPrefix(rec.Body.String(), "[") {
		return unit
	}
	return decodeObject(t, rec)["id"]
}

// The memory detail is a Python dict literal in the reference
// (engine/memories/pg/curation.py:398-438), so its members reach the wire in
// source order while Go would sort a map's keys. The 20 common members therefore
// have a fixed order, and the members an observation adds follow them.
//
// Falsifier: build the detail as a map again - or reorder two members - and the
// ordered key list below no longer matches.
func TestMemoryDetailKeyOrderMatchesTheReference(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	common := []string{
		"id", "text", "context", "date", "type", "mentioned_at", "occurred_start",
		"occurred_end", "entities", "document_id", "chunk_id", "tags", "metadata",
		"observation_scopes", "state", "invalidation_reason", "invalidated_at",
		"edited_at", "consolidated_at", "consolidation_failed_at",
	}
	for _, tc := range []struct {
		name string
		id   string
		want []string
	}{
		{
			name: "world fact",
			id:   "44444444-4444-4444-4444-444444444444",
			want: common,
		},
		{
			name: "observation appends history then its lineage",
			id:   "55555555-5555-5555-5555-555555555555",
			want: append(append([]string(nil), common...),
				"history", "source_memory_ids", "source_memories"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(mux, http.MethodGet, base+"/memories/"+tc.id, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("detail = %d: %s", rec.Code, rec.Body.String())
			}
			if got := objectKeys(t, rec.Body.Bytes()); !equalStrings(got, tc.want) {
				t.Fatalf("key order =\n  %v\nwant\n  %v", got, tc.want)
			}
		})
	}
}

// objectKeys reports the member names of a JSON object in the order they were
// written, which is what a client reading the bytes sees.
func objectKeys(t *testing.T, body []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("body is not a JSON object: %s", body)
	}
	keys := make([]string, 0, 20)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("key token = %v", tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("skip value of %q: %v", key, err)
		}
	}
	return keys
}

// equalStrings reports whether two string slices are element-wise equal.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

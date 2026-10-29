package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// memoryObservationsMux serves the family's routes over the given store.
func memoryObservationsMux(store storage.Store) *http.ServeMux { return boundMux(store) }

// The bank-wide clear and the recovery are the only two whole-bank observation
// writes with no bank-existence check: both answer for a bank nobody created
// rather than 404ing, because each is one SQL statement filtered by bank_id
// (engine/memory_engine.py:11643-11710 and :11785-11822 take no
// `_require_bank_exists`). The scopes listing is the opposite: it reads, so it
// does check (memory_engine.py:11771-11772), and so does the preview, which
// reads through it.
func TestObservationWritesWithoutBankCheckAnswerForAnUnknownBank(t *testing.T) {
	mux := boundMux(storage.NewMemoryStore("public"))
	base := "/v1/default/banks/never-created"

	rec := send(mux, http.MethodDelete, base+"/observations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var cleared DeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared.Success || cleared.DeletedCount != 0 || cleared.Message != "Cleared 0 observations" {
		t.Fatalf("clear = %+v", cleared)
	}

	rec = send(mux, http.MethodPost, base+"/consolidation/recover", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("recover = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var recovered RecoverConsolidationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.RetriedCount != 0 {
		t.Fatalf("retried_count = %d, want 0", recovered.RetriedCount)
	}

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, base + "/observations/scopes", ""},
		{http.MethodPost, base + "/consolidation-strategies/preview", `{"strategies":[]}`},
	} {
		rec := send(mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if want := `{"detail":"Bank 'never-created' not found"}`; rec.Body.String() != want {
			t.Fatalf("%s %s body = %s, want %s", tc.method, tc.path, rec.Body.String(), want)
		}
	}
}

// The consolidation strategy preview validates its body before it reaches the
// bank, so an unknown bank with a malformed body is answered with the model's
// items rather than the 404 the body check can never pre-empt.
func TestConsolidationPreviewRejectsTheBodyBeforeAnUnknownBank(t *testing.T) {
	mux := boundMux(storage.NewMemoryStore("public"))
	rec := send(mux, http.MethodPost,
		"/v1/default/banks/never-created/consolidation-strategies/preview", `{"strategies":[{"scopes":"x"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("preview = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if want := `"loc":["body","strategies",0,"scopes"]`; !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
}

// A consolidation submit deduplicates only against an outstanding *unscoped*
// pass: a body naming observation_scopes — even an empty list — is a targeted
// run the reference submits unconditionally (`dedupe = observation_scopes is
// None`, engine/memory_engine.py:22822). An explicit null is None there, so it
// is a full-bank submit and does deduplicate; the two must not be conflated.
func TestConsolidationSubmitDedupesOnlyWithoutObservationScopes(t *testing.T) {
	cases := []struct {
		body        string
		wantFirst   bool
		wantRepeat  bool
		description string
	}{
		{"{}", false, true, "a body without the member is a full-bank submit"},
		{"", false, true, "an absent body is a full-bank submit"},
		// A body that is not an object is refused by the model, so no submit happens.
		{"null", false, true, "the JSON literal null body is the reference's None request"},
		{`{"observation_scopes":null}`, false, true, "an explicit null is None, so it deduplicates"},
		{`{"x":1,"observation_scopes":null}`, false, true, "a member before it does not change that"},
		{`{"observation_scopes":[]}`, false, false, "an empty list is still a scoped, undeduplicated run"},
		{`{"observation_scopes":[["team:core"]]}`, false, false, "a named list is scoped"},
		{`{"bogus":1}`, false, true, "an unknown member does not make the submit scoped"},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			// A fresh store per case keeps one case's pending pass out of the
			// next one's dedup window.
			store := storage.NewMemoryStore("public")
			if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			mux := memoryObservationsMux(store)
			submit := func(body string) (bool, string) {
				t.Helper()
				rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/consolidate", body)
				if rec.Code != http.StatusOK {
					t.Fatalf("consolidate %q = %d (body %s)", body, rec.Code, rec.Body.String())
				}
				var response ConsolidationResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				return response.Deduplicated, response.OperationID
			}
			firstDeduped, firstID := submit(tc.body)
			if firstDeduped != tc.wantFirst {
				t.Fatalf("first submit deduplicated = %v, want %v", firstDeduped, tc.wantFirst)
			}
			repeatDeduped, repeatID := submit(tc.body)
			if repeatDeduped != tc.wantRepeat {
				t.Fatalf("repeat submit deduplicated = %v, want %v", repeatDeduped, tc.wantRepeat)
			}
			if tc.wantRepeat && repeatID != firstID {
				t.Fatalf("deduplicated submit reported %s, want the pending %s", repeatID, firstID)
			}
		})
	}
}

// The scoped flag is what decides deduplication, and the two bodies that differ
// by it must submit differently through the same handlers: a JSON null decodes
// to a nil slice exactly as an absent member does, so the handler has to read
// the member itself to tell them apart. The reference's test is
// `observation_scopes is None` on the model value (memory_engine.py:22822), so
// `{"observation_scopes": null}` is a full-bank submit and deduplicates, while
// `{"observation_scopes": []}` is a scoped one and never does.
//
// Asserted on the store rather than on a second HTTP request: the reference's
// follow-up submit of an unscoped pass deduplicates against *anything* pending
// for the bank, so a repeated HTTP submit would also fold into an unrelated
// scoped pass and the comparison would prove nothing.
func TestConsolidationScopeFlagDecidesDeduplication(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		scoped bool
	}{
		{"body without the member", `{}`, false},
		{"absent body", "", false},
		{"JSON null body", "null", false},
		{"explicit null member", `{"observation_scopes": null}`, false},
		{"null member after another member", `{"x":1,"observation_scopes":null}`, false},
		{"unknown member", `{"bogus":1}`, false},
		{"empty list", `{"observation_scopes":[]}`, true},
		{"named list", `{"observation_scopes":[["team:core"]]}`, true},
	}
	for _, tc := range cases {
		CASE := tc
		t.Run(CASE.name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			ctx := context.Background()
			if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			// A full-bank pass is queued first: a scoped submit must leave it
			// alone and get an operation of its own, an unscoped one must fold
			// into it (dedupe = observation_scopes is None).
			pending, _, err := store.EnqueueConsolidation(ctx, bank)
			if err != nil {
				t.Fatal(err)
			}
			rec := send(memoryObservationsMux(store), http.MethodPost,
				"/v1/default/banks/"+bank+"/consolidate", CASE.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("consolidate %q = %d (body %s)", CASE.body, rec.Code, rec.Body.String())
			}
			var response ConsolidationResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if CASE.scoped && response.OperationID == pending.OperationID {
				t.Fatalf("scoped submit %q was deduplicated into the pending full-bank pass", CASE.body)
			}
			if !CASE.scoped && response.OperationID != pending.OperationID {
				t.Fatalf("full-bank submit %q created %s instead of folding into %s",
					CASE.body, response.OperationID, pending.OperationID)
			}
			if response.Deduplicated != !CASE.scoped {
				t.Fatalf("deduplicated = %v, want %v", response.Deduplicated, !CASE.scoped)
			}
		})
	}
}

// The bank-wide clear deletes the observations, drops their history and resets
// the consolidated marker on every source fact — and requeues nothing else, so
// the next pass re-consolidates them (engine/memories/pg/engine_curation.py:167-188
// and memory_engine.py:11683-11701).
func TestDeleteObservationsClearsSourcesAndHistory(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := boundMux(store)

	const sourceID = "11111111-1111-1111-1111-111111111111"
	const observationID = "22222222-2222-2222-2222-222222222222"
	source := storage.MemoryUnit{
		ID: sourceID, BankID: bank, Text: "Dan deploys on Fridays",
		FactType: storage.TypeWorld, Tags: []string{"team:core"},
	}
	if _, err := store.UpsertUnit(ctx, source); err != nil {
		t.Fatal(err)
	}
	observation := storage.MemoryUnit{
		ID: observationID, BankID: bank, Text: "Dan deploys on Fridays and enjoys it",
		FactType: storage.TypeObservation, Tags: []string{"team:core"},
		SourceMemoryIDs: []string{sourceID},
	}
	if _, err := store.UpsertUnit(ctx, observation); err != nil {
		t.Fatal(err)
	}
	// A consolidated marker on the source, so its reset is observable.
	units, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: bank, Limit: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(units.Memories) != 2 {
		t.Fatalf("seeded %d units, want 2", len(units.Memories))
	}
	if err := store.MarkConsolidated(ctx, bank, []string{sourceID}); err != nil {
		t.Fatalf("mark consolidated: %v", err)
	}

	rec := send(mux, http.MethodDelete, "/v1/default/banks/"+bank+"/observations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d (body %s)", rec.Code, rec.Body.String())
	}
	var response DeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.DeletedCount != 1 || response.Message != "Cleared 1 observations" || !response.Success {
		t.Fatalf("clear = %+v", response)
	}
	remaining, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: bank, Limit: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Memories) != 1 || remaining.Memories[0].ID != sourceID {
		t.Fatalf("units after clear = %+v, want only the source fact", remaining.Memories)
	}
	if remaining.Memories[0].ConsolidatedAt != nil {
		t.Fatalf("source consolidated_at = %v, want nil (requeued)", remaining.Memories[0].ConsolidatedAt)
	}
}

// An explicit /consolidate starts a chain that keeps draining past the
// auto-consolidation gate: the operation's task payload carries
// caller_requested on both the unscoped and the scoped submit
// (api/http.py:9992, memory_engine.py:23716-23717), and the worker's pass
// reads the flag from there (memory_engine.py:4697).
func TestConsolidateCarriesCallerRequested(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := boundMux(store)
	if _, err := store.CreateBank(t.Context(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}

	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/consolidate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("consolidate = %d (body %s)", rec.Code, rec.Body.String())
	}
	page, err := store.ListOperations(t.Context(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 1 {
		t.Fatalf("queued %d operations", len(page.Operations))
	}
	if page.Operations[0].Payload["caller_requested"] != true {
		t.Fatalf("unscoped payload = %v, want caller_requested", page.Operations[0].Payload)
	}

	rec = send(mux, http.MethodPost, "/v1/default/banks/b1/consolidate", `{"observation_scopes":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped consolidate = %d (body %s)", rec.Code, rec.Body.String())
	}
	page, err = store.ListOperations(t.Context(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 2 {
		t.Fatalf("queued %d operations after the scoped submit", len(page.Operations))
	}
	var scoped *storage.Operation
	for index := range page.Operations {
		if _, ok := page.Operations[index].Payload["observation_scopes"]; ok {
			scoped = &page.Operations[index]
		}
	}
	if scoped == nil {
		t.Fatalf("no scoped operation queued: %v", page.Operations)
	}
	if scoped.Payload["caller_requested"] != true {
		t.Fatalf("scoped payload = %v, want caller_requested", scoped.Payload)
	}
}

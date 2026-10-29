package mentalmodels

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// fullRefreshCronFixtures exercises trigger.full_refresh_cron: the member is
// accepted on create and patch, validated exactly like refresh_cron (including
// the message naming the member it refused), read back stripped, and — unlike
// refresh_cron — not mutually exclusive with refresh_after_consolidation.
func TestFullRefreshCronRoundTrips(t *testing.T) {
	mux := newTriggerServer(t)
	// A delta model on refresh-after-consolidation may also carry a full-refresh
	// schedule: the two schedules are independent, so this is not the
	// exclusivity rule's case.
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"mode":"delta","refresh_after_consolidation":true,`+
			`"full_refresh_cron":"  0 3 * * 0  "}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d (%s)", rec.Code, rec.Body.String())
	}
	model := getModel(t, mux, "mm-1")
	if cron, _ := model.Trigger.Value("full_refresh_cron"); cron != "0 3 * * 0" {
		t.Fatalf("full_refresh_cron = %#v, want the stripped expression", cron)
	}
	// The reflex trigger is untouched by the second schedule.
	if after, _ := model.Trigger.Value("refresh_after_consolidation"); after != true {
		t.Fatalf("refresh_after_consolidation = %#v, want true", after)
	}
	// A whitespace-only expression is the reference's null, not a schedule.
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"full_refresh_cron":"  "}}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d (%s)", rec.Code, rec.Body.String())
	}
	if cron, present := getModel(t, mux, "mm-1").Trigger.Value("full_refresh_cron"); !present || cron != nil {
		t.Fatalf("full_refresh_cron = %#v (present %v), want null", cron, present)
	}
}

// An invalid expression is the same 422 item refresh_cron gets, with the message
// naming full_refresh_cron — one shared field validator in the reference.
func TestFullRefreshCronValidation(t *testing.T) {
	mux := newTriggerServer(t)
	cases := []struct {
		name   string
		body   string
		loc    string
		detail string
	}{
		{
			name:   "invalid expression",
			body:   `{"trigger":{"full_refresh_cron":"every sunday"}}`,
			loc:    "body.trigger.full_refresh_cron",
			detail: "Value error, full_refresh_cron is not a valid cron expression: 'every sunday'",
		},
		{
			name: "wrong type",
			body: `{"trigger":{"full_refresh_cron":5}}`,
			loc:  "body.trigger.full_refresh_cron",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := triggerDetail(t, mux, tc.body)
			if len(detail) != 1 {
				t.Fatalf("detail = %+v, want one item", detail)
			}
			if got := locString(detail[0].Loc); got != tc.loc {
				t.Fatalf("loc = %q, want %q", got, tc.loc)
			}
			if tc.detail != "" && detail[0].Msg != tc.detail {
				t.Fatalf("msg = %q, want %q", detail[0].Msg, tc.detail)
			}
		})
	}
	// A trigger whose only schedule is the full one is a valid trigger: the
	// exclusivity rule names refresh_cron alone.
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"refresh_after_consolidation":true,"full_refresh_cron":"*/5 * * * *"}}`); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d (%s)", rec.Code, rec.Body.String())
	}
}

// getModel reads one model through the served route.
func getModel(t *testing.T, mux *http.ServeMux, id string) Model {
	t.Helper()
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/mental-models/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get %s: %d (%s)", id, rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	return model
}

// last_refreshed_at and last_full_refreshed_at are two clocks: only a full run
// moves the second. Both reach the wire, null before any has run.
func TestModelReportsBothRefreshClocks(t *testing.T) {
	mux := newTriggerServer(t)
	model := getModel(t, mux, "mm-1")
	if model.LastRefreshedAt == nil {
		t.Fatal("last_refreshed_at must be stamped on create")
	}
	if model.LastFullRefreshedAt != nil {
		t.Fatalf("last_full_refreshed_at = %#v, want null before a full refresh", *model.LastFullRefreshedAt)
	}

	stamp := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	mux2, store2 := newServer(t)
	if rec := send(mux2, http.MethodPost, "/v1/default/banks/b1/mental-models",
		`{"id":"mm-2","name":"m","source_query":"q"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d", rec.Code)
	}
	if err := store2.MarkMentalModelFullRefresh(context.Background(), "b1", "mm-2", stamp); err != nil {
		t.Fatal(err)
	}
	fetched := getModel(t, mux2, "mm-2")
	if fetched.LastFullRefreshedAt == nil || *fetched.LastFullRefreshedAt != "2026-10-01T03:00:00+00:00" {
		t.Fatalf("last_full_refreshed_at = %#v", fetched.LastFullRefreshedAt)
	}
}

// createPageModel seeds a mental model that a knowledge page points at, the way
// the knowledge routes do.
func createPageModel(t *testing.T, store *storage.MemoryStore, pageID, modelID string) string {
	t.Helper()
	if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
		ID: modelID, BankID: "b1", Subtype: storage.SubtypePinned, Name: pageID, SourceQuery: "page",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutKnowledgeNode(context.Background(), storage.KnowledgeNode{
		ID: pageID, BankID: "b1", Kind: storage.KnowledgeKindPage, Title: pageID, MentalModelID: modelID,
	}); err != nil {
		t.Fatal(err)
	}
	return modelID
}

// is_knowledge_page tells a page's backing model from a standalone one, and
// exclude_knowledge_pages leaves the page models out of both the items and the
// total.
func TestListExcludesKnowledgePages(t *testing.T) {
	mux, store := newServer(t)
	createPageModel(t, store, "page-1", "mm-page")
	if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
		ID: "mm-standalone", BankID: "b1", Subtype: storage.SubtypePinned, Name: "standalone", SourceQuery: "q",
	}); err != nil {
		t.Fatal(err)
	}

	all := listModels(t, mux, "")
	if all.Total != 2 {
		t.Fatalf("total = %d, want both models", all.Total)
	}
	flags := map[string]*bool{}
	for _, item := range all.Items {
		flags[item.ID] = item.IsKnowledgePage
	}
	if flags["mm-page"] == nil || !*flags["mm-page"] {
		t.Fatalf("mm-page is_knowledge_page = %v, want true", flags["mm-page"])
	}
	if flags["mm-standalone"] == nil || *flags["mm-standalone"] {
		t.Fatalf("mm-standalone is_knowledge_page = %v, want false", flags["mm-standalone"])
	}

	for _, value := range []string{"true", "1", "yes", "on", "TRUE"} {
		filtered := listModels(t, mux, "exclude_knowledge_pages="+value)
		if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].ID != "mm-standalone" {
			t.Fatalf("exclude_knowledge_pages=%s = %+v, want only the standalone model", value, filtered)
		}
	}
	if uncovered := listModels(t, mux, "exclude_knowledge_pages=false"); uncovered.Total != 2 {
		t.Fatalf("exclude_knowledge_pages=false total = %d, want both", uncovered.Total)
	}
	// A value outside the framework's lax boolean set is its 422, not a silent
	// false.
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/mental-models?exclude_knowledge_pages=maybe", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("exclude_knowledge_pages=maybe = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// listModels reads the list route with a query string.
func listModels(t *testing.T, mux *http.ServeMux, query string) ListResponse {
	t.Helper()
	path := "/v1/default/banks/b1/mental-models"
	if query != "" {
		path += "?" + query
	}
	rec := send(mux, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// The refresh route's optional body admits absent, null and {"mode":"full"}, and
// nothing else: an unknown mode is the request model's literal_error and a
// non-object body its model_attributes_type item.
func TestRefreshBodyDecoding(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantMode string
		wantLoc  string
	}{
		{name: "absent", body: "", wantCode: http.StatusOK},
		{name: "null", body: "null", wantCode: http.StatusOK},
		{name: "empty object", body: "{}", wantCode: http.StatusOK},
		{name: "full", body: `{"mode":"full"}`, wantCode: http.StatusOK, wantMode: "full"},
		{name: "explicit null mode", body: `{"mode":null}`, wantCode: http.StatusOK},
		{name: "unknown mode", body: `{"mode":"delta"}`, wantCode: http.StatusUnprocessableEntity, wantLoc: "body.mode"},
		{name: "wrong type", body: `{"mode":1}`, wantCode: http.StatusUnprocessableEntity, wantLoc: "body.mode"},
		{name: "not an object", body: `"full"`, wantCode: http.StatusUnprocessableEntity, wantLoc: "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, store := newServer(t)
			if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
				ID: "mm-1", BankID: "b1", Subtype: storage.SubtypePinned, Name: "m", SourceQuery: "q",
			}); err != nil {
				t.Fatal(err)
			}
			rec := send(mux, http.MethodPost, "/v1/default/banks/b1/mental-models/mm-1/refresh", tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("refresh = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantLoc != "" {
				detail := decodeValidationFailure(t, rec)
				if len(detail) == 0 || locString(detail[0].Loc) != tc.wantLoc {
					t.Fatalf("detail = %+v, want one item at %s", detail, tc.wantLoc)
				}
				return
			}
			var submitted SubmitResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil {
				t.Fatal(err)
			}
			if submitted.OperationID == "" || submitted.Status != "queued" {
				t.Fatalf("response = %+v, want the submit envelope", submitted)
			}
			queued := refreshPayload(t, store, submitted.OperationID)
			if got, _ := queued[storage.RefreshModePayloadKey].(string); got != tc.wantMode {
				t.Fatalf("queued mode = %q, want %q (payload %+v)", got, tc.wantMode, queued)
			}
			if queued[storage.MentalModelIDPayloadKey] != "mm-1" {
				t.Fatalf("payload = %+v, want the model id", queued)
			}
		})
	}
}

// refreshPayload reads a queued operation's payload out of the in-memory store.
func refreshPayload(t *testing.T, store *storage.MemoryStore, operationID string) map[string]any {
	t.Helper()
	op, err := store.GetOperation(context.Background(), "b1", operationID)
	if err != nil {
		t.Fatalf("read operation %s: %v", operationID, err)
	}
	return op.Payload
}

// Dedupe keeps one queued refresh per model, so a full submit that folds into a
// queued plain one must make that one full — otherwise its "full" is silently
// dropped. A plain submit folding into a queued full one leaves it full, and a
// fold into a *processing* refresh changes nothing: that run already chose its
// mode.
func TestFullRefreshUpgradesAQueuedRefresh(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
		ID: "mm-1", BankID: "b1", Subtype: storage.SubtypePinned, Name: "m", SourceQuery: "q",
	}); err != nil {
		t.Fatal(err)
	}
	submit := func(body string) string {
		t.Helper()
		rec := send(mux, http.MethodPost, "/v1/default/banks/b1/mental-models/mm-1/refresh", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("submit %q: %d (%s)", body, rec.Code, rec.Body.String())
		}
		var submitted SubmitResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil {
			t.Fatal(err)
		}
		return submitted.OperationID
	}

	plain := submit("")
	if _, ok := refreshPayload(t, store, plain)[storage.RefreshModePayloadKey]; ok {
		t.Fatal("a plain refresh must not carry a mode")
	}
	full := submit(`{"mode":"full"}`)
	if full != plain {
		t.Fatalf("the full submit queued a second refresh (%s vs %s), want the queued one", full, plain)
	}
	if got, _ := refreshPayload(t, store, plain)[storage.RefreshModePayloadKey].(string); got != "full" {
		t.Fatalf("queued mode = %q, want the fold to upgrade it to full", got)
	}
	// A plain submit folding into a queued full refresh leaves it full.
	if again := submit(""); again != plain {
		t.Fatalf("plain submit queued %s, want the queued full one %s", again, plain)
	}
	if got, _ := refreshPayload(t, store, plain)[storage.RefreshModePayloadKey].(string); got != "full" {
		t.Fatalf("queued mode = %q, want it left at full", got)
	}

	// An operation that is no longer pending is left alone: its mode is decided.
	if promoted, err := storage.PromoteQueuedRefreshToFull(context.Background(), store, "b1", plain); err != nil || promoted {
		t.Fatalf("promote of an already-full refresh = (%v, %v), want a no-op", promoted, err)
	}
}

package app

// End-to-end cases for the v1.1.0 mental-model surface: trigger.full_refresh_cron,
// the optional {"mode":"full"} refresh body, last_full_refreshed_at, and the
// knowledge-page flags on the list. Each runs through the composed application,
// so the route table, the body decoder, the handler and the store are exercised
// together — the same path a client takes.

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider/mock"
	"github.com/studyzy/wenyuan/internal/storage"
)

// mentalModelApp composes the served application with a model provider, because
// the mental-model routes refuse refresh work on a provider-"none" deployment
// (a create queues its first refresh, so it is a 500 there by design).
func mentalModelApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	a, err := New(context.Background(), cfg, Dependencies{
		Store:    storage.NewMemoryStore("public"),
		Provider: &mock.Provider{DimensionsValue: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// createMentalModel creates one model and returns its identifier.
func createMentalModel(t *testing.T, a *App, bankID, body string) map[string]any {
	t.Helper()
	rec := exchange(a, http.MethodPost, "/v1/default/banks/"+bankID+"/mental-models", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d (%s)", rec.Code, rec.Body.String())
	}
	return decodeJSON(t, rec)
}

// full_refresh_cron is carried by the trigger on every read route, and the
// model reports both refresh clocks.
func TestParityMentalModelFullRefreshCron(t *testing.T) {
	a := mentalModelApp(t)
	created := createMentalModel(t, a, "p1",
		`{"id":"mm-full","name":"weekly","source_query":"what changed",`+
			`"trigger":{"mode":"delta","refresh_after_consolidation":true,"full_refresh_cron":"0 3 * * 0"}}`)
	if created["mental_model_id"] != "mm-full" {
		t.Fatalf("create = %v", created)
	}

	rec := exchange(a, http.MethodGet, "/v1/default/banks/p1/mental-models/mm-full", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d (%s)", rec.Code, rec.Body.String())
	}
	model := decodeJSON(t, rec)
	if _, present := model["last_full_refreshed_at"]; !present {
		t.Fatalf("the read must carry last_full_refreshed_at: %v", model)
	}
	if model["last_full_refreshed_at"] != nil {
		t.Fatalf("last_full_refreshed_at = %v on a new model, want null", model["last_full_refreshed_at"])
	}
	trigger, _ := model["trigger"].(map[string]any)
	if trigger["full_refresh_cron"] != "0 3 * * 0" {
		t.Fatalf("trigger.full_refresh_cron = %#v, want the stored schedule", trigger["full_refresh_cron"])
	}
	if trigger["refresh_after_consolidation"] != true {
		t.Fatalf("the full schedule must not clear the reflex trigger: %v", trigger)
	}

	// The list carries it too.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/p1/mental-models?detail=content", "")
	list := decodeJSON(t, rec)
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list = %v", list)
	}
	item, _ := items[0].(map[string]any)
	if item["last_full_refreshed_at"] != nil {
		t.Fatalf("list last_full_refreshed_at = %v, want null", item["last_full_refreshed_at"])
	}

	// An invalid expression is the reference's 422 under the member's own name.
	rec = exchange(a, http.MethodPatch, "/v1/default/banks/p1/mental-models/mm-full",
		`{"trigger":{"full_refresh_cron":"not a cron"}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid full_refresh_cron = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// The refresh route accepts an optional {"mode":"full"} body and queues the
// model with that mode; the trigger is not changed by it.
func TestParityMentalModelRefreshModeBody(t *testing.T) {
	a := mentalModelApp(t)
	createMentalModel(t, a, "p1", `{"id":"mm-delta","name":"d","source_query":"q","trigger":{"mode":"delta"}}`)

	rec := exchange(a, http.MethodPost, "/v1/default/banks/p1/mental-models/mm-delta/refresh", `{"mode":"full"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["status"] != "queued" || body["operation_id"] == "" || body["operation_id"] == nil {
		t.Fatalf("refresh = %v, want the submit envelope", body)
	}
	operationID, _ := body["operation_id"].(string)
	op, err := a.Store.GetOperation(context.Background(), "p1", operationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Payload[storage.RefreshModePayloadKey] != "full" {
		t.Fatalf("queued payload = %+v, want the full mode override", op.Payload)
	}
	// The mode is a one-run override: the stored trigger keeps its delta mode.
	stored, err := a.Store.GetMentalModel(context.Background(), "p1", "mm-delta")
	if err != nil {
		t.Fatal(err)
	}
	if mode, _ := stored.Trigger["mode"].(string); mode != "delta" {
		t.Fatalf("stored trigger mode = %q, want delta left alone", mode)
	}

	// A body the model refuses is its literal_error, not a silent plain refresh.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/p1/mental-models/mm-delta/refresh", `{"mode":"delta"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mode=delta = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// is_knowledge_page tells a page's backing model from a standalone one, and
// exclude_knowledge_pages removes page models from both the items and the total.
func TestParityMentalModelKnowledgePageFlags(t *testing.T) {
	a := mentalModelApp(t)
	createMentalModel(t, a, "p1", `{"id":"mm-standalone","name":"note","source_query":"n"}`)
	// A knowledge page keeps its content in a mental model the page route mints;
	// that model is what is_knowledge_page flags.
	rec := exchange(a, http.MethodPost, "/v1/default/banks/p1/knowledge-base/pages",
		`{"name":"page","source_query":"p"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create page: %d (%s)", rec.Code, rec.Body.String())
	}
	pageModelID, _ := decodeJSON(t, rec)["mental_model_id"].(string)
	if pageModelID == "" {
		t.Fatalf("page create = %s, want the backing model id", rec.Body.String())
	}

	rec = exchange(a, http.MethodGet, "/v1/default/banks/p1/mental-models", "")
	list := decodeJSON(t, rec)
	if list["total"] != float64(2) {
		t.Fatalf("total = %v, want both models: %v", list["total"], list)
	}
	flags := map[string]any{}
	for _, entry := range list["items"].([]any) {
		item, _ := entry.(map[string]any)
		id, _ := item["id"].(string)
		flags[id] = item["is_knowledge_page"]
	}
	if flags[pageModelID] != true {
		t.Fatalf("the page's model is_knowledge_page = %#v, want true", flags[pageModelID])
	}
	if flags["mm-standalone"] != false {
		t.Fatalf("mm-standalone is_knowledge_page = %#v, want false", flags["mm-standalone"])
	}

	rec = exchange(a, http.MethodGet, "/v1/default/banks/p1/mental-models?exclude_knowledge_pages=true", "")
	filtered := decodeJSON(t, rec)
	items, _ := filtered["items"].([]any)
	if filtered["total"] != float64(1) || len(items) != 1 {
		t.Fatalf("exclude_knowledge_pages list = %v, want only the standalone model", filtered)
	}
	only, _ := items[0].(map[string]any)
	if only["id"] != "mm-standalone" {
		t.Fatalf("excluded list kept %v", only["id"])
	}

	// is_knowledge_page is a per-model fact on the single read too.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/p1/mental-models/"+pageModelID, "")
	if page := decodeJSON(t, rec); page["is_knowledge_page"] != true {
		t.Fatalf("get is_knowledge_page = %#v, want true", page["is_knowledge_page"])
	}
}

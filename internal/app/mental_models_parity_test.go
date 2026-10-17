package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider/mock"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newParityApp composes an application over an in-memory store with one bank.
func newParityApp(t *testing.T) *App {
	t.Helper()
	app, err := New(context.Background(), func() config.Config {
		cfg := testConfig()
		cfg.EnableBankLLMHealth = true
		return cfg
	}(), Dependencies{
		Store: storage.NewMemoryStore("public"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "mm-bank", Name: "mm-bank"}); err != nil {
		t.Fatal(err)
	}
	return app
}

// newParityAppWithProvider builds the same app with the deterministic mock
// provider injected, so the mental-model create route is not gated by the
// no-provider refusal and its response contract can be exercised.
func newParityAppWithProvider(t *testing.T) *App {
	t.Helper()
	app, err := New(context.Background(), func() config.Config {
		cfg := testConfig()
		cfg.EnableBankLLMHealth = true
		return cfg
	}(), Dependencies{
		Store:    storage.NewMemoryStore("public"),
		Provider: &mock.Provider{DimensionsValue: storage.EmbeddingDimensions},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "mm-bank", Name: "mm-bank"}); err != nil {
		t.Fatal(err)
	}
	return app
}

// send issues one request through the served handler.
func send(app *App, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)
	return rec
}

// test-mental-model-lifecycle: the mental-model family supports create, read,
// list, update, history, clear, refresh, and delete through the served handler.
func TestMentalModelLifecycle(t *testing.T) {
	app := newParityApp(t)
	base := "/v1/default/banks/mm-bank/mental-models"

	// The refresh runs as a queued operation, so the test drains it with the
	// same processor a worker would use.
	processor := archiveProcessor{store: app.Store}

	// The refresh reads the bank, so it needs something in scope to read: a
	// bank with nothing to reflect over preserves the document and records no
	// history.
	if mem, ok := app.Store.(*storage.MemoryStore); ok {
		mem.SeedMemory("mm-bank", storage.RecallMatch{ID: "mm-fact", Text: "deployment happens on tuesday", Type: "world"})
	}

	// This deployment has no LLM provider, so the reference refuses the refresh a
	// create needs — without catching the refusal, so the route answers the
	// framework's internal-error body — and stores the model regardless. The
	// family's remaining routes are exercised against that stored model.
	rec := send(app, http.MethodPost, base,
		`{"subtype":"pinned","name":"deploy days","description":"when deployments happen","source_query":"deployment"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("create without a model = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	// The model the create stored is the one the rest of the lifecycle uses.
	rec = send(app, http.MethodGet, base, "")
	var stored struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("list after a refused create: %v (%s)", err, rec.Body.String())
	}
	if len(stored.Items) != 1 {
		t.Fatalf("the create must have stored its model: %v", stored.Items)
	}
	id, _ := stored.Items[0]["id"].(string)
	if id == "" {
		t.Fatalf("stored model carries no id: %v", stored.Items[0])
	}
	// A refresh operation is queued through the store, standing in for the one a
	// deployment with a model would have queued at create time.
	operationID := "11111111-1111-1111-1111-111111111111"
	if _, err := app.Store.PutOperation(context.Background(), storage.Operation{
		OperationID: operationID, BankID: "mm-bank",
		OperationType: storage.OperationTypeRefreshMentalModel, Status: storage.StatusPending,
		Payload: map[string]any{storage.MentalModelIDPayloadKey: id},
	}); err != nil {
		t.Fatalf("queue the refresh: %v", err)
	}

	// Read.
	rec = send(app, http.MethodGet, base+"/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}

	// List, narrowed by subtype.
	rec = send(app, http.MethodGet, base+"?subtype=pinned", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("list returned %d models, want 1", len(listed.Items))
	}

	// A subtype filter with no match returns an empty list rather than an error.
	rec = send(app, http.MethodGet, base+"?subtype=learned", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list = %d", rec.Code)
	}

	// Update one field without restating the rest.
	rec = send(app, http.MethodPatch, base+"/"+id, `{"name":"renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d (body %s)", rec.Code, rec.Body.String())
	}

	// History starts empty but is addressable.
	rec = send(app, http.MethodGet, base+"/"+id+"/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d", rec.Code)
	}

	// A dry run also needs a model, so it is refused with the same message (the
	// reference resolves the model first, then checks availability, so the route
	// answers 400 here).
	rec = send(app, http.MethodPost, base+"/"+id+"/dry-run-refresh", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("dry run without a model = %d (body %s)", rec.Code, rec.Body.String())
	}
	var dry map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dry); err != nil {
		t.Fatal(err)
	}
	if dry["detail"] == nil {
		t.Fatalf("a refused dry run must carry the refusal detail: %v", dry)
	}

	// An explicit refresh asks for work that only a model can do, and this
	// deployment has no LLM provider, so the reference refuses it outright
	// instead of queueing it.
	rec = send(app, http.MethodPost, base+"/"+id+"/refresh", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh without a model = %d (body %s)", rec.Code, rec.Body.String())
	}
	// Nothing has been recorded yet: the operation has not run.
	rec = send(app, http.MethodGet, base+"/"+id+"/history", "")
	var history []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatalf("history must be a bare array: %s", rec.Body.String())
	}
	if len(history) != 0 {
		t.Fatalf("history has %d entries before the operation ran, want 0", len(history))
	}

	// Running the queued operation records the history entry.
	if err := processor.Process(context.Background(), storage.Operation{
		OperationID:   operationID,
		BankID:        "mm-bank",
		OperationType: storage.OperationTypeRefreshMentalModel,
		Payload:       map[string]any{storage.MentalModelIDPayloadKey: id},
	}); err != nil {
		t.Fatalf("run the queued refresh: %v", err)
	}
	rec = send(app, http.MethodGet, base+"/"+id+"/history", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d entries after the refresh ran, want 1", len(history))
	}
	if _, ok := history[0]["changed_at"]; !ok {
		t.Fatalf("history entry must carry changed_at: %v", history[0])
	}

	// Clear keeps the record but empties its content.
	rec = send(app, http.MethodPost, base+"/"+id+"/clear", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d", rec.Code)
	}
	rec = send(app, http.MethodGet, base+"/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear must keep the record: %d", rec.Code)
	}

	// Delete.
	rec = send(app, http.MethodDelete, base+"/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec = send(app, http.MethodGet, base+"/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a deleted mental model must be not-found, got %d", rec.Code)
	}
}

// test-mental-model-rejects-unknown-subtype: a subtype outside the accepted set
// is a caller error, not a stored record.
func TestMentalModelRejectsUnknownSubtype(t *testing.T) {
	app := newParityApp(t)
	rec := send(app, http.MethodPost, "/v1/default/banks/mm-bank/mental-models",
		`{"subtype":"nonsense","name":"x"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown subtype = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}

// test-mental-model-trigger-and-id: a create carries a validated trigger and an
// optional client-supplied id, both echoed, with a collision reported as 409.
func TestMentalModelTriggerAndID(t *testing.T) {
	app := newParityAppWithProvider(t)
	base := "/v1/default/banks/mm-bank/mental-models"

	rec := send(app, http.MethodPost, base,
		`{"id":"team-comms","name":"Team comms","source_query":"q","trigger":{"mode":"delta"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d (body %s)", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["mental_model_id"] != "team-comms" {
		t.Fatalf("client id not honored: %v", created)
	}
	// The trigger is echoed on read.
	rec = send(app, http.MethodGet, base+"/team-comms", "")
	var model map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	trigger, _ := model["trigger"].(map[string]any)
	if trigger["mode"] != "delta" {
		t.Fatalf("trigger not echoed: %v", model["trigger"])
	}
	// A duplicate id is a conflict.
	if rec := send(app, http.MethodPost, base, `{"id":"team-comms","name":"again","source_query":"q"}`); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate id = %d, want 409", rec.Code)
	}
	// An invalid trigger member is a structured 422.
	rec = send(app, http.MethodPost, base, `{"name":"x","trigger":{"mode":"sideways"}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid trigger = %d, want 422", rec.Code)
	}
	var failure struct {
		Detail []map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatalf("validation body = %s", rec.Body.String())
	}
	if len(failure.Detail) == 0 {
		t.Fatalf("validation detail empty: %s", rec.Body.String())
	}
}

// test-directive-lifecycle: the directive family supports create, read, list
// ordered by priority, update, and delete.
func TestDirectiveLifecycle(t *testing.T) {
	app := newParityApp(t)
	base := "/v1/default/banks/mm-bank/directives"

	low := send(app, http.MethodPost, base, `{"name":"low","content":"low priority rule","priority":1}`)
	if low.Code != http.StatusOK {
		t.Fatalf("create low = %d (body %s)", low.Code, low.Body.String())
	}
	high := send(app, http.MethodPost, base, `{"name":"high","content":"high priority rule","priority":9}`)
	if high.Code != http.StatusOK {
		t.Fatalf("create high = %d", high.Code)
	}
	var highBody map[string]any
	if err := json.Unmarshal(high.Body.Bytes(), &highBody); err != nil {
		t.Fatal(err)
	}
	highID, _ := highBody["id"].(string)

	// A directive is active by default.
	if highBody["is_active"] != true {
		t.Fatalf("a directive must default to active: %v", highBody)
	}

	// The list is ordered by priority, so the reflection loop's order is the
	// order the API returns.
	rec := send(app, http.MethodGet, base, "")
	var listed struct {
		Directives []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Directives) != 2 {
		t.Fatalf("list returned %d directives, want 2", len(listed.Directives))
	}
	if listed.Directives[0]["name"] != "high" {
		t.Fatalf("directives must be ordered by priority, got %v", listed.Directives)
	}

	// Read one.
	rec = send(app, http.MethodGet, base+"/"+highID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}

	// Deactivate without restating the rest.
	rec = send(app, http.MethodPatch, base+"/"+highID, `{"is_active":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d (body %s)", rec.Code, rec.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["is_active"] != false {
		t.Fatalf("update did not deactivate: %v", updated)
	}

	// Delete, and a repeated delete is not-found.
	rec = send(app, http.MethodDelete, base+"/"+highID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec = send(app, http.MethodDelete, base+"/"+highID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("repeated delete = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// test-directive-requires-content: a directive without content is a caller
// error, reported as a structured 422.
func TestDirectiveRequiresContent(t *testing.T) {
	app := newParityApp(t)
	rec := send(app, http.MethodPost, "/v1/default/banks/mm-bank/directives", `{"name":"x"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing content = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}

// test-families-reject-unknown-bank: a read reports not-found for a bank the
// store does not hold, while a mental-model create lazily creates it.
func TestFamiliesRejectUnknownBank(t *testing.T) {
	app := newParityAppWithProvider(t)
	for _, path := range []string{
		"/v1/default/banks/ghost/mental-models",
		"/v1/default/banks/ghost/directives",
	} {
		rec := send(app, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
	// A create on an unknown bank succeeds, creating the bank.
	rec := send(app, http.MethodPost, "/v1/default/banks/ghost/mental-models", `{"name":"x","source_query":"q"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST mental-models on unknown bank = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := app.Store.GetBank(context.Background(), "ghost"); err != nil {
		t.Fatalf("bank was not created: %v", err)
	}
}

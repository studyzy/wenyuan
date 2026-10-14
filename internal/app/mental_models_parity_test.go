package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newParityApp composes an application over an in-memory store with one bank.
func newParityApp(t *testing.T) *App {
	t.Helper()
	app, err := New(context.Background(), config.FromEnv(func(string) (string, bool) { return "", false }), Dependencies{
		Store: storage.NewMemoryStore("public"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{ID: "mm-bank", Name: "mm-bank"}); err != nil {
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

	// Create.
	rec := send(app, http.MethodPost, base,
		`{"subtype":"pinned","name":"deploy days","description":"when deployments happen","source_query":"deployment"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (body %s)", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create must return an id: %v", created)
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
		Models []map[string]any `json:"mental_models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("list returned %d models, want 1", len(listed.Models))
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

	// A dry run reports without applying.
	rec = send(app, http.MethodPost, base+"/"+id+"/dry-run-refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d", rec.Code)
	}
	var dry map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dry); err != nil {
		t.Fatal(err)
	}
	if dry["dry_run"] != true {
		t.Fatalf("dry run must report itself: %v", dry)
	}

	// A real refresh queues the work rather than performing it in the request,
	// because it regenerates the model's content through a model call. The
	// request reports the accepted operation, and the caller polls that.
	rec = send(app, http.MethodPost, base+"/"+id+"/refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d (body %s)", rec.Code, rec.Body.String())
	}
	var queued map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	if queued["queued"] != true {
		t.Fatalf("refresh must report the work as queued: %v", queued)
	}
	operationID, _ := queued["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("refresh must name the queued operation: %v", queued)
	}
	// Nothing has been recorded yet: the operation has not run.
	rec = send(app, http.MethodGet, base+"/"+id+"/history", "")
	var history struct {
		Entries []map[string]any `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Entries) != 0 {
		t.Fatalf("history has %d entries before the operation ran, want 0", len(history.Entries))
	}

	// Running the queued operation records the history entry.
	if err := processor.Process(context.Background(), storage.Operation{
		ID:            operationID,
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
	if len(history.Entries) != 1 {
		t.Fatalf("history has %d entries after the refresh ran, want 1", len(history.Entries))
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
	if rec.Code != http.StatusNoContent {
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown subtype = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// test-directive-lifecycle: the directive family supports create, read, list
// ordered by priority, update, and delete.
func TestDirectiveLifecycle(t *testing.T) {
	app := newParityApp(t)
	base := "/v1/default/banks/mm-bank/directives"

	low := send(app, http.MethodPost, base, `{"name":"low","content":"low priority rule","priority":1}`)
	if low.Code != http.StatusCreated {
		t.Fatalf("create low = %d (body %s)", low.Code, low.Body.String())
	}
	high := send(app, http.MethodPost, base, `{"name":"high","content":"high priority rule","priority":9}`)
	if high.Code != http.StatusCreated {
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
		Directives []map[string]any `json:"directives"`
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
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec = send(app, http.MethodDelete, base+"/"+highID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("repeated delete = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// test-directive-requires-content: a directive without content is a caller
// error, because a nameless rule cannot be followed.
func TestDirectiveRequiresContent(t *testing.T) {
	app := newParityApp(t)
	rec := send(app, http.MethodPost, "/v1/default/banks/mm-bank/directives", `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing content = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// test-families-reject-unknown-bank: both families report not-found for a bank
// the store does not hold, rather than creating anything.
func TestFamiliesRejectUnknownBank(t *testing.T) {
	app := newParityApp(t)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/default/banks/ghost/mental-models"},
		{http.MethodPost, "/v1/default/banks/ghost/mental-models"},
		{http.MethodGet, "/v1/default/banks/ghost/directives"},
		{http.MethodPost, "/v1/default/banks/ghost/directives"},
	}
	for _, tc := range cases {
		rec := send(app, tc.method, tc.path, `{"name":"x","content":"y"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, http.StatusNotFound)
		}
	}
}

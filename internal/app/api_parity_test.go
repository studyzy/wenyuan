package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedParityOps seeds two banks: one with a terminal and a live operation, plus
// a couple of memory units of different types, so the delete and clear routes
// have something to act on.
func seedParityOps(t *testing.T, app *App) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.Store.CreateBank(ctx, storage.Bank{BankID: "api-bank", Name: "api-bank"}); err != nil {
		t.Fatal(err)
	}
	// A completed operation is removable; a pending one is not.
	for _, op := range []storage.Operation{
		{OperationID: "11111111-1111-1111-1111-111111111111", BankID: "api-bank",
			OperationType: storage.OperationTypeRetain, Status: storage.StatusCompleted},
		{OperationID: "22222222-2222-2222-2222-222222222222", BankID: "api-bank",
			OperationType: storage.OperationTypeRetain, Status: storage.StatusPending},
	} {
		if _, err := app.Store.PutOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "api-world-1", BankID: "api-bank", Text: "the saga service owns billing",
			FactType: storage.TypeWorld},
		{ID: "api-obs-1", BankID: "api-bank", Text: "billing deploys on Fridays",
			FactType: storage.TypeObservation},
	} {
		if _, err := app.Store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
}

func parityOpsApp(t *testing.T) *App {
	t.Helper()
	app := newParityApp(t)
	seedParityOps(t, app)
	return app
}

// test-delete-operation-contract: the reference removes only terminal
// operations, rejecting a malformed identifier with 400, an unknown one with
// 404, and a still-live one with 409.
func TestDeleteOperationContract(t *testing.T) {
	app := parityOpsApp(t)
	base := "/v1/default/banks/api-bank/operations"

	// A malformed identifier is a bad request, not a not-found.
	if rec := send(app, http.MethodDelete, base+"/not-a-uuid/delete", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// A live operation cannot be deleted.
	if rec := send(app, http.MethodDelete, base+"/22222222-2222-2222-2222-222222222222/delete", ""); rec.Code != http.StatusConflict {
		t.Fatalf("live operation = %d, want %d (body %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}

	// An unknown identifier is not found.
	if rec := send(app, http.MethodDelete, base+"/33333333-3333-3333-3333-333333333333/delete", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	// A terminal operation is removed, and the answer names it.
	rec := send(app, http.MethodDelete, base+"/11111111-1111-1111-1111-111111111111/delete", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("terminal operation = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["success"] != true || body["operation_id"] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("delete response = %v", body)
	}

	// It is gone afterwards: the single read reports not_found in-band (200),
	// matching the reference rather than a 404.
	rec = send(app, http.MethodGet, base+"/11111111-1111-1111-1111-111111111111", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deleted operation read = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var gone map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &gone); err != nil {
		t.Fatal(err)
	}
	if gone["status"] != "not_found" {
		t.Fatalf("deleted operation status = %v, want not_found", gone["status"])
	}

	// The member path is a DELETE; the old POST form is not routable.
	if rec := send(app, http.MethodPost, base+"/11111111-1111-1111-1111-111111111111/delete", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST member delete = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// test-clear-memories-contract: the clear route removes memory units and
// reports the count, leaves the bank standing, and honours the type filter.
func TestClearMemoriesContract(t *testing.T) {
	app := parityOpsApp(t)
	base := "/v1/default/banks/api-bank/memories"

	// Clearing only the observations leaves the world fact in place.
	rec := send(app, http.MethodDelete, base+"?type=observation", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear observations = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var scoped struct {
		Success      bool   `json:"success"`
		Message      string `json:"message"`
		DeletedCount int    `json:"deleted_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &scoped); err != nil {
		t.Fatal(err)
	}
	if scoped.DeletedCount != 1 || !scoped.Success {
		t.Fatalf("scoped clear = %v", scoped)
	}

	// The bank still exists, and the untouched fact is still readable.
	rec = send(app, http.MethodGet, "/v1/default/banks", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "api-bank") {
		t.Fatalf("bank must survive a clear: %d (body %s)", rec.Code, rec.Body.String())
	}
	rec = send(app, http.MethodGet, base+"/api-world-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unrelated fact must survive a scoped clear: %d (body %s)", rec.Code, rec.Body.String())
	}

	// Clearing everything removes the rest.
	rec = send(app, http.MethodDelete, base, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear all = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var all struct {
		DeletedCount int `json:"deleted_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if all.DeletedCount != 1 {
		t.Fatalf("clear all removed %d, want 1", all.DeletedCount)
	}
}

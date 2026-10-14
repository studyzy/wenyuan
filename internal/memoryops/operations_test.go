package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

const validOpID = "11111111-1111-1111-1111-111111111111"
const validOpID2 = "22222222-2222-2222-2222-222222222222"

func do(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ts-0002 / st-0001 / st-0002: an async write is accepted pending, then moves
// through processing to a terminal status with an error message.
func TestAsyncWriteLifecycle(t *testing.T) {
	mux, store := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"async"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	opID := decodeOpID(t, rec)

	poll := func() map[string]any {
		r := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+opID)
		var out map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if poll()["status"] != "pending" {
		t.Fatalf("initial status = %v, want pending", poll()["status"])
	}
	if _, err := store.AdvanceOperation(context.TODO(), opID, storage.StatusProcessing, ""); err != nil {
		t.Fatal(err)
	}
	if poll()["status"] != "processing" {
		t.Fatalf("status = %v, want processing", poll()["status"])
	}
	if _, err := store.AdvanceOperation(context.TODO(), opID, storage.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if got := poll(); got["status"] != "failed" || got["error_message"] != "boom" {
		t.Fatalf("terminal status = %+v", got)
	}
}

// ts-0003 / out-0003: listing supports status and type filters and paging.
func TestListOperationsFiltersAndPaging(t *testing.T) {
	mux, store := newServer(t)
	seedOp(t, store, validOpID, storage.StatusCompleted)
	seedOp(t, store, validOpID2, storage.StatusFailed)
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations?status=failed&type=retain&limit=10&offset=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		BankID     string           `json:"bank_id"`
		Total      int              `json:"total"`
		Limit      int              `json:"limit"`
		Offset     int              `json:"offset"`
		Operations []map[string]any `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.BankID != "b1" || got.Total != 1 || len(got.Operations) != 1 {
		t.Fatalf("filtered page = %+v", got)
	}
	first := got.Operations[0]
	for _, field := range []string{"id", "operation_type", "task_type", "status", "created_at", "updated_at"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("operation object missing %q: %+v", field, first)
		}
	}
	if got.Limit != 10 || got.Offset != 0 {
		t.Fatalf("applied limit/offset = %d/%d", got.Limit, got.Offset)
	}
}

// ts-0006 / st-0003 / err-0001: cancelling a pending operation succeeds, while a
// terminal one returns the conflict status.
func TestCancelPendingThenTerminal(t *testing.T) {
	mux, store := newServer(t)
	seedOp(t, store, validOpID, storage.StatusPending)
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/"+validOpID)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var action struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if !action.Success || action.Message == "" || action.OperationID != validOpID {
		t.Fatalf("cancel response = %+v", action)
	}
	// Cancel again: the operation is now terminal.
	if again := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/"+validOpID); again.Code != http.StatusConflict {
		t.Fatalf("re-cancel status = %d, want 409", again.Code)
	}
}

// ts-0007 / st-0002: retrying a failed operation requeues it.
func TestRetryFailedOperation(t *testing.T) {
	mux, store := newServer(t)
	seedOp(t, store, validOpID, storage.StatusFailed)
	rec := do(t, mux, http.MethodPost, "/v1/default/banks/b1/operations/"+validOpID+"/retry")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var action struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if !action.Success || action.Message == "" {
		t.Fatalf("retry response = %+v", action)
	}
}

// ts-0020 / err-0006 / cmp-0007: an unknown identifier is 404; a malformed one
// is a client error.
func TestUnknownAndMalformedOperationID(t *testing.T) {
	mux, _ := newServer(t)
	if rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", rec.Code)
	}
	if rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/not-a-uuid"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed id status = %d, want 422", rec.Code)
	}
}

// ts-0021 / ts-0022: a fetch exposes the operation kind and status, and carries
// the raw payload when the include switch is set.
func TestFetchOperationIncludesPayload(t *testing.T) {
	mux, store := newServer(t)
	op, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: storage.OperationTypeRetain,
		Status: storage.StatusCompleted, Payload: map[string]any{"items": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationType != storage.OperationTypeRetain {
		t.Fatalf("operation type = %q", op.OperationType)
	}
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID+"?include=payload")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["payload"]; !ok {
		t.Fatalf("include switch must carry the raw payload: %s", rec.Body.String())
	}
	if got["operation_type"] != "retain" {
		t.Fatalf("operation_type = %v", got["operation_type"])
	}
}

// ts-0006: a not_found status value is distinct from failed.
func TestOperationStatusValues(t *testing.T) {
	for _, want := range []string{"pending", "processing", "completed", "failed", "cancelled", "not_found"} {
		if !containsStatus(want) {
			t.Fatalf("missing documented status value %q", want)
		}
	}
}

func containsStatus(value string) bool {
	for _, status := range []storage.OperationStatus{
		storage.StatusPending, storage.StatusProcessing, storage.StatusCompleted,
		storage.StatusFailed, storage.StatusCancelled, storage.StatusNotFound,
	} {
		if string(status) == value {
			return true
		}
	}
	return false
}

func seedOp(t *testing.T, store *storage.MemoryStore, id string, status storage.OperationStatus) {
	t.Helper()
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: id, BankID: "b1", OperationType: storage.OperationTypeRetain, Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	if status == storage.StatusPending {
		return
	}
	if status == storage.StatusProcessing || status == storage.StatusCompleted {
		if _, err := store.AdvanceOperation(context.TODO(), id, storage.StatusProcessing, ""); err != nil {
			t.Fatal(err)
		}
	}
	if status != storage.StatusProcessing {
		if _, err := store.AdvanceOperation(context.TODO(), id, status, ""); err != nil {
			t.Fatal(err)
		}
	}
}

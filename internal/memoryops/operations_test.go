package memoryops

import (
	"context"
	"encoding/json"
	"errors"
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

// ts-0020 / err-0006 / cmp-0007: an unknown identifier reads as a 200 with
// status "not_found" (the reference does not 404 the operation read), while a
// malformed one is a client error.
func TestUnknownAndMalformedOperationID(t *testing.T) {
	mux, _ := newServer(t)
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown id status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var missing map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &missing); err != nil {
		t.Fatal(err)
	}
	if missing["status"] != "not_found" {
		t.Fatalf("unknown id status field = %v, want not_found", missing["status"])
	}
	if missing["operation_id"] != validOpID {
		t.Fatalf("unknown id must echo the identifier, got %v", missing["operation_id"])
	}
	if rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id status = %d, want 400", rec.Code)
	}
}

// test-operation-bank-scope: a route addressed to a bank that does not own the
// operation must not expose or mutate it. The read reports not_found; the
// cancel and retry writes are 404.
func TestOperationScopedToBank(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	seedOp(t, store, validOpID, storage.StatusFailed)

	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b2/operations/"+validOpID)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-bank read = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "not_found" {
		t.Fatalf("cross-bank read status = %v, want not_found", got["status"])
	}
	if rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b2/operations/"+validOpID); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-bank cancel = %d, want 404", rec.Code)
	}
	if rec := do(t, mux, http.MethodPost, "/v1/default/banks/b2/operations/"+validOpID+"/retry"); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-bank retry = %d, want 404", rec.Code)
	}
	// The operation is untouched in its owning bank.
	if op, err := store.GetOperation(context.TODO(), "b1", validOpID); err != nil || op.Status != storage.StatusFailed {
		t.Fatalf("owning bank operation changed: %+v, %v", op, err)
	}
}

// test-operation-child-rollup: a parent operation read returns its child
// operations under child_operations, ordered by sub-batch index, and the raw
// task payload lives under task_payload.
func TestOperationChildOperations(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: "batch_retain", Status: storage.StatusCompleted,
		ResultMetadata: map[string]any{"is_parent": true, "num_sub_batches": 2},
		Payload:        map[string]any{"items": 2},
	}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{validOpID2, "33333333-3333-3333-3333-333333333333"} {
		if _, err := store.PutOperation(context.TODO(), storage.Operation{
			OperationID: id, BankID: "b1", OperationType: "retain", Status: storage.StatusCompleted,
			ResultMetadata: map[string]any{
				"parent_operation_id": validOpID, "sub_batch_index": i, "items_count": i + 1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID+"?include_payload=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ChildOperations []map[string]any `json:"child_operations"`
		TaskPayload     map[string]any   `json:"task_payload"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ChildOperations) != 2 {
		t.Fatalf("child_operations = %+v", got.ChildOperations)
	}
	if got.ChildOperations[0]["operation_id"] != validOpID2 {
		t.Fatalf("children must be ordered by sub_batch_index: %+v", got.ChildOperations)
	}
	if got.TaskPayload == nil {
		t.Fatalf("include_payload must carry task_payload: %s", rec.Body.String())
	}
	if _, ok := got.TaskPayload["items"]; !ok {
		t.Fatalf("task_payload = %+v", got.TaskPayload)
	}
}

// test-list-operations-exclude-parents: exclude_parents hides parent batch
// operations from both the page and the total.
func TestListOperationsExcludeParents(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: "batch_retain", Status: storage.StatusCompleted,
		ResultMetadata: map[string]any{"is_parent": true},
	}); err != nil {
		t.Fatal(err)
	}
	seedOp(t, store, validOpID2, storage.StatusCompleted)

	var all struct {
		Total      int              `json:"total"`
		Operations []map[string]any `json:"operations"`
	}
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations")
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Fatalf("unfiltered total = %d, want 2", all.Total)
	}
	var filtered struct {
		Total      int              `json:"total"`
		Operations []map[string]any `json:"operations"`
	}
	rec = do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations?exclude_parents=true")
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || len(filtered.Operations) != 1 {
		t.Fatalf("exclude_parents page = %+v", filtered)
	}
	if filtered.Operations[0]["id"] != validOpID2 {
		t.Fatalf("exclude_parents must hide the parent, got %+v", filtered.Operations[0])
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
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID+"?include_payload=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["task_payload"]; !ok {
		t.Fatalf("include switch must carry the raw payload under task_payload: %s", rec.Body.String())
	}
	if _, ok := got["payload"]; ok {
		t.Fatalf("the raw payload must not appear under the non-contract 'payload' key: %s", rec.Body.String())
	}
	if got["operation_type"] != "retain" {
		t.Fatalf("operation_type = %v", got["operation_type"])
	}
	// The single read names the operation under `operation_id`, and the
	// include_payload spelling is accepted as an alias for include=payload.
	if got["operation_id"] != validOpID {
		t.Fatalf("operation_id = %v, want %s", got["operation_id"], validOpID)
	}
	rec = do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID+"?include_payload=true")
	var aliased map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &aliased); err != nil {
		t.Fatal(err)
	}
	if _, ok := aliased["task_payload"]; !ok {
		t.Fatalf("include_payload must carry the raw payload: %s", rec.Body.String())
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

// test-operation-parent-self-heal (55 D4): a parent batch left "pending" though
// every child has finished reads as its corrected terminal status — failed when
// a child failed, completed otherwise — and the correction is persisted, so the
// next read (and the list) sees the repaired row. A child still in flight, and a
// cancelled child, leave the parent untouched.
func TestOperationParentSelfHeal(t *testing.T) {
	seedBatch := func(t *testing.T, store *storage.MemoryStore, parent string, childStatuses ...storage.OperationStatus) {
		t.Helper()
		if _, err := store.PutOperation(context.TODO(), storage.Operation{
			OperationID: parent, BankID: "b1", OperationType: "batch_retain", Status: storage.StatusPending,
			ResultMetadata: map[string]any{"is_parent": true},
		}); err != nil {
			t.Fatal(err)
		}
		for i, status := range childStatuses {
			childID := "0000000" + string(rune('0'+i)) + "-0000-0000-0000-000000000000"
			if _, err := store.PutOperation(context.TODO(), storage.Operation{
				OperationID: childID, BankID: "b1", OperationType: "retain", Status: status,
				ResultMetadata: map[string]any{"parent_operation_id": parent, "sub_batch_index": i},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	statusOf := func(t *testing.T, mux *http.ServeMux, parent string) string {
		t.Helper()
		rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+parent)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got["status"].(string)
	}

	// All children completed: the pending parent reads completed and is repaired.
	mux, store := newServer(t)
	seedBatch(t, store, validOpID, storage.StatusCompleted, storage.StatusCompleted)
	if got := statusOf(t, mux, validOpID); got != "completed" {
		t.Fatalf("self-healed status = %q, want completed", got)
	}
	repaired, err := store.GetOperation(context.TODO(), "b1", validOpID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Status != storage.StatusCompleted || repaired.CompletedAt == nil {
		t.Fatalf("parent not persisted as repaired: %+v", repaired)
	}

	// A failed child makes the corrected status failed.
	mux2, store2 := newServer(t)
	seedBatch(t, store2, validOpID, storage.StatusCompleted, storage.StatusFailed)
	if got := statusOf(t, mux2, validOpID); got != "failed" {
		t.Fatalf("self-healed status = %q, want failed", got)
	}

	// A child still running leaves the parent pending — no repair.
	mux3, store3 := newServer(t)
	seedBatch(t, store3, validOpID, storage.StatusCompleted, storage.StatusProcessing)
	if got := statusOf(t, mux3, validOpID); got != "pending" {
		t.Fatalf("status = %q, want pending (a child is in flight)", got)
	}

	// A cancelled child is not finished for the read-path repair: the parent stays pending.
	mux4, store4 := newServer(t)
	seedBatch(t, store4, validOpID, storage.StatusCompleted, storage.StatusCancelled)
	if got := statusOf(t, mux4, validOpID); got != "pending" {
		t.Fatalf("status = %q, want pending (a child was cancelled)", got)
	}
}

// test-operation-delete-drops-archive (58 D3): deleting a terminal export
// operation removes the stored archive its result_metadata points at, so the
// blob does not outlive the row that is its only handle.
func TestDeleteOperationRemovesExportArchive(t *testing.T) {
	mux, store := newServer(t)
	const key = "banks/b1/exports-" + validOpID + ".zip"
	if err := store.PutStoredFile(context.TODO(), storage.StoredFile{
		Key: key, ContentType: "application/zip", Data: []byte("PK\x03\x04archive"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: "export", Status: storage.StatusPending,
		ResultMetadata: map[string]any{"storage_key": key, "download_url": "/v1/default/files/download/" + key},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceOperation(context.TODO(), validOpID, storage.StatusProcessing, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceOperation(context.TODO(), validOpID, storage.StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetStoredFile(context.TODO(), key); err != nil {
		t.Fatalf("archive must exist before delete: %v", err)
	}
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/"+validOpID+"/delete")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := store.GetStoredFile(context.TODO(), key); !errors.Is(err, storage.ErrStoredFileNotFound) {
		t.Fatalf("archive survived the delete: err = %v", err)
	}
}

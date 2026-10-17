package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The operation-listing pagination bounds and the missing-bank refusal match
// the reference: limit 1..100, offset >= 0, and an unknown bank is a 404.
func TestListOperationsValidation(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		query string
		code  int
		kind  string
	}{
		{"?limit=500", http.StatusUnprocessableEntity, "less_than_equal"},
		{"?limit=0", http.StatusUnprocessableEntity, "greater_than_equal"},
		{"?limit=-1", http.StatusUnprocessableEntity, "greater_than_equal"},
		{"?limit=abc", http.StatusUnprocessableEntity, "int_parsing"},
		{"?offset=-1", http.StatusUnprocessableEntity, "greater_than_equal"},
		{"?exclude_parents=maybe", http.StatusUnprocessableEntity, "bool_parsing"},
		{"?limit=100", http.StatusOK, ""},
		{"?exclude_parents=on", http.StatusOK, ""},
	}
	for _, tc := range cases {
		rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations"+tc.query)
		if rec.Code != tc.code {
			t.Fatalf("%s: status = %d, want %d (body %s)", tc.query, rec.Code, tc.code, rec.Body.String())
		}
		if tc.kind == "" {
			continue
		}
		var body struct {
			Detail []map[string]any `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if len(body.Detail) == 0 || body.Detail[0]["type"] != tc.kind {
			t.Fatalf("%s: detail = %v, want type %s", tc.query, body.Detail, tc.kind)
		}
	}
}

// The list route reports an unknown bank as the reference's named 404.
func TestListOperationsMissingBank(t *testing.T) {
	mux, _ := newServer(t)
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/never-created/operations")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Detail != "Bank 'never-created' not found" {
		t.Fatalf("detail = %q", body.Detail)
	}
}

// The single-operation read keeps all sixteen keys, and the not_found body
// carries every one as an explicit null.
func TestOperationNotFoundKeepsNulls(t *testing.T) {
	mux, _ := newServer(t)
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"operation_id", "status", "operation_type", "id", "task_type", "mental_model_id",
		"created_at", "updated_at", "completed_at", "error_message", "retry_count", "next_retry_at",
		"progress", "result_metadata", "details", "child_operations", "task_payload"} {
		if _, present := got[key]; !present {
			t.Fatalf("missing key %q in %v", key, got)
		}
	}
	if got["result_metadata"] != nil || got["details"] != nil {
		t.Fatalf("not_found must carry null result_metadata/details: %v", got)
	}
}

// A non-canonical (32-hex) identifier addresses the same row the reference's
// uuid parser folds onto it, while the raw spelling is echoed back.
func TestOperationIDAcceptedUndeashed(t *testing.T) {
	mux, store := newServer(t)
	seedOp(t, store, validOpID, storage.StatusCompleted)
	undeashed := strings.ReplaceAll(validOpID, "-", "")
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+undeashed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "completed" || got["operation_id"] != undeashed || got["id"] != undeashed {
		t.Fatalf("body = %v", got)
	}
}

// details and progress are projected out of result_metadata, matching the
// reference's list and single-read projections.
func TestOperationDetailsAndProgress(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: "refresh_mental_model",
		Status: storage.StatusCompleted,
		ResultMetadata: map[string]any{
			"outcome":  "content_written",
			"progress": map[string]any{"stage": "storing", "at": "2026-01-01T00:00:00+00:00"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	read := func(path string) map[string]any {
		t.Helper()
		rec := do(t, mux, http.MethodGet, path)
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	single := read("/v1/default/banks/b1/operations/" + validOpID)
	details, ok := single["details"].(map[string]any)
	if !ok {
		t.Fatalf("single details = %v", single["details"])
	}
	if details["operation_type"] != "refresh_mental_model" || details["outcome"] != "content_written" {
		t.Fatalf("details = %v", details)
	}
	if _, present := details["failure_reason"]; !present {
		t.Fatalf("details must carry failure_reason: %v", details)
	}
	if _, ok := single["progress"].(map[string]any); !ok {
		t.Fatalf("progress = %v", single["progress"])
	}

	list := read("/v1/default/banks/b1/operations")
	entries := list["operations"].([]any)
	first := entries[0].(map[string]any)
	if first["details"] == nil || first["progress"] == nil {
		t.Fatalf("list entry must carry details and progress: %v", first)
	}
}

// An unrecognised refresh outcome reports no typed detail rather than a value
// the reference's vocabulary cannot name.
func TestOperationDetailsUnknownOutcome(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: "refresh_mental_model",
		Status:         storage.StatusCompleted,
		ResultMetadata: map[string]any{"outcome": "from_the_future"},
	}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, mux, http.MethodGet, "/v1/default/banks/b1/operations/"+validOpID)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["details"] != nil {
		t.Fatalf("unknown outcome must report no details: %v", got["details"])
	}
}

// Cancel and retry name the identifier in their success message, matching the
// reference.
func TestOperationActionMessages(t *testing.T) {
	mux, store := newServer(t)
	seedOp(t, store, validOpID, storage.StatusPending)
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/"+validOpID)
	var cancelled map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled["message"] != "Operation "+validOpID+" cancelled" {
		t.Fatalf("cancel message = %v", cancelled["message"])
	}

	seedOp(t, store, validOpID2, storage.StatusFailed)
	rec = do(t, mux, http.MethodPost, "/v1/default/banks/b1/operations/"+validOpID2+"/retry")
	var retried map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &retried); err != nil {
		t.Fatal(err)
	}
	if retried["message"] != "Operation "+validOpID2+" queued for retry" {
		t.Fatalf("retry message = %v", retried["message"])
	}
}

// A cancelled operation is retryable, and the retry resets the run's bookkeeping
// (retry_count and completed_at).
func TestRetryCancelledOperation(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: storage.OperationTypeRetain,
		Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelOperation(context.TODO(), "b1", validOpID); err != nil {
		t.Fatal(err)
	}
	rec := do(t, mux, http.MethodPost, "/v1/default/banks/b1/operations/"+validOpID+"/retry")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	op, err := store.GetOperation(context.TODO(), "b1", validOpID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != storage.StatusPending || op.RetryCount != 0 || op.CompletedAt != nil {
		t.Fatalf("retried operation = %+v", op)
	}
}

// Cancelling the last live child of a batch terminalises its parent.
func TestCancelRollsUpParent(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID, BankID: "b1", OperationType: "batch_retain", Status: storage.StatusProcessing,
		ResultMetadata: map[string]any{"is_parent": true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: validOpID2, BankID: "b1", OperationType: storage.OperationTypeRetain, Status: storage.StatusCompleted,
		ResultMetadata: map[string]any{"parent_operation_id": validOpID, "sub_batch_index": 1},
	}); err != nil {
		t.Fatal(err)
	}
	const childID = "33333333-3333-3333-3333-333333333333"
	if _, err := store.PutOperation(context.TODO(), storage.Operation{
		OperationID: childID, BankID: "b1", OperationType: storage.OperationTypeRetain, Status: storage.StatusPending,
		ResultMetadata: map[string]any{"parent_operation_id": validOpID, "sub_batch_index": 2},
	}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/"+childID)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", rec.Code, rec.Body.String())
	}
	parent, err := store.GetOperation(context.TODO(), "b1", validOpID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status == storage.StatusProcessing || !parent.Status.Terminal() {
		t.Fatalf("parent must be terminal after the last child cancels: %+v", parent.Status)
	}
}

// The write routes report the reference's not-found detail, naming bank and id.
func TestOperationWriteNotFoundDetail(t *testing.T) {
	mux, _ := newServer(t)
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/operations/"+validOpID+"/delete")
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Detail != "Operation "+validOpID+" not found for bank b1" {
		t.Fatalf("detail = %q", body.Detail)
	}
}

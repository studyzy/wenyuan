package memoryops

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// decodeJSON reads a JSON body into the target value and reports any top-level
// field the target model does not accept, so the ignored-parameter header
// names a typo instead of silently dropping it. It returns the bytes it read,
// so a caller that reports a rejected body can name the value the decode
// failed on the way the shared decoder does. An absent body is reported as
// httpx.ErrEmptyBody, which the shared renderer answers with the item for a
// missing required field.
func decodeJSON(r *http.Request, target any) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	httpx.RecordUnknownBodyFields(r, body, target)
	if len(strings.TrimSpace(string(body))) == 0 {
		return body, httpx.ErrEmptyBody
	}
	return body, json.Unmarshal(body, target)
}

// OperationIDPathParam is the path parameter naming the async operation.
const OperationIDPathParam = "operation_id"

// rejectMalformedOperationID writes the reference's bad-request envelope for an
// identifier that is not a UUID and reports whether it did. Every operation
// member route shares it, so the same malformed identifier gets the same 400 and
// a plain detail string rather than any route's structured validation body.
func rejectMalformedOperationID(w http.ResponseWriter, r *http.Request, opID string) bool {
	if IsValidUUIDShape(opID) {
		return false
	}
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "Invalid operation_id format: "+opID))
	return true
}

// GetOperation serves the asynchronous operation status route. The lookup is
// scoped to the path bank, so an identifier belonging to another bank reads as
// not-found rather than exposing that bank's operation. A missing operation is
// a 200 with status "not_found", matching the reference rather than a 404.
func (h *Handlers) GetOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	if rejectMalformedOperationID(w, r, opID) {
		return
	}
	bankID := r.PathValue("bank_id")
	includePayload := isTruthyQuery(r, "include_payload")
	op, err := h.Store.GetOperation(r.Context(), bankID, opID)
	if err != nil {
		if errors.Is(err, storage.ErrOperationNotFound) {
			httpx.WriteJSONFor(w, r, http.StatusOK, notFoundOperationStatus(opID))
			return
		}
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	var children []storage.ChildOperation
	if op.IsParent {
		children, err = h.Store.GetOperationChildren(r.Context(), bankID, opID)
		if err != nil {
			httpx.WriteErrorFor(w, r, h.mapOperationError(err))
			return
		}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ToOperationStatusDTO(op, children, includePayload))
}

// isTruthyQuery reports whether a query parameter is set to a truthy value.
func isTruthyQuery(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(httpx.Query(r, key))) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// CancelOperation serves the operation-cancellation route.
func (h *Handlers) CancelOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	if rejectMalformedOperationID(w, r, opID) {
		return
	}
	op, err := h.Store.CancelOperation(r.Context(), r.PathValue("bank_id"), opID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, OperationActionResponse{
		Success: true, Message: "operation cancelled", OperationID: op.OperationID,
	})
}

// RetryOperation starts a new run of a failed operation.
func (h *Handlers) RetryOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	if rejectMalformedOperationID(w, r, opID) {
		return
	}
	op, err := h.Store.RetryOperation(r.Context(), r.PathValue("bank_id"), opID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, OperationActionResponse{
		Success: true, Message: "operation requeued", OperationID: op.OperationID,
	})
}

// DeleteOperation permanently removes a terminal operation. A malformed
// identifier is rejected before any lookup, and only failed, cancelled or
// completed operations are removable.
func (h *Handlers) DeleteOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	// The reference rejects a non-UUID identifier with a 400 and a plain
	// detail string, not the structured validation envelope.
	if rejectMalformedOperationID(w, r, opID) {
		return
	}
	bankID := r.PathValue("bank_id")
	if _, err := h.Store.DeleteOperation(r.Context(), bankID, opID); err != nil {
		httpx.WriteErrorFor(w, r, h.mapDeleteOperationError(err, opID))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, OperationActionResponse{
		Success: true, Message: "Operation " + opID + " deleted", OperationID: opID,
	})
}

// mapDeleteOperationError renders the delete-specific failures. A live
// operation is a conflict whose message names the observed status.
func (h *Handlers) mapDeleteOperationError(err error, opID string) error {
	switch {
	case errors.Is(err, storage.ErrOperationNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Operation "+opID+" not found")
	case errors.Is(err, storage.ErrOperationNotTerminal):
		return httpx.NewHTTPError(httpx.KindConflict,
			"Operation "+opID+" cannot be deleted: status is not terminal, expected 'failed', 'cancelled' or 'completed'")
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

func (h *Handlers) mapOperationError(err error) error {
	switch {
	case errors.Is(err, storage.ErrOperationNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "operation not found")
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "operation is already in a terminal state")
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

// ToOperationListDTO maps a stored operation into the list entry the reference
// calls OperationResponse. The subject and outcome fields come from the result
// metadata, which is the only per-operation record the list carries.
func ToOperationListDTO(op storage.Operation) Operation {
	dto := Operation{
		ID:          op.OperationID,
		TaskType:    op.OperationType,
		ItemsCount:  itemsCountOf(op.ResultMetadata),
		Status:      string(op.Status),
		CreatedAt:   op.CreatedAt,
		RetryCount:  op.RetryCount,
		NextRetryAt: op.NextRetryAt,
		Progress:    op.Progress,
	}
	opID := op.OperationID
	dto.OperationID = &opID
	if op.OperationType != "" {
		taskType := op.OperationType
		dto.OperationType = &taskType
	}
	dto.DocumentID = optionalString(op.ResultMetadata, "document_id")
	dto.Filename = optionalString(op.ResultMetadata, "original_filename")
	dto.MentalModelID = optionalString(op.ResultMetadata, storage.MentalModelIDPayloadKey)
	dto.ErrorMessage = stringOrNil(op.Error)
	if !op.UpdatedAt.IsZero() {
		updated := op.UpdatedAt
		dto.UpdatedAt = &updated
	}
	return dto
}

// ToOperationStatusDTO maps a stored operation into the single-operation read
// the reference calls OperationStatusResponse: the full status plus the child
// roll-up and, when requested, the raw task payload.
func ToOperationStatusDTO(op storage.Operation, children []storage.ChildOperation, includePayload bool) OperationStatus {
	dto := OperationStatus{
		OperationID: op.OperationID,
		Status:      string(op.Status),
	}
	if op.OperationType != "" {
		value := op.OperationType
		dto.TaskType = &value
		operationType := op.OperationType
		dto.OperationType = &operationType
	}
	opID := op.OperationID
	dto.ID = &opID
	dto.MentalModelID = optionalString(op.ResultMetadata, storage.MentalModelIDPayloadKey)
	if !op.CreatedAt.IsZero() {
		created := op.CreatedAt
		dto.CreatedAt = &created
	}
	if !op.UpdatedAt.IsZero() {
		updated := op.UpdatedAt
		dto.UpdatedAt = &updated
	}
	dto.CompletedAt = op.CompletedAt
	dto.ErrorMessage = stringOrNil(op.Error)
	retryCount := op.RetryCount
	dto.RetryCount = &retryCount
	dto.NextRetryAt = op.NextRetryAt
	dto.Progress = op.Progress
	dto.ResultMetadata = op.ResultMetadata
	if op.IsParent {
		dto.ChildOperations = make([]ChildOperationStatus, 0, len(children))
		for _, child := range children {
			dto.ChildOperations = append(dto.ChildOperations, ChildOperationStatus{
				OperationID:   child.OperationID,
				Status:        string(child.Status),
				SubBatchIndex: child.SubBatchIndex,
				ItemsCount:    child.ItemsCount,
				ErrorMessage:  child.ErrorMessage,
			})
		}
	}
	if includePayload {
		dto.TaskPayload = op.Payload
	}
	return dto
}

// notFoundOperationStatus is the single-operation read for a missing (or
// wrong-bank) identifier: the reference answers 200 with status "not_found"
// rather than a 404. The remaining fields are null and are dropped by the
// response shaping, leaving the identifier, status and echoed id.
func notFoundOperationStatus(opID string) OperationStatus {
	id := opID
	return OperationStatus{OperationID: opID, Status: string(storage.StatusNotFound), ID: &id}
}

// itemsCountOf reads the ready item count a completed operation recorded. The
// value is an int when a processor wrote it in-process and a float64 when it
// round-tripped through JSON, so both are accepted.
func itemsCountOf(metadata map[string]any) int {
	switch count := metadata["items_count"].(type) {
	case float64:
		return int(count)
	case int:
		return count
	case int64:
		return int(count)
	case json.Number:
		if value, err := count.Int64(); err == nil {
			return int(value)
		}
	}
	return 0
}

// optionalString reads a string member from a decoded object, returning nil for
// an absent or non-string value so the field is emitted as null.
func optionalString(object map[string]any, key string) *string {
	if value, ok := object[key].(string); ok {
		return &value
	}
	return nil
}

// stringOrNil returns a pointer to a non-empty string, and nil for an empty one
// so the field is emitted as null.
func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func queryInt(r *http.Request, key string) int {
	raw := httpx.Query(r, key)
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

package memoryops

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// decodeJSON reads a JSON body into the target value.
func decodeJSON(r *http.Request, target any) error {
	return json.NewDecoder(r.Body).Decode(target)
}

// OperationIDPathParam is the path parameter naming the async operation.
const OperationIDPathParam = "operation_id"

// GetOperation serves the asynchronous operation status route.
func (h *Handlers) GetOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	if failure := ValidateOperationID(opID); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	op, err := h.Store.GetOperation(r.Context(), opID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	includePayload := r.URL.Query().Get("include") == "payload"
	httpx.WriteJSONFor(w, r, http.StatusOK, ToOperationDTO(op, includePayload))
}

// CancelOperation serves the operation-cancellation route.
func (h *Handlers) CancelOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	if failure := ValidateOperationID(opID); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	op, err := h.Store.CancelOperation(r.Context(), opID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, OperationActionResponse{
		Success: true, Message: "operation cancelled", OperationID: op.ID,
	})
}

// RetryOperation starts a new run of a failed operation.
func (h *Handlers) RetryOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	if failure := ValidateOperationID(opID); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	op, err := h.Store.RetryOperation(r.Context(), opID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, OperationActionResponse{
		Success: true, Message: "operation requeued", OperationID: op.ID,
	})
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

// ToOperationDTO maps a stored operation into the public operation object.
func ToOperationDTO(op storage.Operation, includePayload bool) Operation {
	dto := Operation{
		ID:             op.ID,
		BankID:         op.BankID,
		OperationType:  op.OperationType,
		TaskType:       op.OperationType,
		Status:         string(op.Status),
		ErrorMessage:   op.Error,
		CreatedAt:      op.CreatedAt,
		UpdatedAt:      op.UpdatedAt,
		CompletedAt:    op.CompletedAt,
		RetryCount:     op.RetryCount,
		NextRetryAt:    op.NextRetryAt,
		Progress:       op.Progress,
		ResultMetadata: op.ResultMetadata,
	}
	if includePayload {
		dto.Payload = op.Payload
	}
	return dto
}

func queryInt(r *http.Request, key string) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

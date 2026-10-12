package memoryops

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ListOperations serves the operation-listing route with optional status and
// type filters and a bounded page.
func (h *Handlers) ListOperations(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	limit := queryInt(r, "limit")
	offset := queryInt(r, "offset")
	if limit < 0 || offset < 0 {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
			Loc: []string{"query", "limit"}, Msg: "limit and offset must be non-negative integers",
			Type: "value_error.range",
		}}})
		return
	}
	if failure := ValidateOperationList(limit, offset); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	page, err := h.Store.ListOperations(r.Context(), storage.OperationQuery{
		BankID: bankID,
		Status: r.URL.Query().Get("status"),
		Type:   r.URL.Query().Get("type"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	ops := make([]Operation, 0, len(page.Operations))
	for _, op := range page.Operations {
		ops = append(ops, ToOperationDTO(op, false))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, OperationListResponse{
		BankID:     bankID,
		Total:      page.Total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		Operations: ops,
	})
}

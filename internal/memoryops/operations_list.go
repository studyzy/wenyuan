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
	// An omitted limit uses the reference's default page size; an explicit
	// non-positive one is rejected below.
	if limit == 0 && httpx.Query(r, "limit") == "" {
		limit = 20
	}
	if limit < 0 || offset < 0 {
		var items []httpx.ValidationError
		if limit < 0 {
			items = append(items, httpx.GreaterThanEqual([]string{"query", "limit"}, limit, 0))
		}
		if offset < 0 {
			items = append(items, httpx.GreaterThanEqual([]string{"query", "offset"}, offset, 0))
		}
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: items})
		return
	}
	if failure := ValidateOperationList(limit, offset); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	page, err := h.Store.ListOperations(r.Context(), storage.OperationQuery{
		BankID:         bankID,
		Status:         httpx.Query(r, "status"),
		Type:           httpx.Query(r, "type"),
		Limit:          limit,
		Offset:         offset,
		ExcludeParents: isTruthyQuery(r, "exclude_parents"),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	ops := make([]Operation, 0, len(page.Operations))
	for _, op := range page.Operations {
		ops = append(ops, ToOperationListDTO(op))
	}
	// OperationsListResponse is in the reference's keep-nulls policy, so the
	// list entry's nullable members stay in the body as explicit nulls.
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, OperationListResponse{
		BankID:     bankID,
		Total:      page.Total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		Operations: ops,
	})
}

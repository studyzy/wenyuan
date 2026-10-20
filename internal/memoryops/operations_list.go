package memoryops

import (
	"errors"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The operation listing pagination bounds the reference declares on the query:
// a page size of 1..100 (default 20) and a non-negative offset.
const (
	operationListDefaultLimit = 20
	operationListMaxLimit     = 100
)

// ListOperations serves the operation-listing route with optional status and
// type filters and a bounded page.
func (h *Handlers) ListOperations(w http.ResponseWriter, r *http.Request) {
	// The operation log is answered for the whole bank, not narrowed to a tag
	// scope, so a confined caller may not read it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "list the bank's operations") {
		return
	}
	bankID := r.PathValue("bank_id")
	limit, offset, excludeParents, failure := parseOperationListQuery(r)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		if errors.Is(err, storage.ErrBankNotFound) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
			return
		}
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	page, err := h.Store.ListOperations(r.Context(), storage.OperationQuery{
		BankID:         bankID,
		Status:         httpx.Query(r, "status"),
		Type:           httpx.Query(r, "type"),
		Limit:          limit,
		Offset:         offset,
		ExcludeParents: excludeParents,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	ops := make([]Operation, 0, len(page.Operations))
	for _, op := range page.Operations {
		ops = append(ops, ToOperationListDTO(op))
	}
	// OperationsListResponse keeps null fields, so the list entry's nullable
	// members stay in the body as explicit nulls.
	httpx.WriteJSON(w, http.StatusOK, OperationListResponse{
		BankID:     bankID,
		Total:      page.Total,
		Limit:      limit,
		Offset:     offset,
		Operations: ops,
	})
}

// parseOperationListQuery reads limit, offset and exclude_parents the way the
// reference's framework validates them: each is parsed and bounds-checked, and
// the structured items for every rejected parameter are returned in the
// declaration order (limit, offset, exclude_parents).
func parseOperationListQuery(r *http.Request) (int, int, bool, *httpx.ValidationFailure) {
	items := make([]httpx.ValidationError, 0, 3)

	limit := operationListDefaultLimit
	if value, present, item := queryIntParam(r, "limit", 1, operationListMaxLimit); item != nil {
		items = append(items, *item)
	} else if present {
		limit = value
	}

	offset := 0
	if value, present, item := queryIntParam(r, "offset", 0, 0); item != nil {
		items = append(items, *item)
	} else if present {
		offset = value
	}

	excludeParents, item := queryBool(r, "exclude_parents")
	if item != nil {
		items = append(items, *item)
	}

	if len(items) > 0 {
		return 0, 0, false, &httpx.ValidationFailure{Errors: items}
	}
	return limit, offset, excludeParents, nil
}

// queryIntParam parses a bounded integer query parameter. min and max are the
// inclusive bounds the reference declares (a max of 0 means no upper bound). The
// second result reports whether the parameter was present, and the third is the
// structured item for a value that failed to parse or fell outside its bounds;
// when it is non-nil the other results are unset.
func queryIntParam(r *http.Request, key string, min, max int) (int, bool, *httpx.ValidationError) {
	values, present := httpx.QueryParamsOf(r)[key]
	if !present || len(values) == 0 {
		return 0, false, nil
	}
	// The reference binds the LAST occurrence of a repeated scalar parameter.
	raw := values[len(values)-1]
	// The reference reports the raw string the client sent, not the coerced int.
	// The grammar is the framework's, not Go's: surrounding whitespace, an
	// optional sign, underscores between digits and a fractional part only when
	// every digit is zero, so `1.0` reads as 1 and `1_0` as 10.
	parsed, ok := httpx.LaxInteger(raw)
	if !ok {
		// A Python integer past the machine's range is still parsed losslessly by
		// the framework, so only its sign decides which bound it is outside of.
		sign, valid := httpx.BigintSign(raw)
		switch {
		case !valid:
			item := httpx.TypeMismatch("int_parsing", []string{"query", key}, raw)
			return 0, false, &item
		case sign < 0:
			item := httpx.GreaterThanEqual([]string{"query", key}, raw, min)
			return 0, false, &item
		case max != 0:
			item := httpx.LessThanEqual([]string{"query", key}, raw, max)
			return 0, false, &item
		default:
			// No upper bound to violate: the framework hands the value to the
			// store, which cannot carry it and fails — the reference answers that
			// as the internal failure it is.
			return 0, false, nil
		}
	}
	if parsed < min {
		item := httpx.GreaterThanEqual([]string{"query", key}, raw, min)
		return 0, false, &item
	}
	if max != 0 && parsed > max {
		item := httpx.LessThanEqual([]string{"query", key}, raw, max)
		return 0, false, &item
	}
	return parsed, true, nil
}

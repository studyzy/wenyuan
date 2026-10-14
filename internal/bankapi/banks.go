package bankapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Handlers wires the bank API routes to a store.
type Handlers struct {
	Store storage.Store
	// ConfigEnabled gates the bank configuration write route; the read route
	// is never gated.
	ConfigEnabled bool
}

// NewHandlers builds the bank API handler set.
func NewHandlers(store storage.Store) *Handlers {
	return &Handlers{Store: store}
}

// WithConfigAPI returns a copy of the handler set with the configuration write
// gate applied.
func (h *Handlers) WithConfigAPI(enabled bool) *Handlers {
	h.ConfigEnabled = enabled
	return h
}

// ListBanks serves the bank listing route with pagination and search.
func (h *Handlers) ListBanks(w http.ResponseWriter, r *http.Request) {
	query := parseBankQuery(r)
	page, err := h.Store.ListBanks(r.Context(), query)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	rows := make([]BankListRow, 0, len(page.Banks))
	for _, bank := range page.Banks {
		display := h.displayAlias(r, bank.BankID)
		rows = append(rows, ToListRow(bank, display, page.MatchedAliases[bank.BankID]))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BankListResponse{
		Banks:  rows,
		Total:  page.Total,
		Limit:  page.Limit,
		Offset: page.Offset,
	})
}

// ReplaceBank serves the create-or-replace route. A name that already names a
// bank (directly or through an alias) has the supplied fields applied; a name
// that is only an alias of another bank is a conflict; otherwise the bank is
// created.
func (h *Handlers) ReplaceBank(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("bank_id")
	if writeIdentifierError(w, r, rawID) {
		return
	}
	var req BankRequest
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body is not valid JSON"))
		return
	}
	if failure := ValidateCreate(req); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	if _, err := h.Store.GetBank(r.Context(), rawID); err != nil {
		if !errors.Is(err, storage.ErrBankNotFound) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
			return
		}
		// The name is not a real bank: a name that is only an alias of another
		// bank cannot be created because it would shadow that alias.
		if owner, _ := h.Store.ResolveBankID(r.Context(), rawID); owner != rawID {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias"))
			return
		}
		bank, cerr := h.Store.CreateBank(r.Context(), req.ToStorage(rawID))
		if cerr != nil {
			httpx.WriteErrorFor(w, r, h.mapWriteError(cerr))
			return
		}
		httpx.WriteJSONFor(w, r, http.StatusOK, ToResponse(bank))
		return
	}
	bank, err := h.Store.ReplaceBank(r.Context(), req.ToStorage(rawID))
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ToResponse(bank))
}

// UpdateBank serves the partial-update route. A missing bank is not created.
func (h *Handlers) UpdateBank(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	var req BankRequest
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body is not valid JSON"))
		return
	}
	if failure := ValidatePatch(req); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	bank, err := h.Store.UpdateBank(r.Context(), bankID, req.ToPatch())
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ToResponse(bank))
}

// DeleteBank serves the bank deletion route. Deleting a bank that never existed
// is a zero-item no-op.
func (h *Handlers) DeleteBank(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("bank_id")
	if writeIdentifierError(w, r, rawID) {
		return
	}
	bankID, err := h.Store.ResolveBankID(r.Context(), rawID)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	deleted, err := h.Store.DeleteBank(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	message := "bank and its content deleted"
	if deleted == 0 {
		message = "no bank matched; nothing was deleted"
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DeleteResponse{
		Success:      true,
		Message:      message,
		DeletedCount: deleted,
	})
}

// resolveCanonical rewrites a bank-scoped path parameter to its canonical
// identifier after alias resolution. It writes a not-found error and reports
// false when the identifier is malformed or the bank does not exist.
func (h *Handlers) resolveCanonical(w http.ResponseWriter, r *http.Request) (string, bool) {
	rawID := r.PathValue("bank_id")
	if writeIdentifierError(w, r, rawID) {
		return "", false
	}
	bankID, err := h.Store.ResolveBankID(r.Context(), rawID)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return "", false
	}
	return bankID, true
}

func (h *Handlers) displayAlias(r *http.Request, bankID string) string {
	aliases, err := h.Store.ListAliases(r.Context(), bankID)
	if err != nil {
		return ""
	}
	for _, alias := range aliases {
		if alias.Primary {
			return alias.Alias
		}
	}
	return ""
}

func (h *Handlers) mapWriteError(err error) error {
	switch {
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias")
	case errors.Is(err, storage.ErrAliasNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "alias not found")
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "bank already exists")
	default:
		return validateBankExists(err)
	}
}

func validateBankExists(err error) error {
	if errors.Is(err, storage.ErrBankNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailUnknownBank)
	}
	return err
}

func parseBankQuery(r *http.Request) storage.BankQuery {
	query := storage.BankQuery{Search: r.URL.Query().Get("q")}
	if limit, ok := nonNegativeParam(r, "limit"); ok {
		query.Limit = limit
	}
	if offset, ok := nonNegativeParam(r, "offset"); ok {
		query.Offset = offset
	}
	return query
}

func nonNegativeParam(r *http.Request, name string) (int, bool) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(target)
}

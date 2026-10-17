package bankapi

import (
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
	// ConfigDefaults is the process layer the configuration read resolves
	// overrides on top of, so a field the bank did not override still reads its
	// effective value. A nil map leaves the read showing only what was set.
	ConfigDefaults map[string]any
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

// WithConfigDefaults returns the handler set with the process-level
// configuration the read route resolves overrides on top of.
func (h *Handlers) WithConfigDefaults(defaults map[string]any) *Handlers {
	h.ConfigDefaults = defaults
	return h
}

// defaultBankListLimit is the reference's page size when the request names no
// limit.
const defaultBankListLimit = 100

// ListBanks serves the bank listing route with pagination and search.
func (h *Handlers) ListBanks(w http.ResponseWriter, r *http.Request) {
	query, failure := parseBankQuery(r)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	page, err := h.Store.ListBanks(r.Context(), query)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	rows := make([]BankListRow, 0, len(page.Banks))
	for _, bank := range page.Banks {
		display := h.displayAlias(r, bank.BankID)
		// The row reports the bank's resolved disposition and mission, which is
		// what the reference's list serves: a bank whose configuration names a
		// trait reports it rather than the column it was created with.
		disposition, mission := h.resolvedDispositionMission(r, bank)
		rows = append(rows, ToListRowWithDisposition(bank, display, page.MatchedAliases[bank.BankID], disposition, mission))
	}
	httpx.WriteJSON(w, http.StatusOK, BankListResponse{
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
	// Changing a bank's mission or config reaches the whole bank, so a
	// tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's mission or config") {
		return
	}
	rawID := r.PathValue("bank_id")
	var req BankRequest
	if err := httpx.DecodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if failure := ValidateCreate(req); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	configPatchValue, httpErr := configPatch(req.configUpdates())
	if httpErr != nil {
		httpx.WriteErrorFor(w, r, httpErr)
		return
	}
	existing, err := h.Store.GetBank(r.Context(), rawID)
	if err != nil {
		if !errors.Is(err, storage.ErrBankNotFound) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
			return
		}
		// The name is not a real bank: a name that is only an alias of another
		// bank cannot be created because it would shadow that alias.
		if owner, _ := h.Store.ResolveBankID(r.Context(), rawID); owner != rawID {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict, "'"+rawID+"' is an alias of another bank"))
			return
		}
		bank := req.ToStorage(rawID)
		// The identifier admission runs on the create path only, matching the
		// reference's _ensure_bank_exists, and reports the specific rule it broke.
		if writeIdentifierError(w, r, rawID) {
			return
		}
		bank.Config = mergeConfig(nil, configPatchValue.Config)
		created, cerr := h.Store.CreateBank(r.Context(), bank)
		if cerr != nil {
			httpx.WriteErrorFor(w, r, h.mapWriteError(cerr))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, h.profileResponse(r, created))
		return
	}
	// The bank exists: apply only the fields the request supplied, exactly as
	// the reference's update_bank does for a PUT to an existing bank.
	h.applyBankUpdate(w, r, rawID, req, existing)
}

// applyBankUpdate writes the fields a PUT or PATCH actually supplied over an
// existing bank, leaving every field the caller omitted untouched. It mirrors
// the reference's update_bank, whose SET clause names only the supplied columns:
// a name-only update keeps the stored mission and disposition, and a
// mission-only update keeps the stored name.
func (h *Handlers) applyBankUpdate(w http.ResponseWriter, r *http.Request, bankID string, req BankRequest, current storage.Bank) {
	configPatchValue, httpErr := configPatch(req.configUpdates())
	if httpErr != nil {
		httpx.WriteErrorFor(w, r, httpErr)
		return
	}
	patch := req.ToPatch()
	if configPatchValue.Config != nil {
		merged := mergeConfig(current.Config, configPatchValue.Config)
		patch.Config = &merged
	}
	updated, err := h.Store.UpdateBank(r.Context(), bankID, patch)
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.profileResponse(r, updated))
}

// UpdateBank serves the partial-update route. A missing bank is not created.
func (h *Handlers) UpdateBank(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's mission or config") {
		return
	}
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	var req BankRequest
	if err := httpx.DecodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if failure := ValidatePatch(req); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	current, err := h.Store.GetBank(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	h.applyBankUpdate(w, r, bankID, req, current)
}

// DeleteBank serves the bank deletion route. Deleting a bank that never existed
// is a zero-item no-op.
func (h *Handlers) DeleteBank(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "delete or clear the bank") {
		return
	}
	rawID := r.PathValue("bank_id")
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
	// The reference answers one message for every outcome, including a bank
	// that never existed; the count is what varies.
	httpx.WriteJSON(w, http.StatusOK, DeleteResponse{
		Success:      true,
		Message:      "Bank '" + bankID + "' and all associated data deleted successfully",
		DeletedCount: deleted,
	})
}

// resolveCanonical rewrites a bank-scoped path parameter to its canonical
// identifier after alias resolution. It writes a not-found error and reports
// false when the bank does not exist.
//
// A bank identifier is not shape-checked here: the reference declares it an
// unconstrained string and lets the store answer, so a malformed identifier
// reaches the store and a missing bank is what the caller sees.
func (h *Handlers) resolveCanonical(w http.ResponseWriter, r *http.Request) (string, bool) {
	rawID := r.PathValue("bank_id")
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

// resolvedDispositionMission resolves the disposition and mission a bank
// reports: its own configuration overrides layered over the process defaults,
// overlaid on the legacy columns by overlayDispositionMission. Shared by the
// list and single-bank paths so both return the same pair for the same bank.
func (h *Handlers) resolvedDispositionMission(r *http.Request, bank storage.Bank) (map[string]int, string) {
	return overlayDispositionMission(resolvedConfig(bankOverrides(bank), r, h.ConfigDefaults), bank)
}

// profileResponse renders a bank as its public profile with the configuration
// overlaid on the legacy columns, matching what the reference returns from a
// create, replace or partial update.
func (h *Handlers) profileResponse(r *http.Request, bank storage.Bank) BankResponse {
	disposition, mission := h.resolvedDispositionMission(r, bank)
	return ToResponse(bank, disposition, mission)
}

func (h *Handlers) displayAlias(r *http.Request, bankID string) *string {
	aliases, err := h.Store.ListAliases(r.Context(), bankID)
	if err != nil {
		return nil
	}
	for _, alias := range aliases {
		if alias.Primary {
			value := alias.Alias
			return &value
		}
	}
	return nil
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
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	}
	return err
}

// parseBankQuery reads the list query. An absent limit uses the reference's
// default page size; an absent offset is zero. A named limit or offset must be
// a non-negative integer, and a value that is not is a validation failure, the
// same 422 the reference's bounded query parameters produce.
func parseBankQuery(r *http.Request) (storage.BankQuery, *httpx.ValidationFailure) {
	query := storage.BankQuery{Limit: defaultBankListLimit}
	// The reference declares scalar query parameters, and the framework keeps
	// the last occurrence of a repeated key. Read the last value of each name
	// rather than the first so a repeated q/limit/offset behaves the same.
	if raw, ok := httpx.QueryPresent(r, "q"); ok {
		query.Search = raw[len(raw)-1]
	}
	var items []httpx.ValidationError
	if raw, ok := httpx.QueryPresent(r, "limit"); ok {
		value, failure := parseNonNegativeParam("limit", raw[len(raw)-1])
		if failure != nil {
			items = append(items, *failure)
		} else {
			query.Limit = value
		}
	}
	if raw, ok := httpx.QueryPresent(r, "offset"); ok {
		value, failure := parseNonNegativeParam("offset", raw[len(raw)-1])
		if failure != nil {
			items = append(items, *failure)
		} else {
			query.Offset = value
		}
	}
	if len(items) > 0 {
		return storage.BankQuery{}, &httpx.ValidationFailure{Errors: items}
	}
	return query, nil
}

// parseNonNegativeParam parses one required non-negative integer query value,
// reporting a parse failure and a range failure distinctly, as the reference
// does.
func parseNonNegativeParam(name, raw string) (int, *httpx.ValidationError) {
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		item := httpx.TypeMismatch("int_parsing", []string{"query", name}, raw)
		return 0, &item
	}
	if parsed < 0 {
		// The framework reports the raw query string as the item's input.
		item := httpx.GreaterThanEqual([]string{"query", name}, raw, 0)
		return 0, &item
	}
	return parsed, nil
}

// mergeConfig layers a validated configuration patch over a bank's existing
// overrides. A nil patch leaves the base untouched; a nil value in the patch
// removes the key (the reference's "server default" tombstone).
func mergeConfig(base map[string]any, patch *map[string]any) map[string]any {
	if patch == nil && base == nil {
		return nil
	}
	merged := make(map[string]any, len(base)+configLen(patch))
	for key, value := range base {
		merged[key] = value
	}
	if patch != nil {
		for key, value := range *patch {
			if value == nil {
				delete(merged, key)
				continue
			}
			merged[key] = value
		}
	}
	return merged
}

func configLen(patch *map[string]any) int {
	if patch == nil {
		return 0
	}
	return len(*patch)
}

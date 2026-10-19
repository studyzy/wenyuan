package bankapi

import (
	"errors"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ListAliases serves the alias listing route.
func (h *Handlers) ListAliases(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	aliases, err := h.Store.ListAliases(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ToAliasResponse(bankID, aliases))
}

// AddAlias serves the alias creation route. It returns the created status and
// the whole alias list.
func (h *Handlers) AddAlias(w http.ResponseWriter, r *http.Request) {
	// Aliases address the whole bank, so a tag-scoped caller may not change them.
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's aliases") {
		return
	}
	var req AliasRequest
	// The body is validated before the bank is resolved, matching the
	// framework's pre-handler validation: a malformed body wins over a missing
	// bank.
	object, err := decodeRequest(r, &req)
	if err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if req.Alias == nil {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body", "alias"}, object),
		}})
		return
	}
	// `primary` is a non-nullable bool in the reference: an explicit null is a
	// bool_type item rather than the silently-ignored zero value a bare bool
	// field would take.
	if value, present := object["primary"]; present && value == nil {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("bool_type", []string{"body", "primary"}, nil),
		}})
		return
	}
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	if writeIdentifierError(w, r, *req.Alias) {
		return
	}
	aliases, err := h.Store.AddAlias(r.Context(), bankID, *req.Alias, req.Primary)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.addAliasError(*req.Alias, err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, ToAliasResponse(bankID, aliases))
}

// PromoteAlias serves the alias display route: primary true displays the bank
// under the alias (demoting any previous display alias), primary false returns
// it to its own id without touching the other aliases.
func (h *Handlers) PromoteAlias(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's aliases") {
		return
	}
	// The body is validated before the bank is resolved, matching the
	// framework's pre-handler validation: a malformed body wins over a missing
	// bank. Only the canonical id is needed to report the result, so the
	// existence check follows the body's validation.
	bankID, ok := h.resolveAlias(w, r)
	if !ok {
		return
	}
	var req AliasUpdateRequest
	if err := httpx.DecodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if req.Primary == nil {
		httpx.WriteValidationFailure(w, primaryRequiredFailure())
		return
	}
	alias := r.PathValue("alias")
	aliases, err := h.Store.SetAliasPrimary(r.Context(), bankID, alias, *req.Primary)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.aliasOrWriteError(bankID, alias, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ToAliasResponse(bankID, aliases))
}

// addAliasError renders the reference's two alias-creation conflicts: a name
// that is the bank's own id (400) and a name already taken by another bank or
// alias (409).
func (h *Handlers) addAliasError(alias string, err error) error {
	switch {
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "A bank cannot be an alias of itself")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "'"+alias+"' is already taken by another bank or alias")
	}
	return h.mapWriteError(err)
}

// aliasOrWriteError names both the bank and the alias in the not-found detail
// the reference returns when a promote/demote/remove targets an alias the bank
// does not own: `Bank '<bank>' has no alias '<alias>'`.
func (h *Handlers) aliasOrWriteError(bankID, alias string, err error) error {
	if errors.Is(err, storage.ErrAliasNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, "Bank '"+bankID+"' has no alias '"+alias+"'")
	}
	return h.mapWriteError(err)
}

// primaryRequiredFailure reports the required primary field of the alias
// display body.
func primaryRequiredFailure() *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Missing([]string{"body", "primary"}, map[string]any{}),
	}}
}

// RemoveAlias serves the alias removal route.
func (h *Handlers) RemoveAlias(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's aliases") {
		return
	}
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	alias := r.PathValue("alias")
	aliases, err := h.Store.RemoveAlias(r.Context(), bankID, alias)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.aliasOrWriteError(bankID, alias, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ToAliasResponse(bankID, aliases))
}

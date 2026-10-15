package bankapi

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
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
	httpx.WriteJSONFor(w, r, http.StatusOK, ToAliasResponse(bankID, aliases))
}

// AddAlias serves the alias creation route. It returns the created status and
// the whole alias list.
func (h *Handlers) AddAlias(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	var req AliasRequest
	if err := httpx.DecodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if writeIdentifierError(w, r, req.Alias) {
		return
	}
	aliases, err := h.Store.AddAlias(r.Context(), bankID, req.Alias, req.Primary)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, ToAliasResponse(bankID, aliases))
}

// PromoteAlias serves the alias display route: primary true displays the bank
// under the alias (demoting any previous display alias), primary false returns
// it to its own id without touching the other aliases.
func (h *Handlers) PromoteAlias(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	var req AliasUpdateRequest
	if err := httpx.DecodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if req.Primary == nil {
		httpx.WriteValidationFor(w, r, primaryRequiredFailure())
		return
	}
	alias := r.PathValue("alias")
	aliases, err := h.Store.SetAliasPrimary(r.Context(), bankID, alias, *req.Primary)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ToAliasResponse(bankID, aliases))
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
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	alias := r.PathValue("alias")
	aliases, err := h.Store.RemoveAlias(r.Context(), bankID, alias)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ToAliasResponse(bankID, aliases))
}

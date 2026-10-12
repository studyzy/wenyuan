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
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body is not valid JSON"))
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

// PromoteAlias serves the alias display route, demoting any previous display
// alias.
func (h *Handlers) PromoteAlias(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	var req AliasUpdateRequest
	if r.Body != nil {
		_ = decodeBody(r, &req)
	}
	alias := r.PathValue("alias")
	aliases, err := h.Store.PromoteAlias(r.Context(), bankID, alias)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ToAliasResponse(bankID, aliases))
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

package bankapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// aliasItems is the model's verdict on an alias body: the members the body did
// not supply, the model's own type items, all in the order the model declares
// them. A member that is present but unreadable is a type item, not a missing
// one, and a null primary is a type item of the primary member.
func aliasItems(aliasSupplied bool, object any, decoded []byte, decodeErr error) []httpx.ValidationError {
	// A member the body carried is never missing, even when its value is one the
	// model cannot read: the reference answers a null alias with the type item
	// alone. A body that is not an object at all carries no members, so the
	// framework's whole-body item is its verdict, not a missing member.
	var members map[string]any
	if asObject, ok := object.(map[string]any); ok {
		members = asObject
	}
	var aliasMember []httpx.ValidationError
	if _, aliasCarried := members["alias"]; !aliasSupplied && !aliasCarried {
		aliasMember = []httpx.ValidationError{httpx.Missing([]string{"body", "alias"}, object)}
	}
	primary := []httpx.ValidationError{}
	if value, present := members["primary"]; present && value == nil {
		primary = append(primary, httpx.TypeMismatch("bool_type", []string{"body", "primary"}, nil))
	}
	if decodeErr != nil {
		var failure *httpx.ValidationFailure
		switch {
		case errors.As(decodeErr, &failure) && failure.Detail == "":
			primary = append(primary, failure.Errors...)
		default:
			primary = append(primary, httpx.BodyDecodeFailure(decodeErr, decoded).Errors...)
		}
	}
	// The two sources can name the same member; the model reports it once.
	seen := map[string]bool{}
	items := make([]httpx.ValidationError, 0, len(aliasMember)+len(primary))
	for _, item := range append(aliasMember, primary...) {
		key := item.Type + "|" + strings.Join(item.Loc, ".")
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, item)
	}
	return items
}

// aliasBodyObject renders a decoded alias body as the generic object an item
// echoes, so a missing member names the body the caller wrote.
func aliasBodyObject(decoded []byte, req *AliasRequest) (map[string]any, error) {
	object := map[string]any{}
	if len(decoded) == 0 {
		return object, nil
	}
	if err := decodeRawObject(decoded, object); err != nil {
		return nil, err
	}
	return object, nil
}

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
	body, readErr := io.ReadAll(io.LimitReader(r.Body, maxBankBodyBytes))
	if readErr != nil {
		httpx.WriteErrorFor(w, r, httpx.BodyDecodeFailure(readErr, body))
		return
	}
	if len(body) == 0 {
		// A request with no body at all is the framework's item for the body
		// parameter itself, not a syntax failure.
		httpx.WriteErrorFor(w, r, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, body))
		return
	}
	decoded, err := httpx.DecodeLax(body, &req)
	object, objectErr := aliasBodyObject(decoded, &req)
	trimmed := bytes.TrimSpace(body)
	if trimmed[0] != '{' {
		// The body is not an object: the framework refuses the parameter itself,
		// so there is no member to report.
		httpx.WriteErrorFor(w, r, httpx.BodyDecodeFailure(err, decoded))
		return
	}
	if objectErr != nil {
		// The body is not an object the model can read a field out of: the whole
		// body is the failure, so there is no member verdict to add to.
		httpx.WriteErrorFor(w, r, httpx.BodyDecodeFailure(err, decoded))
		return
	}
	// `primary` is a non-nullable bool in the reference: an explicit null is a
	// bool_type item rather than the silently-ignored zero value a bare bool
	// field would take. The reference's model declares alias before primary,
	// and one validation pass reports every rejected field in that order: a
	// body with both a missing alias and an unreadable primary answers with two
	// items, not with whichever was checked first.
	items := aliasItems(req.Alias != nil, object, decoded, err)
	if len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return
	}
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	if writeIdentifierError(w, r, *req.Alias) {
		return
	}
	if _, err := h.Store.AddAlias(r.Context(), bankID, *req.Alias, req.Primary); err != nil {
		httpx.WriteErrorFor(w, r, h.addAliasError(*req.Alias, err))
		return
	}
	// The list is read back rather than taken from the write, because the write
	// promoted the display alias in the same transaction: a creation that
	// invalidated the bank's previous primary would otherwise be answered with
	// the stale row it replaced.
	aliases, err := h.Store.ListAliases(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
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

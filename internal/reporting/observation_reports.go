package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// This file is the observation half of the reporting family: deleting and
// scoping observations, and the consolidation passes that rewrite them.

// DeleteObservations removes a bank's observations.
func (h *Handlers) DeleteObservations(w http.ResponseWriter, r *http.Request) {
	// A bank-wide clear reaches every observation regardless of tags, so a
	// tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "clear the bank's observations") {
		return
	}
	// The reference takes no bank-existence check on this route: an unknown bank
	// simply matches no rows and clears zero observations, so a delete against a
	// missing bank is a 200 with deleted_count 0 rather than a 404.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	removed, err := h.Store.DeleteObservations(r.Context(), bankID, "")
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// Clearing observations changes the counts the bank summary reports, so the
	// cached summary is dropped; the reference invalidates it here too, or a poll
	// in the next TTL window would serve observations that no longer exist. The
	// memory-scoped clear below deliberately does not, matching the reference.
	invalidateBankStats(r.Context(), h.Store, bankID)
	httpx.WriteJSON(w, http.StatusOK, DeleteResponse{
		Success: true, Message: fmt.Sprintf("Cleared %d observations", removed), DeletedCount: removed,
	})
}

// ObservationScopes lists a bank's observation scopes.
func (h *Handlers) ObservationScopes(w http.ResponseWriter, r *http.Request) {
	// The reference validates the query before the handler runs, so an
	// out-of-range limit/offset is a 422 even for an unknown bank — and every
	// rejected parameter is reported, not just the first.
	limit, offset, ok := parseObservationScopeQuery(w, r)
	if !ok {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// The caller's resolved read scope confines the histogram, so a scoped
	// reader pages over only the scopes it may see and the total agrees.
	page, err := h.Store.ListObservationScopes(r.Context(), bankID, memoryops.ScopeFromRequest(r), limit, offset)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	scopes := make([]ObservationScopeItem, 0, len(page.Scopes))
	for _, scope := range page.Scopes {
		tags := scope.Tags
		if tags == nil {
			tags = []string{}
		}
		scopes = append(scopes, ObservationScopeItem{Tags: tags, Count: scope.Count})
	}
	httpx.WriteJSON(w, http.StatusOK, ObservationScopesResponse{
		Scopes: scopes, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// ConsolidationPreview reports which of the bank's existing observation scopes
// each draft consolidation strategy would apply to.
func (h *Handlers) ConsolidationPreview(w http.ResponseWriter, r *http.Request) {
	// The reference validates the body before the handler body runs and the
	// bank existence check lives inside the engine call, so a malformed body is
	// rejected before an unknown bank is reported.
	body, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	httpx.RecordUnknownBodyFields(r, body, &consolidationScopesPreviewRequest{})
	if len(body) == 0 {
		writeRequiredBodyFailure(w, r, httpx.ErrEmptyBody)
		return
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, body))
		return
	}
	// A body that is the JSON literal null reads as an absent required body: the
	// framework reports the field missing rather than a value it cannot extract
	// fields from. A list or scalar body keeps the model_attributes_type item.
	if raw == nil {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body"}, nil),
		}})
		return
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			consolidationModelAttributesType([]string{"body"}, raw),
		}})
		return
	}
	req, failures := decodeConsolidationPreview(obj)
	if len(failures) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: failures})
		return
	}

	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// The preview is built from the same histogram, so it is confined by the
	// same scope (the reference delegates to the scoped scope list,
	// engine/memory_engine.py:12394-12405).
	scopePage, err := h.Store.ListObservationScopes(r.Context(), bankID, memoryops.ScopeFromRequest(r), 10000, 0)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	scopes := make([]scopePreview, 0, len(scopePage.Scopes))
	for _, scope := range scopePage.Scopes {
		tags := scope.Tags
		if tags == nil {
			tags = []string{}
		}
		scopes = append(scopes, scopePreview{Tags: tags, Count: scope.Count})
	}
	preview := buildConsolidationPreview(req.Strategies, scopes, req.SampleLimit, scopePage.Total <= len(scopes))
	httpx.WriteJSON(w, http.StatusOK, preview)
}

// ConsolidationRecover retries a bank's failed consolidations.
func (h *Handlers) ConsolidationRecover(w http.ResponseWriter, r *http.Request) {
	// A bank-wide consolidation retry reaches every memory regardless of tags,
	// so a tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "retry the bank's failed consolidation") {
		return
	}
	// The reference's retry is one UPDATE filtered by bank_id: it never checks
	// that the bank exists, so an unknown bank retries nothing and answers 200
	// with a zero count rather than 404ing.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	retried, err := h.Store.RecoverConsolidations(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, RecoverConsolidationResponse{RetriedCount: retried})
}

// Consolidate queues a consolidation pass for one bank.
func (h *Handlers) Consolidate(w http.ResponseWriter, r *http.Request) {
	// The reference validates the optional body before the handler runs, so a
	// malformed or wrong-shaped body is a 422 even for an unknown bank.
	scopes, scoped, ok := parseConsolidationRequest(w, r)
	if !ok {
		return
	}
	// A bank-wide consolidation reaches every memory regardless of tags, so a
	// tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "run consolidation on the bank") {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var op storage.Operation
	var deduplicated bool
	var err error
	if scoped {
		op, deduplicated, err = enqueueScopedConsolidation(r.Context(), h.Store, bankID, scopes)
	} else if enqueuer, ok := h.Store.(callerRequestedConsolidationEnqueuer); ok {
		// An explicit /consolidate starts a chain that keeps draining past the
		// auto-consolidation gate: the operation's payload carries
		// caller_requested (memory_engine.py:23716-23717, api/http.py:9992).
		op, deduplicated, err = enqueuer.EnqueueCallerRequestedConsolidation(r.Context(), bankID)
	} else {
		op, deduplicated, err = h.Store.EnqueueConsolidation(r.Context(), bankID)
	}
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ConsolidationResponse{
		OperationID: op.OperationID, Deduplicated: deduplicated,
	})
}

// scopedConsolidationEnqueuer is the optional capability a store provides to
// submit a scoped consolidation whose scope list may be empty.
type scopedConsolidationEnqueuer interface {
	EnqueueConsolidationScoped(ctx context.Context, bankID string, observationScopes [][]string) (storage.Operation, bool, error)
}

// callerRequestedConsolidationEnqueuer is the optional capability a store
// provides to submit an explicit consolidation whose task payload carries
// caller_requested, the way the reference's submit_async_consolidation does
// (memory_engine.py:23716-23717).
type callerRequestedConsolidationEnqueuer interface {
	EnqueueCallerRequestedConsolidation(ctx context.Context, bankID string) (storage.Operation, bool, error)
}

// enqueueScopedConsolidation submits a scoped consolidation pass. A store that
// implements the scoped capability is asked directly, so an explicit but empty
// scope list stays a scoped (never deduplicated) run; a store without it falls
// back to the variadic enqueue with a non-nil list, which carries the scopes and
// likewise never deduplicates.
func enqueueScopedConsolidation(ctx context.Context, store storage.Store, bankID string, scopes [][]string) (storage.Operation, bool, error) {
	if enqueuer, ok := store.(scopedConsolidationEnqueuer); ok {
		return enqueuer.EnqueueConsolidationScoped(ctx, bankID, scopes)
	}
	if scopes == nil {
		scopes = [][]string{}
	}
	return store.EnqueueConsolidation(ctx, bankID, scopes...)
}

// invalidateBankStats drops a bank's cached summary through the optional
// capability a store provides, so the next stats read recomputes.
func invalidateBankStats(ctx context.Context, store storage.Store, bankID string) {
	if invalidator, ok := store.(interface {
		InvalidateBankStats(ctx context.Context, bankID string)
	}); ok {
		invalidator.InvalidateBankStats(ctx, bankID)
	}
}

// consolidationRequest is the consolidation route's optional body: the tag
// scopes to consolidate, each scope a list of tags. The declaration is the
// same one the served contract publishes, so the shared decoder's walk reports
// a rejected body with the reference's own items.
type consolidationRequest struct {
	ObservationScopes [][]string `json:"observation_scopes"`
}

// parseConsolidationRequest reads the optional consolidation body and returns
// its observation scopes together with whether the body was a scoped submit. An
// absent or null body yields no scopes and reports scoped=false (a full-bank
// pass); a body that names observation_scopes — even an empty list — reports
// scoped=true, since the reference treats a present list as a targeted run that
// must not deduplicate against a pending full-bank pass. A body the reference's
// model rejects is answered with that model's items through the shared decoder.
func parseConsolidationRequest(w http.ResponseWriter, r *http.Request) ([][]string, bool, bool) {
	body, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return nil, false, false
	}
	trimmed := strings.TrimSpace(string(body))
	// The declared body is optional and nullable, so an absent body and the
	// JSON literal null both mean "the whole bank" rather than a missing field.
	if trimmed == "" || trimmed == "null" {
		return nil, false, true
	}
	// The walk reports every value the model refuses — a body that is not an
	// object, a member that is not a list of lists of strings — with the
	// vocabulary, location and input the reference serves.
	if items := httpx.ValidationItemsAgainstRequest(r, &consolidationRequest{}, body); len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return nil, false, false
	}
	var req consolidationRequest
	if _, err := httpx.DecodeLax(body, &req); err != nil {
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, body))
		return nil, false, false
	}
	// A present list is a scoped submit whether or not it is empty: an empty
	// list is a targeted run matching no memory, which the reference submits
	// without deduplication, not a full-bank sweep. A null or absent member
	// decodes to a nil slice and stays a full-bank pass.
	return req.ObservationScopes, req.ObservationScopes != nil, true
}

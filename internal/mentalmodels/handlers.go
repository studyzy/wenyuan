package mentalmodels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// DryRunFunc previews a refresh without persisting anything. The composition
// root supplies it, because the preview runs the same reflect pipeline a real
// refresh does and so needs the model-provider boundary.
type DryRunFunc func(ctx context.Context, bankID, modelID string) (*DryRunResult, error)

// Handlers serves the mental-model route family.
type Handlers struct {
	Store storage.Store
	Now   func() time.Time
	// DryRun previews a refresh without persisting; when nil the route reports
	// that the preview is unavailable rather than inventing a result.
	DryRun DryRunFunc
	// RefuseWithoutRefreshModel makes the refresh route refuse with the
	// reference's message when the deployment is configured with no LLM
	// provider (WENYUAN_API_LLM_PROVIDER=none): the reference rejects refresh
	// work before any lookup rather than queuing it. False (the zero value)
	// leaves the route queuing, which is what a handler set assembled without a
	// provider expects.
	RefuseWithoutRefreshModel bool
}

// ErrNoRefreshModel is the refusal the reference returns for a mental-model
// refresh on a provider-"none" deployment; the message is the reference's,
// verbatim.
//
//nolint:staticcheck // the reference's own wording, capitalised and sentence-final
var ErrNoRefreshModel = errors.New("Mental model refresh requires an LLM provider. Current provider is set to 'none'. Set WENYUAN_API_LLM_PROVIDER to a real provider (e.g., openai, anthropic, gemini).")

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers {
	return &Handlers{Store: store, Now: func() time.Time { return time.Now().UTC() }}
}

// List returns the bank's mental models, filtered by tags, at the requested
// detail level and page.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.pathBankID(w, r)
	if !ok {
		return
	}
	// The list route is the one route in the family that probes the bank: the
	// reference's list_mental_models calls _require_bank_exists, so an unknown
	// bank is a bank-level 404 rather than an empty page.
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	detail, ok := detailParam(w, r, "metadata")
	if !ok {
		return
	}
	mode, ok := tagsMatchParam(w, r)
	if !ok {
		return
	}
	limit, offset, ok := pageParams(w, r)
	if !ok {
		return
	}
	excludePages, ok := excludeKnowledgePagesParam(w, r)
	if !ok {
		return
	}
	records, err := h.Store.ListMentalModels(r.Context(), bankID, "")
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	tags := httpx.QueryValues(r, "tags")
	items := make([]Model, 0, len(records))
	for _, record := range records {
		// A page's backing model is a mental model, so it is listed unless the
		// caller asked for standalone models only — and the exclusion happens
		// before the totals, so `total` counts what the filter matched.
		if excludePages && record.IsKnowledgePage != nil && *record.IsKnowledgePage {
			continue
		}
		if len(tags) > 0 && !matchesTags(record.Tags, tags, mode) {
			continue
		}
		model := toModel(record)
		stale := h.Store.IsStale(r.Context(), bankID, record)
		model.IsStale = &stale
		applyDetail(&model, detail)
		items = append(items, model)
	}
	total := len(items)
	items = pageSlice(items, offset, limit)
	httpx.WriteJSON(w, http.StatusOK, ListResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

// Get returns one mental model.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	detail, ok := detailParam(w, r, "full")
	if !ok {
		return
	}
	record, err := h.Store.GetMentalModel(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.modelError(id, err))
		return
	}
	model := toModel(record)
	// is_stale is populated only at full detail on a single read, matching the
	// reference's get_mental_model; the list route computes it at every level.
	if detail == "full" {
		stale := h.Store.IsStale(r.Context(), bankID, record)
		model.IsStale = &stale
	}
	applyDetail(&model, detail)
	httpx.WriteJSON(w, http.StatusOK, model)
}

// Create adds a mental model. An unknown bank is created first, matching the
// reference's lazy bank creation.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.pathBankID(w, r)
	if !ok {
		return
	}
	var body CreateRequest
	object, bodyErrors, failure := requestItems(r, &body)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	decodeInto(object, &body)
	// The request model's contract supplies the items for its members — including
	// name and source_query, which are required (a pydantic `str` with no
	// default, so an absent key or an explicit null is the framework's
	// missing-field item) — in the order the framework files them at this route:
	// name, source_query, tags, then the trigger. The shared walk files no numeric
	// bound, so max_tokens' range is checked here and appended after those and
	// before the trigger's items, which is where the reference puts it.
	maxTokens := rawMaxTokens(body.MaxTokens)
	// The bound check runs on the value the coercion produced but NAMES the value
	// the client sent, so the message does not report true as 1.
	if items := maxTokensItems(maxTokens, object["max_tokens"]); len(items) > 0 {
		bodyErrors = append(bodyErrors, items...)
	}
	trigger, triggerFailure := parseTrigger(body.Trigger, true)
	if triggerFailure != nil {
		bodyErrors = append(bodyErrors, triggerFailure.Errors...)
	}
	if len(bodyErrors) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: bodyErrors})
		return
	}
	tokens := defaultMentalModelTokens
	if maxTokens != nil {
		tokens = *maxTokens
	}
	id := newID()
	if body.ID != nil && strings.TrimSpace(*body.ID) != "" {
		// The reference declares id a plain string with no shape validator, so
		// only uniqueness is enforced; the stored value is the client's.
		id = *body.ID
		if _, err := h.Store.GetMentalModel(r.Context(), bankID, id); err == nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict,
				fmt.Sprintf("Mental model '%s' already exists in bank '%s'", id, bankID)))
			return
		} else if !errors.Is(err, storage.ErrMentalModelNotFound) {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
	}
	// The bank is created lazily inside the same flow, matching the reference.
	if _, ok := h.requireOrCreateBank(w, r); !ok {
		return
	}
	// A create always stores subtype 'pinned' and a single-space description;
	// neither is client-supplied.
	record, err := h.Store.PutMentalModel(r.Context(), storage.MentalModel{
		ID:          id,
		BankID:      bankID,
		Subtype:     storage.SubtypePinned,
		Name:        *body.Name,
		Description: " ",
		SourceQuery: *body.SourceQuery,
		Content:     "",
		Tags:        derefTags(body.Tags),
		MaxTokens:   tokens,
		Trigger:     trigger,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// With no LLM provider the reference submits the refresh this route needs
	// and does not catch the refusal, so the create answers the framework's
	// internal-error body (500) with the refusal's message — and the model it
	// just stored stays stored.
	if h.RefuseWithoutRefreshModel {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, ErrNoRefreshModel.Error()))
		return
	}
	// A refresh is always queued, so the acknowledgement always carries the
	// operation id the caller polls, even for an empty source query.
	opID, err := h.enqueueModelRefresh(r, bankID, record.ID, record.Name, false)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, CreateResponse{MentalModelID: record.ID, OperationID: opID})
}

// derefTags returns the supplied tag list, or an empty slice when absent.
func derefTags(tags *[]string) []string {
	if tags == nil {
		return []string{}
	}
	return *tags
}

// refreshModeFull is the one-run mode override a refresh request may state, and
// the value the worker reads out of the queued payload.
const refreshModeFull = "full"

// enqueueModelRefresh queues a refresh of a mental model and returns the
// operation identifier. It records the model's identifier and name in the
// operation's result metadata, which is what the operation-status route reads
// back as mental_model_id, matching the reference.
//
// full asks for a one-run full refresh. The payload carries the mode override
// the worker reads, and a submit that folds into an already-queued plain refresh
// upgrades that one rather than dropping the request: a full refresh covers what
// a delta would have done. A fold into a *processing* refresh changes nothing —
// that run has already chosen its mode, so a plain submit is left as one.
func (h *Handlers) enqueueModelRefresh(r *http.Request, bankID, modelID, name string, full bool) (string, error) {
	ctx := r.Context()
	if !full {
		return h.enqueueRefresh(ctx, bankID, modelID, name, false)
	}
	// The fold-and-upgrade is decided under the same lock a deduplicated enqueue
	// takes, where the store offers one, so no submit can queue a second refresh
	// between the fold and the amendment. The in-memory store offers no such
	// lock (its enqueue already holds the store mutex, so a second lock would
	// deadlock) and runs the two steps directly; there the window between them
	// lets a concurrent plain submit fold into the refresh just before this
	// promotion, which is still the intended outcome — it is made full, not
	// dropped.
	var operationID string
	enqueue := func() error {
		var err error
		operationID, err = h.enqueueRefresh(ctx, bankID, modelID, name, true)
		return err
	}
	if locker, locked := h.Store.(storage.LifecycleLockedStore); locked {
		if err := locker.WithOperationSubjectLock(ctx, bankID, storage.OperationTypeRefreshMentalModel, enqueue); err != nil {
			return "", err
		}
	} else if err := enqueue(); err != nil {
		return "", err
	}
	if _, err := storage.PromoteQueuedRefreshToFull(ctx, h.Store, bankID, operationID); err != nil {
		return "", err
	}
	return operationID, nil
}

// enqueueRefresh performs one deduplicated enqueue.
func (h *Handlers) enqueueRefresh(ctx context.Context, bankID, modelID, name string, full bool) (string, error) {
	payload := map[string]any{storage.MentalModelIDPayloadKey: modelID}
	if full {
		payload[storage.RefreshModePayloadKey] = refreshModeFull
	}
	result, err := h.Store.EnqueueOperation(ctx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          payload,
			ResultMetadata:   map[string]any{storage.MentalModelIDPayloadKey: modelID, "name": name},
			SerializationKey: storage.MentalModelSerializationKey(modelID),
		},
		DedupeByPayloadKey: storage.MentalModelIDPayloadKey,
	})
	if err != nil {
		return "", err
	}
	return result.Operation.OperationID, nil
}

// Update applies a partial update.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	var body UpdateRequest
	object, bodyErrors, failure := requestItems(r, &body)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	decodeInto(object, &body)
	// Like every request model in this family, the reference reports every
	// failing field in one envelope; the walk supplies them in declaration order.
	// Unlike the create route, a patch files its items in strict declaration
	// order — name, source_query, max_tokens, tags, trigger — so the bound is
	// placed ahead of the walk's tags item.
	updateTokens := rawMaxTokens(body.MaxTokens)
	bodyErrors = insertBeforeLoc(bodyErrors, []string{"body", "tags"},
		maxTokensItems(updateTokens, object["max_tokens"]))
	var triggerPatch map[string]any
	// Unlike the create route, a trigger member the body states as JSON null is not
	// a supplied value here: the patch model makes the whole member optional, and a
	// null reads as "not supplied", so only the members a trigger object carries can
	// change anything. `{"trigger": null}` alone is therefore a patch that names no
	// column — the reference answers that with 404 on the model, exactly like `{}`.
	hasTrigger := len(body.Trigger) > 0 && !isJSONNull(body.Trigger)
	if hasTrigger {
		parsed, failure := parseTrigger(body.Trigger, false)
		if failure != nil {
			bodyErrors = append(bodyErrors, failure.Errors...)
		}
		triggerPatch = parsed
	}
	if len(bodyErrors) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: bodyErrors})
		return
	}
	patch := storage.MentalModelPatch{
		Name:        body.Name,
		SourceQuery: body.SourceQuery,
		Tags:        body.Tags,
		MaxTokens:   updateTokens,
	}
	if hasTrigger {
		current, err := h.Store.GetMentalModel(r.Context(), bankID, id)
		if err != nil {
			httpx.WriteErrorFor(w, r, h.modelError(id, err))
			return
		}
		merged := mergeTrigger(current.Trigger, triggerPatch)
		patch.Trigger = &merged
	}
	// The reference builds a dynamic UPDATE and answers 404 when it names no
	// column: a patch that sets nothing is indistinguishable from a missing row.
	if patch.Name == nil && patch.SourceQuery == nil && patch.Tags == nil &&
		patch.MaxTokens == nil && patch.Trigger == nil {
		httpx.WriteErrorFor(w, r, h.modelError(id, storage.ErrMentalModelNotFound))
		return
	}
	record, err := h.Store.UpdateMentalModel(r.Context(), bankID, id, patch)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.modelError(id, err))
		return
	}
	model := toModel(record)
	applyDetail(&model, "full")
	httpx.WriteJSON(w, http.StatusOK, model)
}

// Delete removes a mental model.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteMentalModel(r.Context(), bankID, id); err != nil {
		httpx.WriteErrorFor(w, r, h.modelError(id, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, deleteResponse{Status: "deleted"})
}

// deleteResponse is the delete acknowledgement the reference returns.
type deleteResponse struct {
	Status string `json:"status"`
}

// Clear empties a mental model's content while keeping the record.
func (h *Handlers) Clear(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	record, err := h.Store.ClearMentalModel(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.modelError(id, err))
		return
	}
	// The clear leaves the rest of the row alone — including reflect_response,
	// which the reference's clear does not touch either — so the answer carries
	// the same detail level its update and refresh do.
	model := toModel(record)
	applyDetail(&model, "full")
	httpx.WriteJSON(w, http.StatusOK, model)
}

// History returns the recorded refreshes of one mental model as a bare JSON
// array, most recent first, using the reference's entry keys.
func (h *Handlers) History(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	entries, err := h.Store.MentalModelHistory(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.modelError(id, err))
		return
	}
	out := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, HistoryEntry{
			PreviousContent:         entry.PreviousContent,
			PreviousReflectResponse: entry.PreviousReflectResponse,
			ChangedAt:               httpx.FormatTimestamp(entry.ChangedAt),
			Kind:                    entry.Kind,
			Outcome:                 entry.Outcome,
			FailureReason:           entry.FailureReason,
			ErrorMessage:            entry.ErrorMessage,
		})
	}
	// The reference returns the bare list, with explicit nulls on a version
	// snapshot's failure fields, so nulls are preserved here.
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Refresh enqueues a refresh of a mental model and reports the accepted
// operation, which the caller polls to completion.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	// A deployment with no LLM provider refuses refresh work before any lookup,
	// matching the reference: the gate precedes the model's existence check, so
	// an unknown model is the same refusal rather than a 404.
	if h.RefuseWithoutRefreshModel {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, ErrNoRefreshModel.Error()))
		return
	}
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	// The body is optional, and the reference reads it after the model exists, so
	// a malformed body on an unknown model is still the model's 404.
	record, err := h.Store.GetMentalModel(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.refreshError(bankID, id, err))
		return
	}
	mode, ok := refreshModeParam(w, r)
	if !ok {
		return
	}
	opID, err := h.enqueueModelRefresh(r, bankID, id, record.Name, mode == refreshModeFull)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, SubmitResponse{OperationID: opID, Status: "queued"})
}

// DryRunRefresh reports what a refresh would do without applying it.
func (h *Handlers) DryRunRefresh(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	if h.DryRun == nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, "dry-run refresh is not configured"))
		return
	}
	// The reference resolves the model before it checks provider availability,
	// so a missing model is a 404 even when no LLM is configured.
	if h.RefuseWithoutRefreshModel {
		if _, err := h.Store.GetMentalModel(r.Context(), bankID, id); err != nil {
			httpx.WriteErrorFor(w, r, h.dryRunError(id, err))
			return
		}
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, ErrNoRefreshModel.Error()))
		return
	}
	result, err := h.DryRun(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.dryRunError(id, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// BindRoutes registers the family's route table.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModels), httpx.QueryParams(h.List,
		"tags", "tags_match", "detail", "limit", "offset", "exclude_knowledge_pages"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModels), h.Create)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModelMember), httpx.QueryParams(h.Get, "detail"))
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixMentalModelMember), h.Update)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixMentalModelMember), h.Delete)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModelHistory), h.History)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModelRefresh), h.Refresh)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModelDryRun), h.DryRunRefresh)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModelClear), h.Clear)
}

// member reads the bank and the mental-model identifier from the path.
func (h *Handlers) member(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.pathBankID(w, r)
	if !ok {
		return "", "", false
	}
	id := strings.TrimSpace(r.PathValue("mental_model_id"))
	if id == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Mental model '' not found"))
		return "", "", false
	}
	return bankID, id, true
}

// pathBankID reads the bank identifier from the path. The family never probes
// bank existence: like the reference, an unknown bank is indistinguishable from
// a missing model (the model lookup itself reports not-found).
func (h *Handlers) pathBankID(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// modelError names the requested model in a not-found detail, matching the
// reference's `Mental model '<id>' not found`.
func (h *Handlers) modelError(id string, err error) *httpx.HTTPError {
	if errors.Is(err, storage.ErrMentalModelNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, fmt.Sprintf("Mental model '%s' not found", id))
	}
	return asHTTPError(err)
}

// refreshError renders the refresh route's not-found detail, which names both
// the model and the bank: `Mental model <id> not found in bank <bank>`.
func (h *Handlers) refreshError(bankID, id string, err error) *httpx.HTTPError {
	if errors.Is(err, storage.ErrMentalModelNotFound) || errors.Is(err, storage.ErrBankNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, fmt.Sprintf("Mental model %s not found in bank %s", id, bankID))
	}
	return asHTTPError(err)
}

// dryRunError renders the dry-run route's not-found detail, which names only the
// model: `Mental model <id> not found`.
func (h *Handlers) dryRunError(id string, err error) *httpx.HTTPError {
	if errors.Is(err, storage.ErrMentalModelNotFound) || errors.Is(err, storage.ErrBankNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, fmt.Sprintf("Mental model %s not found", id))
	}
	return asHTTPError(err)
}

// requireOrCreateBank resolves a bank, creating it when unknown so a create on
// a fresh bank id succeeds, matching the reference's lazy bank creation.
func (h *Handlers) requireOrCreateBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err == nil {
		return bankID, true
	} else if !errors.Is(err, storage.ErrBankNotFound) {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return "", false
	}
	if _, err := h.Store.CreateBank(r.Context(), storage.Bank{BankID: bankID, Name: bankID}); err != nil {
		if !errors.Is(err, storage.ErrOperationConflict) {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return "", false
		}
	}
	return bankID, true
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrMentalModelNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "mental model not found")
	case errors.Is(err, storage.ErrInvalidMentalModelSubtype):
		return httpx.NewHTTPError(httpx.KindBadRequest, "unknown mental model subtype")
	case errors.Is(err, storage.ErrEmbeddingDimensions):
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias")
	case errors.Is(err, storage.ErrReflectModelUnavailable):
		// The dry-run route surfaces the reflection boundary: a deployment with no
		// usable model refused the preview, which the reference answers 400 for.
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}

// newID builds an opaque identifier, matching the reference's mm-prefixed shape.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("mm-%d", time.Now().UnixNano())
	}
	return "mm-" + hex.EncodeToString(raw[:])
}

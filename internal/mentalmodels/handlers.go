package mentalmodels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
}

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers {
	return &Handlers{Store: store, Now: func() time.Time { return time.Now().UTC() }}
}

// List returns the bank's mental models, filtered by tags, at the requested
// detail level and page.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// The route has no subtype filter: the reference lists every model of the
	// bank and narrows by tags only.
	detail := detailParam(httpx.Query(r, "detail"), "metadata")
	records, err := h.Store.ListMentalModels(r.Context(), bankID, "")
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	tags := httpx.QueryValues(r, "tags")
	mode := strings.TrimSpace(httpx.Query(r, "tags_match"))
	items := make([]Model, 0, len(records))
	for _, record := range records {
		if len(tags) > 0 && !matchesTags(record.Tags, tags, mode) {
			continue
		}
		model := toModel(record)
		model.IsStale = h.Store.IsStale(r.Context(), bankID, record)
		applyDetail(&model, detail)
		items = append(items, model)
	}
	total := len(items)
	limit, offset := pageParams(r, 100)
	items = pageSlice(items, offset, limit)
	httpx.WriteJSONFor(w, r, http.StatusOK, ListResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

// Get returns one mental model.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	record, err := h.Store.GetMentalModel(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	model := toModel(record)
	model.IsStale = h.Store.IsStale(r.Context(), bankID, record)
	applyDetail(&model, detailParam(httpx.Query(r, "detail"), "full"))
	httpx.WriteJSONFor(w, r, http.StatusOK, model)
}

// mentalModelIDPattern is the shape a client-supplied identifier must have:
// alphanumeric lowercase with hyphens, starting with a letter or digit.
var mentalModelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Create adds a mental model. An unknown bank is created first, matching the
// reference's lazy bank creation.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireOrCreateBank(w, r)
	if !ok {
		return
	}
	var body CreateRequest
	if failure := decodeBody(r, &body); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	subtype := storage.MentalModelSubtype(strings.TrimSpace(body.Subtype))
	if subtype == "" {
		subtype = storage.SubtypePinned
	}
	if !storage.ValidMentalModelSubtype(subtype) {
		httpx.WriteValidationFor(w, r, bodyValueError("subtype", body.Subtype, "unknown mental model subtype"))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.WriteValidationFor(w, r, missingBodyField("name"))
		return
	}
	trigger, failure := parseTrigger(body.Trigger, true)
	if failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id != "" {
		if !mentalModelIDPattern.MatchString(id) {
			httpx.WriteValidationFor(w, r, bodyValueError("id", id,
				fmt.Sprintf("Mental model id '%s' must be alphanumeric lowercase with hyphens, starting with a letter or digit.", id)))
			return
		}
		if _, err := h.Store.GetMentalModel(r.Context(), bankID, id); err == nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict,
				fmt.Sprintf("Mental model '%s' already exists in bank '%s'", id, bankID)))
			return
		} else if !errors.Is(err, storage.ErrMentalModelNotFound) {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
	} else {
		id = newID()
	}
	record, err := h.Store.PutMentalModel(r.Context(), storage.MentalModel{
		ID:          id,
		BankID:      bankID,
		Subtype:     subtype,
		Name:        body.Name,
		Description: body.Description,
		SourceQuery: body.SourceQuery,
		Content:     body.Content,
		Tags:        body.Tags,
		MaxTokens:   body.MaxTokens,
		Trigger:     trigger,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	response := CreateResponse{MentalModelID: record.ID}
	// A model with a source query is generated in the background, so the
	// create returns the queued operation rather than waiting on a model call.
	if strings.TrimSpace(record.SourceQuery) != "" {
		opID, err := h.enqueueModelRefresh(r, bankID, record.ID)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		response.OperationID = opID
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, response)
}

// enqueueModelRefresh queues a refresh of a mental model and returns the
// operation identifier, or an empty string when there is nothing to run.
func (h *Handlers) enqueueModelRefresh(r *http.Request, bankID, modelID string) (string, error) {
	result, err := h.Store.EnqueueOperation(r.Context(), storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          map[string]any{storage.MentalModelIDPayloadKey: modelID},
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
	if failure := decodeBody(r, &body); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	patch := storage.MentalModelPatch{
		Name:        body.Name,
		Description: body.Description,
		SourceQuery: body.SourceQuery,
		Content:     body.Content,
		Tags:        body.Tags,
		MaxTokens:   body.MaxTokens,
	}
	if len(body.Trigger) > 0 && string(body.Trigger) != "null" {
		triggerPatch, failure := parseTrigger(body.Trigger, false)
		if failure != nil {
			httpx.WriteValidationFor(w, r, failure)
			return
		}
		current, err := h.Store.GetMentalModel(r.Context(), bankID, id)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		merged := mergeTrigger(current.Trigger, triggerPatch)
		patch.Trigger = &merged
	}
	record, err := h.Store.UpdateMentalModel(r.Context(), bankID, id, patch)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toModel(record))
}

// Delete removes a mental model.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteMentalModel(r.Context(), bankID, id); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, deleteResponse{Status: "deleted"})
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
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toModel(record))
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
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, HistoryEntry{
			PreviousContent:         entry.PreviousContent,
			PreviousReflectResponse: entry.PreviousReflectResponse,
			ChangedAt:               entry.ChangedAt,
			Kind:                    entry.Kind,
			Outcome:                 entry.Outcome,
			FailureReason:           entry.FailureReason,
			ErrorMessage:            entry.ErrorMessage,
		})
	}
	// The reference returns the bare list, with explicit nulls on a version
	// snapshot's failure fields, so nulls are preserved here.
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, out)
}

// Refresh enqueues a refresh of a mental model and reports the accepted
// operation, which the caller polls to completion.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetMentalModel(r.Context(), bankID, id); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	opID, err := h.enqueueModelRefresh(r, bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, SubmitResponse{OperationID: opID, Status: "queued"})
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
	result, err := h.DryRun(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, result)
}

// BindRoutes registers the family's route table.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModels), httpx.QueryParams(h.List,
		"subtype", "tags", "tags_match", "detail", "limit", "offset"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModels), h.Create)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModelMember), httpx.QueryParams(h.Get, "detail"))
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixMentalModelMember), h.Update)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixMentalModelMember), h.Delete)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModelHistory), h.History)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModelRefresh), h.Refresh)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModelDryRun), h.DryRunRefresh)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModelClear), h.Clear)
}

// member resolves the bank and the mental-model identifier.
func (h *Handlers) member(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return "", "", false
	}
	id := strings.TrimSpace(r.PathValue("mental_model_id"))
	if id == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "mental model not found"))
		return "", "", false
	}
	return bankID, id, true
}

// requireBank reports not-found for an unknown bank, so every route in the
// family authorizes the same way.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
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
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
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

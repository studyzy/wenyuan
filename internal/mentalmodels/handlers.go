package mentalmodels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// Handlers serves the mental-model route family.
type Handlers struct {
	Store storage.Store
	Now   func() time.Time
}

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers {
	return &Handlers{Store: store, Now: func() time.Time { return time.Now().UTC() }}
}

// List returns the bank's mental models, optionally narrowed to a subtype.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	subtype := storage.MentalModelSubtype(strings.TrimSpace(r.URL.Query().Get("subtype")))
	if subtype != "" && !storage.ValidMentalModelSubtype(subtype) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "unknown mental model subtype"))
		return
	}
	records, err := h.Store.ListMentalModels(r.Context(), bankID, subtype)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	models := make([]Model, 0, len(records))
	for _, record := range records {
		models = append(models, toModel(record))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ListResponse{BankID: bankID, Models: models})
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
	httpx.WriteJSONFor(w, r, http.StatusOK, toModel(record))
}

// Create adds a mental model.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body CreateRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	subtype := storage.MentalModelSubtype(strings.TrimSpace(body.Subtype))
	if subtype == "" {
		subtype = storage.SubtypePinned
	}
	if !storage.ValidMentalModelSubtype(subtype) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "unknown mental model subtype"))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "name is required"))
		return
	}
	record, err := h.Store.PutMentalModel(r.Context(), storage.MentalModel{
		ID:          newID(),
		BankID:      bankID,
		Subtype:     subtype,
		Name:        body.Name,
		Description: body.Description,
		SourceQuery: body.SourceQuery,
		Content:     body.Content,
		Tags:        body.Tags,
		MaxTokens:   body.MaxTokens,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, toModel(record))
}

// Update applies a partial update.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	var body UpdateRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	record, err := h.Store.UpdateMentalModel(r.Context(), bankID, id, storage.MentalModelPatch{
		Name:        body.Name,
		Description: body.Description,
		SourceQuery: body.SourceQuery,
		Content:     body.Content,
		Tags:        body.Tags,
		MaxTokens:   body.MaxTokens,
	})
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
	w.WriteHeader(http.StatusNoContent)
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

// History returns the recorded refreshes of one mental model.
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
			RefreshedAt: entry.RefreshedAt,
			SourceQuery: entry.SourceQuery,
			Content:     entry.Content,
			Reason:      entry.Reason,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, HistoryResponse{BankID: bankID, MentalModel: id, Entries: out})
}

// Refresh enqueues a refresh of a mental model.
//
// The refresh itself runs as a background operation rather than in this
// request, because it regenerates the model's content by running its source
// query through the reflection loop — a model call whose duration is not
// bounded by anything a request should wait on. The response reports the
// accepted operation, and the model's current state, so a caller can poll the
// operation to completion.
//
// A model with no source query has nothing to re-run, so the refresh reports
// that reason rather than queueing work that would immediately refuse.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	h.refresh(w, r, false)
}

// DryRunRefresh reports what a refresh would do without queueing it.
func (h *Handlers) DryRunRefresh(w http.ResponseWriter, r *http.Request) {
	h.refresh(w, r, true)
}

// refresh implements both the enqueueing and the dry-run form.
func (h *Handlers) refresh(w http.ResponseWriter, r *http.Request, dryRun bool) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	record, err := h.Store.GetMentalModel(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	if strings.TrimSpace(record.SourceQuery) == "" {
		httpx.WriteJSONFor(w, r, http.StatusOK, RefreshResult{
			Model:     toModel(record),
			Refreshed: false,
			DryRun:    dryRun,
			Reason:    "the mental model has no source query to re-run",
		})
		return
	}
	if dryRun {
		httpx.WriteJSONFor(w, r, http.StatusOK, RefreshResult{
			Model:  toModel(record),
			DryRun: true,
			Reason: "a refresh would re-run the source query",
		})
		return
	}

	// An explicit refresh does not fold into a *running* one: that run may have
	// read its inputs before whatever change prompted this request. It does
	// fold into a merely queued one, since that run has not started and will
	// read the same current state.
	result, err := h.Store.EnqueueOperation(r.Context(), storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          map[string]any{storage.MentalModelIDPayloadKey: id},
			SerializationKey: storage.MentalModelSerializationKey(id),
		},
		DedupeByPayloadKey: storage.MentalModelIDPayloadKey,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, RefreshResult{
		Model:       toModel(record),
		Refreshed:   false,
		OperationID: result.Operation.OperationID,
		Queued:      true,
		Reason:      "a refresh has been queued",
	})
}

// BindRoutes registers the family's route table.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModels), h.List)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMentalModels), h.Create)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMentalModelMember), h.Get)
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	return bankID, true
}

// decodeBody decodes a JSON body with a size bound.
func decodeBody(r *http.Request, target any) error {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "bank not found")
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

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("mm-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// bankContext is unused indirection kept private; the store carries the
// context, so this only documents intent for a future consumer.
var _ = context.Background

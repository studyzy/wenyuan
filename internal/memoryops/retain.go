package memoryops

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Handlers wires the memory operation routes to a store.
type Handlers struct {
	Store storage.Store
	// DefaultRetryAfter is the delay advertised when writes are unavailable.
	DefaultRetryAfterSeconds int
	// ReadTimeout is the budget after which a read reports a gateway timeout.
	ReadTimeoutMillis int
	// RetainBatchEnabled forces asynchronous writes and rejects synchronous
	// requests when set by configuration.
	RetainBatchEnabled bool
	// RecallStrategyBoosts maps a retrieval arm identifier to a bounded boost
	// level; empty leaves every arm unboosted.
	RecallStrategyBoosts map[string]string
	// DisabledRecallArms lists retrieval arms removed by bank configuration.
	DisabledRecallArms []string
	// RerankerScoresByRankPosition reports whether the configured reranker
	// scores by rank position; a reranker floor is rejected against it because
	// such a floor would keep a fixed share rather than filter by relevance.
	RerankerScoresByRankPosition bool
	// ReflectSchemaValidator validates a caller-supplied structured-output
	// schema; nil accepts any non-null schema.
	ReflectSchemaValidator ReflectSchemaValidator
	// BankReflectDefaults carries the two per-bank reflection defaults applied
	// unless the request overrides them; nil uses the shipped defaults.
	BankReflectDefaults *BankReflectDefaults
	// ReflectTimeout is the reflection wall-clock budget; zero is unbounded.
	ReflectTimeout time.Duration
	// ReflectCatalog supplies the per-bank perspective and directive
	// catalogues the reflection loop considers. It is optional; nil means the
	// loop sees no perspectives or directives.
	ReflectCatalog ReflectCatalog
}

// ReflectCatalog resolves the perspective and directive candidates for a bank.
type ReflectCatalog interface {
	MentalModels(bankID string) []storage.ReflectMentalModel
	Directives(bankID string) []storage.ReflectDirective
}

// BankReflectDefaults carries the two per-bank reflection defaults, resolved as
// explicit-request over bank default over shipped default.
type BankReflectDefaults struct {
	Budget                            string
	SearchObservationsMaxTokens       *int
	SearchObservationsIncludeEntities *bool
}

// NewHandlers builds the memory operations handler set.
func NewHandlers(store storage.Store, retryAfterSeconds, readTimeoutMillis int) *Handlers {
	return &Handlers{Store: store, DefaultRetryAfterSeconds: retryAfterSeconds, ReadTimeoutMillis: readTimeoutMillis}
}

// Retain serves the memory write route.
func (h *Handlers) Retain(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	var req RetainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body is not valid JSON"))
		return
	}
	if failure := ValidateRetain(req, h.RetainBatchEnabled); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	if blocked := blockedWriteFailure(req); blocked != nil {
		httpx.WriteValidationFor(w, r, blocked)
		return
	}
	result, err := h.Store.RetainWrite(r.Context(), toStorageRetain(bankID, req))
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// BuildRetainResponse assembles the write acceptance body.
func BuildRetainResponse(bankID string, req RetainRequest, result storage.RetainOutcome) RetainResponse {
	response := RetainResponse{
		Success:    true,
		BankID:     bankID,
		ItemsCount: len(req.Items),
		Async:      req.Async,
		Usage: Usage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
			TotalTokens:  result.Usage.TotalTokens,
		},
	}
	if req.Async {
		response.OperationID = result.OperationID
		response.OperationIDs = result.OperationIDs
	}
	return response
}

func (h *Handlers) mapWriteError(err error) error {
	switch {
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailUnknownBank)
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "operation identifier has already been used")
	case errors.Is(err, storage.ErrDocumentConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "document changed since it was read; retry the write")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown).
			WithHeader(httpx.HeaderRetryAfter, itoa(h.DefaultRetryAfterSeconds))
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

func (h *Handlers) mapReadError(err error) error {
	switch {
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailUnknownBank)
	case errors.Is(err, storage.ErrReadTimeout):
		return httpx.NewHTTPError(httpx.KindTimeout, httpx.DetailSearchTO)
	case errors.Is(err, storage.ErrReadShed):
		return httpx.NewHTTPError(httpx.KindUnavailable, "recall is temporarily unavailable").
			WithHeader(httpx.HeaderRetryAfter, itoa(h.DefaultRetryAfterSeconds))
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

func toStorageRetain(bankID string, req RetainRequest) storage.RetainRequest {
	items := make([]storage.RetainItem, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, toStorageRetainItem(item))
	}
	return storage.RetainRequest{
		BankID:       bankID,
		Items:        items,
		Async:        req.Async,
		OperationID:  req.OperationID,
		DocumentTags: req.DocumentTags,
	}
}

func toStorageRetainItem(item RetainItem) storage.RetainItem {
	stored := storage.RetainItem{
		Content:           item.Content.Flatten(),
		Context:           item.Context,
		Metadata:          DropNullValues(item.Metadata),
		DocumentID:        item.DocumentID,
		Entities:          item.Entities,
		ResolveEntities:   item.ResolveEntities,
		Tags:              item.Tags,
		ObservationScopes: item.ObservationScopes,
		Strategy:          item.Strategy,
		UpdateMode:        item.UpdateMode,
	}
	if item.Timestamp.Set {
		stored.HasTimestamp = true
		stored.Timestamp = item.Timestamp.Value
	}
	return stored
}

func itoa(value int) string {
	if value <= 0 {
		return "1"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

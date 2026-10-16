package memoryops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/fileparse"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Handlers wires the memory operation routes to a store.
type Handlers struct {
	Store storage.Store
	// DefaultRetryAfter is the delay advertised when writes are unavailable.
	DefaultRetryAfterSeconds int
	// ReadTimeout is the budget after which a read reports a gateway timeout.
	ReadTimeoutMillis int
	// RetainSubBatchTokens bounds one queued child operation's token budget on
	// the asynchronous path: a larger submission is split across several
	// children, each of which a worker runs on its own. Zero uses the shipped
	// default.
	RetainSubBatchTokens int
	// RecallStrategyBoosts maps a retrieval arm identifier to a bounded boost
	// level; empty leaves every arm unboosted.
	RecallStrategyBoosts map[string]string
	// RecallRecency is the resolved age-to-freshness tuning recall applies.
	RecallRecency storage.RecencyDecayConfig
	// DisabledRecallArms lists retrieval arms removed by bank configuration.
	DisabledRecallArms []string
	// RerankerScoresByRankPosition reports whether the configured reranker
	// scores by rank position; a reranker floor is rejected against it because
	// such a floor would keep a fixed share rather than filter by relevance.
	RerankerScoresByRankPosition bool
	// RerankerPrunesCandidates reports whether the configured reranker judges
	// relevance and marks a candidate it would remove with a score of exactly
	// 0.0; recall then drops those candidates instead of carrying them.
	RerankerPrunesCandidates bool
	// RerankerSeedFromFused reports that the configured reranker's own values
	// carry no ordering information (the RRF passthrough), so recall seeds the
	// base from the fused ordering rather than the reranker's output.
	RerankerSeedFromFused bool
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
	// Extraction turns retained text into facts. A nil value stores each
	// item's text verbatim, which is what a deployment with no model
	// configured gets.
	Extraction storage.FactExtraction
	// Embeddings, when non-nil, embeds retained units and the recall query so
	// the vector recall arm and its ANN index have vectors to rank.
	Embeddings storage.EmbeddingGenerator
	// FileUploadDisabled makes the file retain route report not-found, matching
	// the reference's HINDSIGHT_API_ENABLE_FILE_UPLOAD_API gate when it is off.
	FileUploadDisabled bool
	// DefaultExtractionMode is the deployment's extraction mode, used when the
	// bank configures none; DefaultOptionalFactDimensions relaxes the schema the
	// same way.
	DefaultExtractionMode         string
	DefaultOptionalFactDimensions bool
	// DefaultRetainStrategy is the deployment's retain strategy, applied when a
	// write's items name none. Empty leaves the bank's own settings in force,
	// which is the reference's shipped default (no strategy override).
	DefaultRetainStrategy string
	// DiscardDocumentText is the deployment-level opt-out from persisting raw
	// source text: when true, a retain keeps neither the document's
	// original_text nor its chunks' text, unless a bank's own
	// store_document_text override says otherwise. False (the zero value)
	// keeps the text, which is the reference's default.
	DiscardDocumentText bool
	// Reflection, when non-nil, replaces the deterministic in-process
	// reflection with the model-driven retrieval loop.
	Reflection storage.ReflectRunner
	// Reranker, when non-nil, rescoring of the fused recall candidates runs
	// through the cross-encoder instead of falling back to the best per-arm
	// score. A deployment with no rerank model leaves it nil.
	Reranker RecallReranker
	// RerankDepth bounds how many candidates reach the reranker, in ranked
	// order. Zero means every candidate does.
	RerankDepth int
	// MaxAttachmentBytes bounds one inline attachment, and MaxAttachments
	// bounds how many one request may carry. Zero leaves the bound off, which is
	// what a deployment that configured nothing had before. Both are the
	// reference's static server-level upload limits.
	MaxAttachmentBytes int64
	MaxAttachments     int
	// VisionEnabled reports whether the configured write model can read inline
	// image content. When false a write carrying images is refused with a 422
	// rather than extracting from the caption alone; when true the images are
	// forwarded to the model.
	VisionEnabled bool
	// Parsers is the document-parser registry the file route converts uploads
	// with. A nil value builds the configured registry lazily from the process
	// environment, so a handler set assembled without one still converts.
	Parsers *fileparse.Registry
	// ParserChain is the ordered fallback chain applied when a request names no
	// parser. Empty falls back to the shipped default chain.
	ParserChain []string
	// ParserAllowlist bounds the parser names a client may request. Empty means
	// every registered parser is requestable.
	ParserAllowlist []string
	// MaxFileBatchCount and MaxFileBatchBytes bound one /files/retain request
	// overall; zero leaves that bound off. They are the reference's
	// HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE and _MB.
	MaxFileBatchCount int
	MaxFileBatchBytes int64
	// DeleteAfterRetain keeps only the converted text of an upload, leaving its
	// stored bytes out of file storage once it has been retained. It is the
	// reference's HINDSIGHT_API_FILE_DELETE_AFTER_RETAIN (default on).
	DeleteAfterRetain bool
	// Webhooks enqueues webhook deliveries for the events this route family
	// raises. memoryops cannot reach the worker's queue directly, so the
	// composition root supplies an implementation; a nil value (a deployment
	// with no webhooks) disables every call.
	Webhooks WebhookNotifier
	// ObservationsEnabled and AutoConsolidationEnabled are the deployment
	// defaults for the two flags that gate the post-retain consolidation pass.
	// A bank may override either through its own configuration. When both are
	// on, a successful write enqueues a consolidation pass so freshly stored
	// facts are folded into observations without the caller asking.
	ObservationsEnabled      bool
	AutoConsolidationEnabled bool
}

// WebhookNotifier enqueues a webhook delivery for an event. The composition
// root adapts the worker's one enqueue path to this capability, so a delivery is
// created in exactly one place and the handler never reaches the queue itself.
type WebhookNotifier interface {
	// EnqueueWebhookEvent queues one delivery per webhook of the bank that
	// subscribes to the event.
	EnqueueWebhookEvent(ctx context.Context, bankID, eventType string, data map[string]any)
}

// RecallReranker scores fused recall candidates against the query. It is the
// boundary between the recall pipeline and the cross-encoder, so the storage
// area stays free of any provider import.
type RecallReranker interface {
	// Rerank returns one score per candidate, in candidate order, on the
	// backend's own relevance scale. An error leaves the caller on its
	// fallback rather than failing the recall.
	Rerank(ctx context.Context, query string, texts []string) ([]float64, error)
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

// retainBodyFields is the set of top-level body names the retain route accepts.
// Deprecated aliases the contract keeps accepting are listed alongside the
// current names so a client that sends one is not told it is unknown.
var retainBodyFields = map[string]struct{}{
	"items": {}, "async": {}, "operation_id": {}, "document_tags": {},
	// Deprecated aliases kept for compatibility.
	"background": {}, "reflect_mission": {},
}

// retainWriteScopeFailure refuses a retain that would write a tag outside the
// caller's write scope, answering 403. Every item's tags (with the batch's
// document tags) must be writable, and so must every tag a retain strategy's
// entity labels could add — those attach after extraction, from what the model
// reads into the text, so the whole possible set is checked up front. With no
// write scope the check is a no-op, which is the unextended behaviour.
func (h *Handlers) retainWriteScopeFailure(r *http.Request, bankID string, _ RetainRequest, write storage.RetainRequest) error {
	scope, confined := httpx.WriteTagScopeFrom(r)
	if !confined {
		return nil
	}
	bank, err := h.resolveBankConfig(r.Context(), nil, bankID)
	if err != nil {
		// A configuration that cannot be read is not a scope failure; the write
		// path reports it on its own.
		return nil
	}
	for _, item := range write.Items {
		tags := append(append([]string{}, item.Tags...), write.DocumentTags...)
		if !httpx.TagsWritable(tags, scope, confined) {
			return httpx.NewHTTPError(httpx.KindForbidden,
				"You can't write memories tagged "+strings.Join(tags, ", "))
		}
		config := strategyConfigOverlay(bank.Config, item.Strategy)
		vocabulary, perr := entitylabels.Parse(config["entity_labels"])
		if perr != nil {
			continue
		}
		if candidates := vocabulary.TagCandidates(); len(candidates) > 0 && !httpx.TagsWritable(candidates, scope, confined) {
			return httpx.NewHTTPError(httpx.KindForbidden,
				"Retain strategy '"+item.Strategy+"' can tag memories "+
					strings.Join(candidates, ", ")+", which you can't write")
		}
	}
	return nil
}

// Retain serves the memory write route.
func (h *Handlers) Retain(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	// Unknown body fields are reported on the response, never rejected, so a
	// client that sends a field the route does not know learns about it.
	body, r := httpx.RecordIgnoredBodyFields(r, retainBodyFields)
	var req RetainRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// The reference's framework rejects a malformed body as a structured
		// 422 item, not with a 400 sentence.
		httpx.WriteErrorFor(w, r, httpx.BodyDecodeFailure(err, body))
		return
	}
	if failure := ValidateRetain(req); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	if blocked := blockedWriteFailure(req, h.VisionEnabled); blocked != nil {
		httpx.WriteValidationFor(w, r, blocked)
		return
	}
	// Inline attachments are decoded and stored before the retain is submitted,
	// so the canonical text carries only placeholders and the bytes never enter
	// an async operation's payload. A failure to store them refuses the write
	// rather than extracting from a picture the model will never see.
	if err := h.storeInlineAttachments(r.Context(), bankID, &req); err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	// Memory Defense screens the items as the caller submitted them, before the
	// write is converted or folded: the reference decides "every item was blocked"
	// against the submitted count and reports each violation by its position in
	// that list, so screening a folded list would move those positions and let one
	// blocked item take a clean neighbour down with it.
	bankConfig, err := h.resolveBankConfig(r.Context(), httpx.TenantOverridesFrom(r), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	screened, err := h.applyMemoryDefense(r.Context(), bankID, bankConfig, req.Items)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	req.Items = screened
	write := toStorageRetainItems(bankID, req, !req.Async)
	// A write may not carry a tag the caller cannot write, so a scoped caller
	// cannot reach another scope's tags through a retain.
	if err := h.retainWriteScopeFailure(r, bankID, req, write); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	// The bank's own retain configuration supplies the extraction prompt and
	// chunk boundaries. It is read once here; an unset key leaves the shipped
	// default in place. The tenant's overrides layer under the bank's own, so a
	// tenant key applies unless the bank overrode it.
	//
	// A configuration that cannot be read refuses the write rather than
	// proceeding without it: the bank's Memory Defense policy lives in that
	// configuration, so failing open would store an item the policy would have
	// redacted or blocked on exactly the request where reads are flaky.
	if req.Async {
		h.submitAsyncRetain(r, w, bankID, req, write)
		return
	}
	result, bank, err := h.executeRetain(r.Context(), bankID, httpx.TenantOverridesFrom(r), write)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	// The write is durable here, so the event is raised once and only for work
	// that actually landed: a caller polling the operation and a subscriber
	// receiving the delivery see the same completion.
	if h.Webhooks != nil {
		h.Webhooks.EnqueueWebhookEvent(r.Context(), bankID, "retain.completed", map[string]any{
			"document_id":  retainDocumentID(write),
			"items_count":  len(result.ItemUnitIDs),
			"unit_ids":     flattenedUnitIDs(result),
			"operation_id": result.OperationID,
		})
	}
	h.enqueuePostRetainConsolidation(r.Context(), bankID, bank)
	httpx.WriteJSONFor(w, r, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// enqueuePostRetainConsolidation queues the consolidation pass that follows a
// successful write, so freshly stored facts are folded into observations
// without the caller asking. It runs only when observations and automatic
// consolidation are both enabled — the bank's own override else the
// deployment default. A failure to enqueue is logged and swallowed: the write
// is already durable, and consolidation is a follow-up that the reconcile
// sweep will pick up rather than a condition of the write's success.
func (h *Handlers) enqueuePostRetainConsolidation(ctx context.Context, bankID string, bank storage.Bank) {
	observations := h.ObservationsEnabled
	if value, ok := bank.Config["enable_observations"].(bool); ok {
		observations = value
	}
	auto := h.AutoConsolidationEnabled
	if value, ok := bank.Config["enable_auto_consolidation"].(bool); ok {
		auto = value
	}
	if !observations || !auto {
		return
	}
	if _, _, err := h.Store.EnqueueConsolidation(ctx, bankID); err != nil {
		slog.Default().Warn("could not enqueue post-retain consolidation", "bank_id", bankID, "error", err)
	}
}

// retainDocumentID reports the document the write targeted, which is the
// request's when every item shares one. An empty value means the items were not
// written to a single document, which is what a batch of separate items is.
func retainDocumentID(write storage.RetainRequest) string {
	documentID := ""
	for _, item := range write.Items {
		if item.DocumentID == "" {
			return ""
		}
		if documentID == "" {
			documentID = item.DocumentID
			continue
		}
		if item.DocumentID != documentID {
			return ""
		}
	}
	return documentID
}

// flattenedUnitIDs returns every unit the write created, in item order.
func flattenedUnitIDs(result storage.RetainOutcome) []string {
	out := make([]string, 0)
	for _, ids := range result.ItemUnitIDs {
		out = append(out, ids...)
	}
	return out
}

// errBankConfigRead marks a write refused because the bank's configuration
// could not be read. Screening a write needs that configuration, so an
// unreadable one refuses the write rather than storing it unscreened. It is its
// own sentinel so the refusal is distinguishable from a bank that simply
// configured no policy.
var errBankConfigRead = errors.New("bank configuration could not be read")

// resolveBankConfig reads the bank's configuration for a write path, layering
// the tenant's overrides under it. A read failure is returned wrapped with
// errBankConfigRead, so a caller refuses the write instead of proceeding with
// an empty configuration and skipping the bank's Memory Defense policy. An
// unknown bank is not a failure: it has no configuration to apply, so the write
// path creates it empty exactly as it did before.
func (h *Handlers) resolveBankConfig(ctx context.Context, overrides map[string]any, bankID string) (storage.Bank, error) {
	bankConfig, err := h.Store.BankConfig(ctx, bankID)
	if err != nil {
		// An unknown bank has no configuration to apply and no Memory Defense
		// policy to skip; the write path lazily creates it, so it proceeds with
		// an empty configuration exactly as before. Any other read failure may
		// hide a configured policy, so it refuses the write.
		if errors.Is(err, storage.ErrBankNotFound) {
			return storage.Bank{BankID: bankID}, nil
		}
		return storage.Bank{}, fmt.Errorf("%w: %w", errBankConfigRead, err)
	}
	resolved := config.ResolveLayers(bankConfig, overrides, nil)
	return storage.Bank{BankID: bankID, Config: resolved}, nil
}

// submitAsyncRetain queues the write instead of running it, and answers with
// the receipt the caller polls.
//
// The submission is the reference's shape: one parent batch operation that
// aggregates it and one child per token-bounded sub-batch, each carrying the
// items and the settings resolved now. Nothing is extracted here — the work is
// the queue's — so the response reports an item count and an operation id and
// no usage, because none has happened yet.
func (h *Handlers) submitAsyncRetain(r *http.Request, w http.ResponseWriter, bankID string, req RetainRequest, write storage.RetainRequest) {
	prepared, _, err := h.prepareRetain(r.Context(), bankID, httpx.TenantOverridesFrom(r), write)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	prepared.Async = true
	tokensPerBatch := h.RetainSubBatchTokens
	if tokensPerBatch <= 0 {
		tokensPerBatch = storage.DefaultRetainSubBatchTokens
	}
	submission, err := h.Store.SubmitAsyncRetain(r.Context(), prepared, tokensPerBatch)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	// The receipt names the parent, which is what the caller polls and what a
	// retried submission with the same identifier rejoins.
	result := storage.RetainOutcome{
		OperationID: submission.OperationID,
		// The submission's own identifier is reported in both fields, so a
		// caller that reads either sees the operation it polls. A request that
		// split across several strategy groups is the only case with more than
		// one, and this service queues one.
		OperationIDs: []string{submission.OperationID},
		// No accounting has happened yet, which is reported as absent rather
		// than as zero.
		ProcessedContentTokens: -1,
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// errAppendWithoutStoredText refuses an append when the bank stores no document
// text, because there is nothing to append to. It is refused as a client error
// rather than silently degrading to a replace.
//
// The text is the reference's, character for character — it names the setting a
// caller has to change — so the sentence ends in a period, which ST1005 would
// otherwise flag. Contract fidelity wins over the style rule here.
//
//nolint:staticcheck // the reference's own wording, period included
var errAppendWithoutStoredText = errors.New("update_mode='append' is not supported when document text storage (store_document_text / HINDSIGHT_API_STORE_DOCUMENT_TEXT) is disabled: the prior document text is not stored and cannot be appended to. Use update_mode='replace' instead.")

// executeRetain resolves the bank configuration onto the write and runs it. It
// is the one path the write route and a document reprocess both take, so the
// two cannot drift on which bank settings shape extraction.
func (h *Handlers) executeRetain(ctx context.Context, bankID string, overrides map[string]any, write storage.RetainRequest) (storage.RetainOutcome, storage.Bank, error) {
	// Every model call this write makes — fact extraction and any chunked
	// follow-up — is recorded under the bank and the "retain" operation. The
	// attribution has to be on the context that reaches the write, not only on
	// the one the configuration is resolved with: the extraction runs inside
	// RetainWrite, so a decoration dropped on the way in leaves the trace rows
	// with no bank or operation.
	ctx = provider.WithTraceContext(ctx, provider.TraceContext{BankID: bankID, Operation: "retain"})
	write, bank, err := h.prepareRetain(ctx, bankID, overrides, write)
	if err != nil {
		return storage.RetainOutcome{}, storage.Bank{}, err
	}
	outcome, err := h.Store.RetainWrite(ctx, write)
	if err != nil {
		return storage.RetainOutcome{}, storage.Bank{}, err
	}
	return outcome, bank, nil
}

// prepareRetain resolves the bank's retain configuration onto the write and
// attaches the provider-facing boundaries, without storing anything. It is what
// the synchronous path runs before it writes and what the queued path runs
// before it serializes, so the two cannot drift on which settings shape
// extraction.
func (h *Handlers) prepareRetain(ctx context.Context, bankID string, overrides map[string]any, write storage.RetainRequest) (storage.RetainRequest, storage.Bank, error) {
	bank, err := h.resolveBankConfig(ctx, overrides, bankID)
	if err != nil {
		return storage.RetainRequest{}, storage.Bank{}, err
	}
	applyExtractionConfig(&write, bank, extractionDefaults{
		mode:               h.DefaultExtractionMode,
		optionalDimensions: h.DefaultOptionalFactDimensions,
		strategy:           h.DefaultRetainStrategy,
	})
	if applyStoreDocumentText(&write, bank, h.DiscardDocumentText) {
		return storage.RetainRequest{}, storage.Bank{}, errAppendWithoutStoredText
	}
	// Memory Defense has already screened the caller's items (see Retain): it runs
	// on the submitted list, not here, so the violation positions and the
	// all-blocked decision describe what was sent.
	write.Extraction = h.Extraction
	write.Embeddings = h.Embeddings
	return write, bank, nil
}

// ReprocessDocument re-runs the retain pipeline over a document's stored text
// and replayable parameters, replacing what the earlier retain derived from it.
//
// The stored text is the input, so reprocess re-reads without rewriting it; the
// replayable parameters (strategy, context, event date, metadata, observation
// scopes, tags) are restored so a re-extraction runs under the same settings
// the document was retained with rather than under the bank defaults. The write
// is forced to re-extract, so an unchanged content hash cannot turn the
// reprocess into a silent no-op.
func (h *Handlers) ReprocessDocument(ctx context.Context, bankID, documentID string) (storage.RetainOutcome, error) {
	doc, err := h.Store.GetDocument(ctx, bankID, documentID)
	if err != nil {
		return storage.RetainOutcome{}, err
	}
	// No stored text means nothing to re-extract from; the reference reads that
	// as the document not being there.
	if strings.TrimSpace(doc.OriginalText) == "" {
		return storage.RetainOutcome{}, storage.ErrDocumentNotFound
	}
	item := storage.RetainItem{
		Content:        doc.OriginalText,
		DocumentID:     documentID,
		UpdateMode:     "replace",
		ForceReextract: true,
		Tags:           doc.Tags,
	}
	params := doc.RetainParams
	item.Strategy = stringFromParams(params, "strategy")
	item.Context = stringFromParams(params, "context")
	if metadata, ok := params["metadata"].(map[string]any); ok {
		item.Metadata = metadata
	}
	if eventDate := stringFromParams(params, "event_date"); eventDate != "" {
		if parsed, err := time.Parse(time.RFC3339, eventDate); err == nil {
			item.Timestamp = &parsed
			item.HasTimestamp = true
		}
	}
	if scopes, ok := params["observation_scopes"]; ok {
		if encoded, err := json.Marshal(scopes); err == nil {
			item.ObservationScopes = encoded
		}
	}
	write := storage.RetainRequest{BankID: bankID, Items: []storage.RetainItem{item}}
	outcome, _, err := h.executeRetain(ctx, bankID, nil, write)
	return outcome, err
}

// stringFromParams reads a string field from a document's retain parameters,
// defaulting to empty when the value is absent or of another type.
func stringFromParams(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	value, _ := params[key].(string)
	return value
}

// extractionDefaults carries the deployment-level extraction settings a bank
// may override.
type extractionDefaults struct {
	mode               string
	optionalDimensions bool
	// strategy is the deployment's retain_default_strategy, applied when the
	// write's items name none.
	strategy string
}

// applyExtractionConfig copies the bank's retain configuration onto the write.
// An unset or invalid key leaves the field at its zero value, so a bank that
// configures nothing keeps the shipped prompt and chunking byte-for-byte.
func applyExtractionConfig(write *storage.RetainRequest, bank storage.Bank, defaults extractionDefaults) {
	// A named strategy layers its overrides on top of the bank's own settings,
	// so a write that names one extracts under that strategy's prompt and
	// chunking rather than the bank default. The items agree on a name in every
	// practical request; the first name wins when they do not. An item that
	// names none falls back to the deployment's retain_default_strategy, which
	// is the reference's `strategy or resolved_config.retain_default_strategy`.
	config := strategyConfigOverlay(bank.Config, writeStrategyName(write.Items, defaults.strategy))
	write.ExtractionMission = bankConfigString(config, "retain_mission")
	write.ExtractionMode = bankConfigString(config, "retain_extraction_mode")
	if write.ExtractionMode == "" {
		// The bank configures no mode, so the deployment's default applies.
		write.ExtractionMode = defaults.mode
	}
	if !bankConfigBool(config, "retain_optional_fact_dimensions") && defaults.optionalDimensions {
		write.OptionalFactDimensions = true
	}
	// The reference stores the custom instructions unconditionally but consults
	// them only in custom mode, so they are carried onto the write only then —
	// otherwise they would silently reshape a prompt the mode never asked for.
	if write.ExtractionMode == "custom" {
		write.ExtractionInstructions = bankConfigString(config, "retain_custom_instructions")
	}
	write.OptionalFactDimensions = bankConfigBool(config, "retain_optional_fact_dimensions")
	write.ChunkSize = bankConfigInt(config, "retain_chunk_size")
	write.StructuredChunkSize = bankConfigInt(config, "retain_structured_chunk_size")
	// The label vocabulary is validated when it is written, so a value that
	// cannot be parsed here is one that predates that validation; it leaves the
	// feature off rather than failing a write the caller cannot fix.
	if vocabulary, err := entitylabels.Parse(config["entity_labels"]); err == nil {
		write.Vocabulary = vocabulary
	}
}

// writeStrategyName returns the retain strategy the write runs under: the first
// name its items carry, else the deployment's default strategy. An unnamed write
// with no deployment default leaves this empty, so the bank's own settings
// apply unchanged.
func writeStrategyName(items []storage.RetainItem, fallback string) string {
	for _, item := range items {
		if item.Strategy != "" {
			return item.Strategy
		}
	}
	return fallback
}

// strategyConfigOverlay layers a named retain strategy's overrides on top of the
// bank configuration, mirroring the reference's apply_strategy: any retain key
// the strategy names replaces the bank-level value for this write. An unknown or
// malformed strategy leaves the base configuration in force, so a name the bank
// never defined behaves exactly as the bank default.
func strategyConfigOverlay(bankConfig map[string]any, strategy string) map[string]any {
	if strategy == "" {
		return bankConfig
	}
	strategies, ok := bankConfig["retain_strategies"].(map[string]any)
	if !ok {
		return bankConfig
	}
	overrides, ok := strategies[strategy].(map[string]any)
	if !ok || len(overrides) == 0 {
		return bankConfig
	}
	merged := make(map[string]any, len(bankConfig)+len(overrides))
	for key, value := range bankConfig {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}

// applyStoreDocumentText resolves the bank's store_document_text setting (its
// own override, else the deployment default) onto the write. It reports
// whether an append was requested while the setting is off, which cannot work
// because there is no stored text to prepend.
func applyStoreDocumentText(write *storage.RetainRequest, bank storage.Bank, defaultDiscard bool) bool {
	storeText := !defaultDiscard
	if value, ok := bank.Config["store_document_text"].(bool); ok {
		storeText = value
	}
	write.DiscardDocumentText = !storeText
	if storeText {
		return false
	}
	for _, item := range write.Items {
		if item.UpdateMode == "append" {
			return true
		}
	}
	return false
}

// bankConfigString reads a string override, defaulting to empty.
func bankConfigString(config map[string]any, key string) string {
	if value, ok := config[key].(string); ok {
		return value
	}
	return ""
}

// bankConfigInt reads a positive integer override, defaulting to zero. A
// non-positive or non-numeric value reads as unset, so a bad override cannot
// disable chunking.
func bankConfigInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case float64:
		if value > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil && parsed > 0 {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

// bankConfigBool reads a boolean override, defaulting to false. Only a real
// boolean true, or the string "true", turns a flag on; an unset or malformed
// value leaves it off rather than failing the write.
func bankConfigBool(config map[string]any, key string) bool {
	switch value := config[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

// BuildRetainResponse assembles the write acceptance body.
func BuildRetainResponse(bankID string, req RetainRequest, result storage.RetainOutcome) RetainResponse {
	response := RetainResponse{
		Success:    true,
		BankID:     bankID,
		ItemsCount: len(req.Items),
		Async:      req.Async,
	}
	if req.Async {
		response.OperationID = result.OperationID
		response.OperationIDs = result.OperationIDs
	} else {
		response.Usage = &Usage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
			TotalTokens:  result.Usage.TotalTokens,
		}
	}
	// A negative count means the path did not account for the content, which is
	// reported as absent rather than as zero.
	if result.ProcessedContentTokens >= 0 {
		tokens := result.ProcessedContentTokens
		response.ProcessedContentTokens = &tokens
	}
	return response
}

func (h *Handlers) mapWriteError(err error) error {
	switch {
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrInvalidBankID):
		// A first write to an over-budget or malformed bank identifier is
		// refused before the bank exists, exactly as the explicit create is.
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "operation identifier has already been used")
	case errors.Is(err, storage.ErrDocumentConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "document changed since it was read; retry the write")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown).
			WithHeader(httpx.HeaderRetryAfter, itoa(h.DefaultRetryAfterSeconds))
	case errors.Is(err, errAppendWithoutStoredText):
		return httpx.NewHTTPError(httpx.KindBadRequest, errAppendWithoutStoredText.Error())
	case errors.As(err, new(*MemoryDefenseAllBlockedError)):
		// Every item was blocked, so nothing was written. The reference answers 422
		// with the violations under `detail`, which is an object rather than the
		// usual sentence; a caller needs to know which item matched what.
		var blocked *MemoryDefenseAllBlockedError
		_ = errors.As(err, &blocked)
		return &httpx.HTTPError{
			Status:  httpx.KindUnprocessable,
			Detail:  blocked.Error(),
			Payload: map[string]any{"violations": blocked.Violations},
		}
	case errors.Is(err, errBankConfigRead):
		// The write was refused because the bank's configuration could not be
		// read; it is retryable, and the caller learns it was not written
		// unscreened.
		return httpx.NewHTTPError(httpx.KindUnavailable, "the bank configuration could not be read; the write was not accepted").
			WithHeader(httpx.HeaderRetryAfter, itoa(h.DefaultRetryAfterSeconds))
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

func (h *Handlers) mapReadError(err error) error {
	switch {
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrReadTimeout):
		return httpx.NewHTTPError(httpx.KindTimeout, httpx.DetailSearchTO)
	case errors.Is(err, storage.ErrReadShed):
		return httpx.NewHTTPError(httpx.KindUnavailable, "recall is temporarily unavailable").
			WithHeader(httpx.HeaderRetryAfter, itoa(h.DefaultRetryAfterSeconds))
	case errors.Is(err, storage.ErrTagVocabularyTooLarge), errors.Is(err, storage.ErrTagExactCombinationsTooLarge):
		// A tag-scope cap is a client error, not a server fault: the caller's
		// scope exceeds a ceiling it can see in the message.
		return httpx.NewHTTPError(httpx.KindUnprocessable, err.Error())
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

// tagScopeFailure converts a tag-scope cap violation into the structured
// validation failure the recall route uses for its other 422s. The message
// carries the storage numbers through verbatim. It returns nil for any other
// error. This lives outside mapReadError because that function returns the
// shared {"detail": "..."} envelope, which cannot carry the error-item list.
func tagScopeFailure(err error) *httpx.ValidationFailure {
	if !errors.Is(err, storage.ErrTagVocabularyTooLarge) && !errors.Is(err, storage.ErrTagExactCombinationsTooLarge) {
		return nil
	}
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValueError([]string{"body", "tag_groups"}, nil, err.Error()),
	}}
}

// writeReadError maps a storage read error to its response. A tag-scope cap is
// emitted with the structured validation writer so the client gets the same
// 422 shape the route uses for its other validation errors; every other error
// keeps its existing mapping.
func (h *Handlers) writeReadError(w http.ResponseWriter, r *http.Request, err error) {
	if failure := tagScopeFailure(err); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	httpx.WriteErrorFor(w, r, h.mapReadError(err))
}

// toStorageRetain maps the wire request onto the storage write. fold controls
// whether adjacent items are folded into one extraction call, which is a
// synchronous-path optimization: a queued submission keeps its items as the
// caller counted them, so the receipt's item count and its per-item children
// name what was actually sent.
func toStorageRetain(bankID string, req RetainRequest) storage.RetainRequest {
	return toStorageRetainItems(bankID, req, true)
}

// toStorageRetainItems is toStorageRetain with the folding decision exposed.
func toStorageRetainItems(bankID string, req RetainRequest, fold bool) storage.RetainRequest {
	items := req.Items
	if fold {
		items = FoldRetainItems(req.Items)
	}
	stored := make([]storage.RetainItem, 0, len(items))
	for _, item := range items {
		stored = append(stored, toStorageRetainItem(item))
	}
	return storage.RetainRequest{
		BankID:       bankID,
		Items:        stored,
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
		Entities:          toStorageEntities(item.Entities),
		ResolveEntities:   resolveEntitiesOrDefault(item.ResolveEntities),
		Tags:              item.Tags,
		ObservationScopes: item.ObservationScopes.RawJSON(),
		Strategy:          item.Strategy,
		UpdateMode:        item.UpdateMode,
		ForceReextract:    item.ForceReextract,
		AttachmentIDs:     item.AttachmentIDs,
		Attachments:       toStorageAttachments(item.Attachments),
	}
	if item.Timestamp.Set {
		stored.HasTimestamp = true
		stored.Timestamp = item.Timestamp.Value
	}
	return stored
}

// resolveEntitiesOrDefault returns the entity-resolution switch, defaulting an
// omitted flag to true to match the reference. The reference resolves supplied
// entity names against the bank unless the caller explicitly opts out.
func resolveEntitiesOrDefault(flag *bool) bool {
	if flag == nil {
		return true
	}
	return *flag
}

// EntityDefaultType is the type an entity carries when the caller supplied a
// bare name rather than an object. A name with no label is a concept, which is
// what an untyped mention means.
const EntityDefaultType = "CONCEPT"

// toStorageEntities maps the wire entity list onto the storage shape. Entries
// with a blank name are dropped, because a nameless entity cannot be stored.
func toStorageEntities(entities EntityInputList) []storage.RetainEntity {
	out := make([]storage.RetainEntity, 0, len(entities))
	for _, entity := range entities {
		name := trimSpaceString(entity.Text)
		if name == "" {
			continue
		}
		kind := trimSpaceString(entity.Type)
		if kind == "" {
			kind = EntityDefaultType
		}
		out = append(out, storage.RetainEntity{Text: name, Kind: kind})
	}
	return out
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

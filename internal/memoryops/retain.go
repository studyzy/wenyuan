package memoryops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/fileparse"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/validation"
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
	// RecallPreferObservations is the deployment's (WENYUAN_API_RECALL_PREFER_
	// OBSERVATIONS) value for recall's prefer_observations. It is the last
	// layer: a request that states the member (including false) wins, then the
	// bank's own recall_prefer_observations, then this.
	RecallPreferObservations bool
	// DefaultObservationScopes is the deployment's
	// WENYUAN_API_DEFAULT_OBSERVATION_SCOPES: the observation_scopes a retained
	// item takes when neither it nor its bank states one. Raw JSON (a mode name
	// or a list of tag-group lists); empty is the combined default.
	DefaultObservationScopes json.RawMessage
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
	// ReflectSchemaValidator is an optional second structured-output schema
	// validator, run only after the reference's own response-schema rules pass;
	// nil adds no extra rule. The built-in rules are always enforced.
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
	// the reference's WENYUAN_API_ENABLE_FILE_UPLOAD_API gate when it is off.
	FileUploadDisabled bool
	// RetainBatchEnabled is the reference's WENYUAN_API_RETAIN_BATCH_ENABLED.
	// When it is on a synchronous retain is refused, because a batch retain can
	// run for hours and cannot complete inside a synchronous request.
	RetainBatchEnabled bool
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
	// RefuseWithoutReflectModel makes the reflect route refuse with the
	// reference's message when the deployment is configured with no reflect
	// model (WENYUAN_API_LLM_PROVIDER=none). The reference answers 400 rather
	// than synthesizing an answer, so a deployment with no model behaves like
	// the reference instead of falling back to the deterministic loop. False
	// (the zero value) leaves the route serving, which is what a handler set
	// assembled without a provider expects.
	RefuseWithoutReflectModel bool
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
	// WENYUAN_API_FILE_CONVERSION_MAX_BATCH_SIZE and _MB.
	MaxFileBatchCount int
	MaxFileBatchBytes int64
	// DeleteAfterRetain keeps only the converted text of an upload, leaving its
	// stored bytes out of file storage once it has been retained. It is the
	// reference's WENYUAN_API_FILE_DELETE_AFTER_RETAIN (default on).
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
// It is exactly the reference's RetainRequest members (items, async,
// operation_id, document_tags); any other top-level name is reported as an
// ignored parameter, so a deprecated alias such as "background" or
// "reflect_mission" — which the reference's model does not declare — surfaces the
// same warning rather than being silently accepted.
var retainBodyFields = map[string]struct{}{
	"items": {}, "async": {}, "operation_id": {}, "document_tags": {},
}

// retainBatchDisabledMessage is the reference's refusal for a synchronous write
// when the batch API is enabled. It is the reference's sentence character for
// character — it names the setting to change.
//
//nolint:staticcheck // the reference's own wording, period included
var retainBatchDisabledMessage = "Batch API is enabled (WENYUAN_API_RETAIN_BATCH_ENABLED=true) but async=false. " +
	"Batch operations can take several minutes to hours and will timeout in synchronous mode. " +
	"Please set async=true in your request to use background processing, or disable batch API by setting " +
	"WENYUAN_API_RETAIN_BATCH_ENABLED=false in your environment."

// retainWriteScopeFailure refuses a retain that would write a tag outside the
// caller's write scope, answering 403. Every item's tags (with the batch's
// document tags) must be writable, and so must every tag a retain strategy's
// entity labels could add — those attach after extraction, from what the model
// reads into the text, so the whole possible set is checked up front — and so
// must the item's observation scopes, which consolidation writes at (the
// strategy's resolved default standing in for an item that names none). With no
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
			// The refusal names the tags outside the scope, sorted, or the
			// untagged marker when the item carries none — the reference's
			// _refuse_unwritable (engine/memory_engine.py:3644-3650).
			return httpx.NewHTTPError(httpx.KindForbidden,
				"You can't write memories tagged "+strings.Join(unwritableMemoryTags(tags, scope), ", "))
		}
		config := strategyConfigOverlay(bank.Config, item.Strategy)
		// The scopes an item without its own would be stamped with are a write
		// by this caller too: the reference resolves the strategy's default at
		// check time, so a bank or strategy default of "shared" refuses a
		// confined caller exactly as an explicit one would.
		scopes := item.ObservationScopes
		if len(scopes) == 0 {
			scopes = defaultObservationScopes(config, h.DefaultObservationScopes)
		}
		if err := RefuseUnwritableScopes(scopes, scope, confined); err != nil {
			return err
		}
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

// unwritableMemoryTags names the tags of a refused write that sit outside the
// caller's write scope, sorted, or the untagged marker when the write carries
// none — the reference's _refuse_unwritable computes the same set
// (engine/memory_engine.py:3644-3650).
func unwritableMemoryTags(tags, scope []string) []string {
	outside := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !httpx.TagsWritable([]string{tag}, scope, true) {
			outside = append(outside, tag)
		}
	}
	sort.Strings(outside)
	if len(outside) == 0 {
		return []string{"(untagged)"}
	}
	return outside
}

// retainDecodeFailure renders a write body the decoder refused. The shared
// decoder stops at the first member it cannot place, and a member inside the
// item list cannot name the index it sits at from inside its own type, so the
// rejected bytes are walked against the request contract instead: that walk
// names every value the reference's model refuses, at the full path the
// reference reports — index levels included — with the offending value itself
// as the item's input. A body the walk cannot describe keeps the decoder's own
// rendering.
func retainDecodeFailure(r *http.Request, req *RetainRequest, body, decoded []byte, err error) *httpx.ValidationFailure {
	items := httpx.ValidationItemsAgainstRequest(r, req, body)
	if len(items) == 0 {
		return httpx.BodyDecodeFailure(err, decoded)
	}
	// The walk names the structural rejections; the item's own field rules — the
	// timestamp's format, the observation-scope union, the update-mode literal —
	// are the model's, and the model is refilled from the same bytes so those are
	// reported beside them, the way the reference's single validation pass
	// reports one field failure per rule. A rule the walk already named at the
	// same item field is dropped, because the walk's rendering of it is the one
	// the reference serves.
	_ = json.Unmarshal(body, req)
	items = missingItemObjects(items, req, body)
	for index, item := range req.Items {
		for _, field := range itemFieldErrors(index, item) {
			if fieldReported(field.Loc, items) {
				continue
			}
			items = append(items, field)
		}
	}
	httpx.OrderItemsByDeclaration(r, req, items)
	return &httpx.ValidationFailure{Errors: items}
}

// missingItemObjects replaces the empty object the generic walk reports for a
// missing item member with the item the caller actually sent. The framework
// reports "Field required" with the object the field was missing *from* as the
// item's input, and for an item member that object is the item.
func missingItemObjects(items []httpx.ValidationError, req *RetainRequest, body []byte) []httpx.ValidationError {
	raw, ok := requestItemsRaw(body)
	if !ok {
		return items
	}
	for position, itemError := range items {
		if itemError.Type != "missing" || len(itemError.Loc) != 4 ||
			itemError.Loc[0] != "body" || itemError.Loc[1] != "items" {
			continue
		}
		index, err := strconv.Atoi(itemError.Loc[2])
		if err != nil || index < 0 || index >= len(raw) {
			continue
		}
		items[position].Input = decodeAny(raw[index])
	}
	return items
}

// decodedRequestValue is the request body as a caller's JSON value, which the
// framework carries as a validation item's `input` when a top-level member is
// missing. A body that does not decode leaves the empty object, which is the
// shape an absent body has.
func decodedRequestValue(body []byte) any {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}

// requestItemsRaw returns the raw JSON value of each element of the body's
// `items` list, so a rejection can name the item the caller sent verbatim.
func requestItemsRaw(body []byte) ([]json.RawMessage, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, false
	}
	list, present := members["items"]
	if !present {
		return nil, false
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(list, &raw); err != nil {
		return nil, false
	}
	return raw, true
}

// fieldReported reports whether the walk already named a failure for the item
// field the location sits in, at any depth of that field: the walk names a
// union's branches at their own paths, so an exact match is not enough.
func fieldReported(loc []string, items []httpx.ValidationError) bool {
	if len(loc) < 4 {
		return false
	}
	field := loc[:4]
	for _, item := range items {
		if len(item.Loc) >= 4 && equalPath(item.Loc[:4], field) {
			return true
		}
	}
	return false
}

// Retain serves the memory write route.
func (h *Handlers) Retain(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	// Unknown body fields are reported on the response, never rejected, so a
	// client that sends a field the route does not know learns about it.
	body, r := httpx.RecordIgnoredBodyFields(r, retainBodyFields)
	var req RetainRequest
	if decoded, err := httpx.DecodeLax(body, &req); err != nil {
		// The reference's framework rejects a malformed body as a structured
		// 422 item, not with a 400 sentence.
		httpx.WriteErrorFor(w, r, retainDecodeFailure(r, &req, body, decoded, err))
		return
	}
	if failure := ValidateRetain(req, decodedRequestValue(body)); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	// The reference enforces these two as handler-level 400 sentences, not model
	// validators, and only on the queued path: a synchronous request ignores a
	// supplied operation_id and folds shared-document items sequentially, so
	// neither rule applies to it. They run before any write.
	if req.Async {
		if req.OperationID != "" && strategyGroupCount(req.Items) > 1 {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				"operation_id requires all retain items to resolve to a single strategy"))
			return
		}
		if duplicates := duplicateDocumentIDsInGroups(req.Items); len(duplicates) > 0 {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				"Batch contains duplicate document_ids: "+pythonStringList(duplicates)+
					". Each content item in an async batch must have a unique document_id to avoid races between the "+
					"parallel workers that process them. To fold several items into one document, send the batch "+
					"synchronously (async=false), which processes them sequentially."))
			return
		}
	} else if h.RetainBatchEnabled {
		// A synchronous retain when the batch API is on would run for hours and
		// time out in the request, so it is refused with the reference's own
		// explanation of how to proceed.
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, retainBatchDisabledMessage))
		return
	}
	// The reference canonicalizes each item's content after the model validated
	// it, and a block whose payload cannot be decoded there is a 400 naming the
	// item and the block rather than a quietly degraded write.
	for index, item := range req.Items {
		if failure := item.Content.attachmentPayloadFailure("items[" + strconv.Itoa(index) + "].content"); failure != "" {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, failure))
			return
		}
	}
	// Whether the deployment's write model reads images decides what the
	// canonical text of an image-bearing item is: a vision model sees the
	// picture where its placeholder stands, while a text-only one gets the prose
	// with every attachment block dropped — the reference's canonicalizer
	// applies the same rule, and it is not an error either way.
	for index := range req.Items {
		req.Items[index].visionEnabled = h.VisionEnabled
	}
	// Inline attachments are decoded and stored before the retain is submitted,
	// so the canonical text carries only placeholders and the bytes never enter
	// an async operation's payload. A failure to store them refuses the write
	// rather than extracting from a picture the model will never see.
	written, err := h.storeInlineAttachments(r.Context(), bankID, &req)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	// Every refusal from here on has to take those bytes back out: the ingress
	// already wrote them, nothing else reclaims an attachment a retain never
	// referenced, and the reference does exactly this on the memory-clearing
	// refusals below (engine/memory_engine.py:6924-6966, :23257-23287 — the
	// write-scope check moved inside that block in v1.1.0, #5416). The scope is
	// the documents this request names, so a group that already landed keeps its
	// attachments.
	refused := func() { h.discardIngressAttachments(r.Context(), bankID, written, req) }
	// Memory Defense screens the items as the caller submitted them, before the
	// write is converted or folded: the reference decides "every item was blocked"
	// against the submitted count and reports each violation by its position in
	// that list, so screening a folded list would move those positions and let one
	// blocked item take a clean neighbour down with it.
	bankConfig, err := h.resolveBankConfig(r.Context(), httpx.TenantOverridesFrom(r), bankID)
	if err != nil {
		refused()
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	screened, err := h.applyMemoryDefense(r.Context(), bankID, bankConfig, req.Items)
	if err != nil {
		refused()
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	req.Items = screened
	write := toStorageRetainItems(bankID, req, !req.Async)
	// A write may not carry a tag the caller cannot write, so a scoped caller
	// cannot reach another scope's tags through a retain.
	if err := h.retainWriteScopeFailure(r, bankID, req, write); err != nil {
		refused()
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
	// The synchronous write is the reference's observed retain (api/http.py:10279),
	// and it carries neither a budget nor a token count. The observed block is
	// the write itself: the reference opens it after the items have been
	// screened and converted, so a request refused before that is not counted.
	budget, maxTokens := retainOperationLabels()
	var result storage.RetainOutcome
	var bank storage.Bank
	err = serveOperation(r.Context(), operationRetain, budget, maxTokens, func() error {
		result, bank, err = h.executeRetain(r.Context(), bankID, httpx.TenantOverridesFrom(r), write)
		return err
	})
	if err != nil {
		// An abandoned synchronous retain stops work and is answered 499 with
		// the reference's disconnect reason rather than a server fault.
		if httpx.WriteClientDisconnected(w, r) {
			return
		}
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	// The write is durable here, so the events are raised once and only for work
	// that actually landed: a caller polling the operation and a subscriber
	// receiving the delivery see the same completion. The reference raises one
	// event per content item, carrying only what that item contributed, and the
	// events of one retain share a single operation id.
	h.notifyRetainCompleted(r.Context(), bankID, req.Items)
	h.enqueuePostRetainConsolidation(r.Context(), bankID, bank)
	httpx.WriteJSON(w, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// notifyRetainCompleted raises one retain.completed delivery per retained
// content item, which is the reference's shape: a two-item batch delivers two
// events, each with the document that item named, the tags it carried, and how
// many memory units the document holds afterwards (the number the Documents API
// reports). A member the item left unset is absent from the event rather than
// present as null — an item that named no document has no count to report,
// because there is no document to count against.
//
// A synchronous retain has no operation of its own in the reference, so its
// events carry a fresh `uuid4().hex` rather than an operation id; the id is
// minted once so the events of one retain can be recognised as a group.
func (h *Handlers) notifyRetainCompleted(ctx context.Context, bankID string, items []RetainItem) {
	if h.Webhooks == nil {
		return
	}
	operationID := eventOperationID()
	for _, item := range items {
		h.Webhooks.EnqueueWebhookEvent(ctx, bankID, "retain.completed",
			RetainCompletedData(operationID, item.DocumentID, item.Tags, DocumentUnitCount(ctx, h.Store, bankID, item.DocumentID)))
	}
}

// RetainCompletedData renders the data object one retain.completed event
// carries: the tags the item held, the document it wrote to and how many memory
// units that document owns, and the operation the events of one retain share. A
// member the item left unset is absent from the rendered object rather than a
// null, matching the reference's `exclude_none` serialization, and an item that
// named no document reports no count — there is no document to count against.
//
// Both retain routes shape their events here rather than each building the
// object: the synchronous path mints the operation id (it has no operation row
// of its own), and a queued child passes the id of the operation it ran, so the
// two paths cannot drift on what a subscriber sees. tags is passed through as
// the caller holds it, so an in-process subscriber sees the same value the wire
// carries rather than a copy of a different type.
func RetainCompletedData(operationID, documentID string, tags any, memoryUnitCount int) map[string]any {
	data := map[string]any{"operation_id": operationID}
	if documentID != "" {
		data["document_id"] = documentID
		data["memory_unit_count"] = memoryUnitCount
	}
	if tagsPresent(tags) {
		data["tags"] = tags
	}
	return data
}

// tagsPresent reports whether a caller supplied tags worth reporting: an item
// that named none leaves the member out rather than sending an empty list.
func tagsPresent(tags any) bool {
	switch value := tags.(type) {
	case nil:
		return false
	case []string:
		return len(value) > 0
	case TagList:
		return len(value) > 0
	default:
		return true
	}
}

// DocumentUnitCount reports how many memory units a document owns after a write,
// which is what the reference counts for the retain.completed event and what the
// Documents API serves. A document that cannot be read counts zero, the answer a
// count over no matching rows gives, and a call with no document id counts zero
// without touching the store.
func DocumentUnitCount(ctx context.Context, store storage.Store, bankID, documentID string) int {
	if documentID == "" {
		return 0
	}
	document, err := store.GetDocument(ctx, bankID, documentID)
	if err != nil {
		return 0
	}
	return document.MemoryUnitCount
}

// eventOperationID mints the identifier a retain.completed event carries when
// the retain has no operation of its own. The reference uses `uuid4().hex`: a
// bare 32-character lower-case hexadecimal uuid, so the dashes come off the
// same version-4 generator the store's other identifiers use.
func eventOperationID() string {
	return strings.ReplaceAll(storage.NewDocumentID(), "-", "")
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

// errBankConfigRead marks a write refused because the bank's configuration
// could not be read. Screening a write needs that configuration, so an
// unreadable one refuses the write rather than storing it unscreened. It is its
// own sentinel so the refusal is distinguishable from a bank that simply
// configured no policy.
var errBankConfigRead = errors.New("bank configuration could not be read")

// applyDefaultObservationScopes gives every item that names no observation_scopes
// the effective default — default_observation_scopes is strategy-overridable
// (config.py), so the item's own strategy overlays the bank's config before the
// setting is read; without a strategy the bank's own value decides, else the
// deployment's WENYUAN_API_DEFAULT_OBSERVATION_SCOPES. Each value is resolved to
// raw JSON and carried onto the item so consolidation never merges across it. A
// value that is not a mode name or a list of tag-group lists is ignored rather
// than written: the bank's config is validated on its own path, and a
// deployment value nobody validated must not turn a write into one no scope can
// resolve.
func (h *Handlers) applyDefaultObservationScopes(write *storage.RetainRequest, bank storage.Bank) {
	for index := range write.Items {
		if len(write.Items[index].ObservationScopes) != 0 {
			continue
		}
		encoded := defaultObservationScopes(
			strategyConfigOverlay(bank.Config, write.Items[index].Strategy), h.DefaultObservationScopes)
		if len(encoded) == 0 {
			continue
		}
		write.Items[index].ObservationScopes = encoded
	}
}

// defaultObservationScopes resolves the scopes an item without its own would be
// stamped with, from an already strategy-overlaid bank config and the
// deployment's raw value: the bank's default_observation_scopes wins, the
// deployment's applies when the bank names none, and neither form — a mode name
// or a list of tag-group lists — resolves to none.
func defaultObservationScopes(config map[string]any, deployment json.RawMessage) json.RawMessage {
	var value any
	if len(deployment) > 0 {
		value = deployment
	}
	if configured, ok := config["default_observation_scopes"]; ok && configured != nil {
		value = configured
	}
	encoded, ok := observationScopesJSON(value)
	if !ok {
		return nil
	}
	return encoded
}

// observationScopesJSON renders a bank or deployment observation_scopes value as
// the raw JSON the item carries: a mode name as a JSON string, or a list of
// tag-group lists as itself. A value in neither form reports false.
func observationScopesJSON(value any) (json.RawMessage, bool) {
	if raw, isRaw := value.(json.RawMessage); isRaw && len(raw) == 0 {
		return nil, false
	}
	switch typed := value.(type) {
	case json.RawMessage:
		var parsed any
		if err := json.Unmarshal(typed, &parsed); err != nil {
			return nil, false
		}
		return observationScopesJSON(parsed)
	case string:
		if !containsToken(ObservationScopeModes, typed) {
			return nil, false
		}
		encoded, err := json.Marshal(typed)
		return encoded, err == nil
	case []any:
		groups := make([][]string, 0, len(typed))
		for _, element := range typed {
			tags, ok := element.([]any)
			if !ok {
				return nil, false
			}
			group := make([]string, 0, len(tags))
			for _, tag := range tags {
				text, ok := tag.(string)
				if !ok {
					return nil, false
				}
				group = append(group, text)
			}
			groups = append(groups, group)
		}
		encoded, err := json.Marshal(groups)
		return encoded, err == nil
	}
	return nil, false
}

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
	// A submission with nothing to extract queues no work, and the reference
	// reports that as an absent operation rather than an id nothing will ever
	// advance: `items: []` with `async` answers `operation_id: null`.
	if len(req.Items) == 0 {
		httpx.WriteJSON(w, http.StatusOK, BuildRetainResponse(bankID, req, storage.RetainOutcome{ProcessedContentTokens: -1}))
		return
	}
	result, err := h.queueRetainAsync(r.Context(), bankID, httpx.TenantOverridesFrom(r), write)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// queueRetainAsync resolves the bank's configuration onto the write and queues
// it, returning the receipt a caller polls. It is the one path a queued retain
// and a document reprocess both take, so the two queue the same shape of work:
// one parent batch operation that aggregates the submission and one child per
// token-bounded sub-batch. Nothing is extracted here — the work is the queue's.
func (h *Handlers) queueRetainAsync(ctx context.Context, bankID string, overrides map[string]any, write storage.RetainRequest) (storage.RetainOutcome, error) {
	prepared, _, err := h.prepareRetain(ctx, bankID, overrides, write)
	if err != nil {
		return storage.RetainOutcome{}, err
	}
	prepared.Async = true
	// The extraction happens later, on a worker that no longer holds this
	// request, so the document each item lands in must be fixed here: a
	// document's extraction is attributed to it on the model-request trace
	// (the reference's `set_call_metadata({"document_id": effective_doc_id})`),
	// and an id minted during the write is known to nothing outside it — the
	// row would carry no document for an item that named none.
	assignDocumentIDs(&prepared)
	tokensPerBatch := h.RetainSubBatchTokens
	if tokensPerBatch <= 0 {
		tokensPerBatch = storage.DefaultRetainSubBatchTokens
	}
	submission, err := h.Store.SubmitAsyncRetain(ctx, prepared, tokensPerBatch)
	if err != nil {
		return storage.RetainOutcome{}, err
	}
	// The receipt names the parent, which is what the caller polls and what a
	// retried submission with the same identifier rejoins.
	return storage.RetainOutcome{
		OperationID: submission.OperationID,
		// The submission's own identifier is reported in both fields, so a
		// caller that reads either sees the operation it polls. A request that
		// split across several strategy groups is the only case with more than
		// one, and this service queues one.
		OperationIDs: []string{submission.OperationID},
		// No accounting has happened yet, which is reported as absent rather
		// than as zero.
		ProcessedContentTokens: -1,
	}, nil
}

// errAppendWithoutDocumentID refuses an append on an item that named no
// document. The reference's sentence is character for character its own.
var errAppendWithoutDocumentID = errors.New("update_mode='append' requires a document_id")

// errAppendWithoutStoredText refuses an append when the bank stores no document
// text, because there is nothing to append to. It is refused as a client error
// rather than silently degrading to a replace.
//
// The text is the reference's, character for character — it names the setting a
// caller has to change — so the sentence ends in a period, which ST1005 would
// otherwise flag. Contract fidelity wins over the style rule here.
//
//nolint:staticcheck // the reference's own wording, period included
var errAppendWithoutStoredText = errors.New("update_mode='append' is not supported when document text storage (store_document_text / WENYUAN_API_STORE_DOCUMENT_TEXT) is disabled: the prior document text is not stored and cannot be appended to. Use update_mode='replace' instead.")

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
	// Count the document by its extraction outcome. A document whose extraction
	// produced no memory units is stored but unreachable through recall, and
	// this counter is the only surface that shows it.
	monitoring.Workload.CountRetainDocument(countRetainedUnits(outcome))
	return outcome, bank, nil
}

// countRetainedUnits totals the memory units one retain produced, across every
// item it carried.
func countRetainedUnits(outcome storage.RetainOutcome) int {
	total := 0
	for _, ids := range outcome.ItemUnitIDs {
		total += len(ids)
	}
	return total
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
	h.applyDefaultObservationScopes(&write, bank)
	// An append needs a document to append to. The reference checks that on the
	// synchronous path and inside submit_async_retain alike (its
	// _validate_retain_update_modes), so a queued append is refused at submit
	// time with the same 400 instead of reaching the worker — which would mint
	// the append a fresh document after the caller already saw success. It runs
	// before the bank's store_document_text read, so the more specific refusal
	// is this one.
	for _, item := range write.Items {
		if item.UpdateMode == "append" && item.DocumentID == "" {
			return storage.RetainRequest{}, storage.Bank{}, errAppendWithoutDocumentID
		}
	}
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
// Like the reference, it submits a queued batch retain rather than extracting
// in the request: the reply names a pending parent operation with children, and
// the work runs in a worker. The stored text is the input, so reprocess re-reads
// without rewriting it; every retain parameter the document stored is replayed
// (see applyRetainParams) so a re-extraction runs under the same settings the
// document was retained with rather than under the bank defaults. The write is
// forced to re-extract, so an unchanged content hash cannot turn the reprocess
// into a silent no-op.
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
	item := storage.RetainItem{}
	applyRetainParams(&item, doc.RetainParams)
	// These are the reprocess's own and win over anything the stored parameters
	// carried: the document's text and identifier are the input, the mode is
	// replace, and the re-extract is forced.
	item.Content = doc.OriginalText
	item.DocumentID = documentID
	item.UpdateMode = "replace"
	item.ForceReextract = true
	item.Tags = doc.Tags
	write := storage.RetainRequest{BankID: bankID, Items: []storage.RetainItem{item}}
	return h.queueRetainAsync(ctx, bankID, nil, write)
}

// retainParamsNotReplayed names the retain-parameter keys a reprocess does not
// replay. The write carries the document's own content, identifier and mode,
// and the parameters never record the tag list or the force switch; everything
// else the document stored is replayed. This is a deny-list, matching the
// reference, so a newly persisted parameter round-trips through a reprocess
// without a second edit here.
var retainParamsNotReplayed = map[string]bool{
	"content":         true,
	"document_id":     true,
	"update_mode":     true,
	"tags":            true,
	"force_reextract": true,
}

// applyRetainParams restores a document's stored retain parameters onto the
// item a reprocess writes, so the re-extraction runs under the document's own
// settings. A key the item cannot express is ignored; an absent or malformed
// value leaves the field at its zero value rather than failing the reprocess.
func applyRetainParams(item *storage.RetainItem, params map[string]any) {
	for key, value := range params {
		if retainParamsNotReplayed[key] || value == nil {
			continue
		}
		switch key {
		case "strategy":
			item.Strategy = stringFromParams(params, "strategy")
		case "context":
			item.Context = stringFromParams(params, "context")
		case "event_date":
			if eventDate := stringFromParams(params, "event_date"); eventDate != "" {
				// A document retained before v1.1.0 may have kept a year-1/9999
				// placeholder as its event date, which retain now refuses. It
				// replays as undated instead, so its reprocess does not fail
				// forever on a date it can no longer be given (#5820,
				// memory_engine.py:14113-14117).
				if parsed, err := time.Parse(time.RFC3339, eventDate); err == nil &&
					!validation.IsPlaceholderDate(parsed) {
					item.Timestamp = &parsed
					item.HasTimestamp = true
				}
			}
		case "metadata":
			if metadata, ok := value.(map[string]any); ok {
				item.Metadata = metadata
			}
		case "observation_scopes":
			if encoded, err := json.Marshal(value); err == nil {
				item.ObservationScopes = encoded
			}
		case "attachment_filenames":
			item.AttachmentFilenames = attachmentFilenamesFromParams(value)
		case "entities":
			item.Entities = retainEntitiesFromParams(value)
		case "resolve_entities":
			if resolve, ok := value.(bool); ok {
				item.ResolveEntities = resolve
			}
		}
	}
}

// attachmentFilenamesFromParams reads a stored attachment-filename map back into
// the storage shape. It arrives as map[string]string from the in-process store,
// which never round-trips through JSON, and as map[string]any from the postgres
// store, which does. An entry that is not a pair of non-empty strings is dropped
// rather than failing the reprocess.
func attachmentFilenamesFromParams(value any) map[string]string {
	entries, ok := value.(map[string]any)
	if !ok {
		if direct, isStrings := value.(map[string]string); isStrings {
			return direct
		}
		return nil
	}
	names := make(map[string]string, len(entries))
	for id, name := range entries {
		text, isText := name.(string)
		if id == "" || !isText || text == "" {
			continue
		}
		names[id] = text
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// retainEntitiesFromParams reads a stored entity list back into the storage
// shape. The reference records each entity as {"text", "type"}; a value of any
// other shape yields no entities rather than failing the reprocess. The list
// arrives as []map[string]any from the in-process store, which never round-trips
// through JSON, and as []any from the postgres store, which does.
func retainEntitiesFromParams(value any) []storage.RetainEntity {
	var entries []any
	switch typed := value.(type) {
	case []any:
		entries = typed
	case []map[string]any:
		entries = make([]any, 0, len(typed))
		for _, entry := range typed {
			entries = append(entries, entry)
		}
	default:
		return nil
	}
	out := make([]storage.RetainEntity, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		text, _ := object["text"].(string)
		if text == "" {
			continue
		}
		kind, _ := object["type"].(string)
		out = append(out, storage.RetainEntity{Text: text, Kind: kind})
	}
	return out
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
		if result.OperationID != "" {
			id := result.OperationID
			response.OperationID = &id
		}
		// operation_ids stays null: this service queues a single strategy group,
		// and the reference serializes operation_ids as null whenever only one
		// group was submitted.
		return response
	}
	response.Usage = &Usage{
		InputTokens:  result.Usage.InputTokens,
		OutputTokens: result.Usage.OutputTokens,
		TotalTokens:  result.Usage.TotalTokens,
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
	case errors.Is(err, errAppendWithoutDocumentID):
		return httpx.NewHTTPError(httpx.KindBadRequest, errAppendWithoutDocumentID.Error())
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
		httpx.WriteValidationFailure(w, failure)
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
	// A client-supplied identity is stored in its canonical spelling, which is
	// what the reference's uuid-typed validator produces.
	operationID := req.OperationID
	if canonical, ok := canonicalOperationID(operationID); ok {
		operationID = canonical
	}
	return storage.RetainRequest{
		BankID:       bankID,
		Items:        stored,
		Async:        req.Async,
		OperationID:  operationID,
		DocumentTags: req.DocumentTags,
	}
}

func toStorageRetainItem(item RetainItem) storage.RetainItem {
	stored := storage.RetainItem{
		// The ids this request recorded are exempt from the placeholder scrub:
		// an upload minted its placeholder from an attachment the same request
		// carries, so scrubbing it would store the bytes and drop the reference
		// to them. Authored text carrying someone else's token is still
		// scrubbed.
		Content:             canonicalItemText(item),
		Parts:               storageItemParts(item),
		Context:             item.Context,
		Metadata:            DropNullValues(item.Metadata),
		DocumentID:          item.DocumentID,
		Entities:            toStorageEntities(item.Entities),
		ResolveEntities:     resolveEntitiesOrDefault(item.ResolveEntities),
		Tags:                item.Tags,
		ObservationScopes:   item.ObservationScopes.RawJSON(),
		Strategy:            item.Strategy,
		UpdateMode:          item.UpdateMode,
		AttachmentIDs:       item.AttachmentIDs,
		Attachments:         toStorageAttachments(item.Attachments),
		AttachmentFilenames: item.AttachmentFilenames,
	}
	if item.Timestamp.Set {
		stored.HasTimestamp = true
		stored.Timestamp = item.Timestamp.Value
	}
	return stored
}

// canonicalItemText renders the text a write stores for one item, which is what
// the reference's canonicalize_item_content produces: a model that reads images
// keeps each attachment as a placeholder in position, and one that does not keeps
// only the prose, with the attachment's position left as a paragraph break.
//
// The text a write *stores* and the text the *extractor* is asked about may
// differ once an attachment is already a flat string rather than a block: a
// placeholder the reference's canonicalizer minted (from a block form, or from
// an upload that carries the bytes) is resolved back into the picture at prompt
// time, so it stays. A placeholder no canonicalizer minted — an upload whose
// model cannot read images — is not a reference to anything the model can be
// shown, so the prose is what it reads.
func canonicalItemText(item RetainItem) string {
	return canonicalText(item)
}

// canonicalText is canonicalItemText's body, shared with the per-block
// canonicalization a folded item's Parts go through.
func canonicalText(item RetainItem) string {
	if item.textIsCanonical && !item.Content.IsList {
		if item.visionEnabled {
			return item.Content.Text
		}
		// A text-only model is handed the prose only, so the document stores
		// the prose only — whether the placeholder came from a block form or
		// from an upload that carries its own bytes.
		return stripPlaceholders(item.Content.Text)
	}
	if !item.visionEnabled {
		return item.Content.FlattenDecliningAttachments(item.AttachmentIDs)
	}
	return item.Content.FlattenKeepingAttachments(item.AttachmentIDs)
}

// storageItemParts carries a folded item's content blocks to the storage layer.
// Each block is canonicalized the way the item's own text is, so the blocks the
// write chunks from are the blocks the extraction was handed. A single-block
// item reports none: its Parts would only restate its Content.
func storageItemParts(item RetainItem) []string {
	if len(item.Parts) <= 1 {
		return nil
	}
	parts := make([]string, 0, len(item.Parts))
	for _, part := range item.Parts {
		parts = append(parts, canonicalText(RetainItem{Content: Content{Text: part}, textIsCanonical: item.textIsCanonical, visionEnabled: item.visionEnabled, AttachmentIDs: item.AttachmentIDs}))
	}
	return parts
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

// assignDocumentIDs gives every item an explicit document id, minting one where
// the caller named none, before the write is queued.
//
// `RetainItem.DocumentID` is optional here in a way it is not in the reference:
// that engine resolves an `effective_doc_id` per item — the caller's, else a
// fresh uuid — and hands every chunk of that item the id. This service's store
// instead mints a fresh id per item at write time, which the caller never
// learns and which therefore cannot be put on anything it reports about that
// item. Resolving it at submission keeps the same guarantee — the extraction of
// a document is attributed to that document — and costs nothing when the caller
// already named one.
//
// Each unnamed item gets its own id. An async batch is a set of independent
// documents: sharing one id across its items would make the second item's
// default replace-mode write erase the first one's units. An item that already
// has an id keeps it, so a resubmission and a client-chosen id are unaffected.
func assignDocumentIDs(req *storage.RetainRequest) {
	for i := range req.Items {
		if req.Items[i].DocumentID == "" {
			req.Items[i].DocumentID = storage.NewDocumentID()
		}
	}
}

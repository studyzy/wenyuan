package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/embed"
	"github.com/studyzy/wenyuan/internal/storage"
)

// tracingProvider records every completion in the model-request trace before
// returning its result, so the /llm-requests endpoints reflect the calls that
// actually ran rather than an empty table.
//
// Recording is best-effort: a trace write that fails is dropped rather than
// failing the model call, because the answer is what the caller asked for and
// the trace is diagnostic.
type tracingProvider struct {
	provider.Provider
	store storage.Store
	cfg   config.ProviderConfig
}

// RerankCapability lets ProviderReranker see through the wrapper, so tracing a
// provider never advertises a reranker the inner provider does not have. The
// capability is wrapped too, so a recall that reranks through the chat
// provider's own cross-encoder is traced next to the chat rows it serves.
func (p *tracingProvider) RerankCapability() provider.Reranker {
	inner := provider.ProviderReranker(p.Provider)
	if inner == nil {
		return nil
	}
	return TraceReranker(inner, p.store, p.cfg, traceBackend{provider: p.Name(), model: p.cfg.Model})
}

// TraceProvider wraps a provider so its completions are recorded in the
// model-request trace. When tracing is disabled, or the provider is nil, it
// returns the provider unchanged.
func TraceProvider(p provider.Provider, store storage.Store, cfg config.ProviderConfig) provider.Provider {
	if p == nil || !cfg.TraceEnabled {
		return p
	}
	return &tracingProvider{Provider: p, store: store, cfg: cfg}
}

// Complete runs the completion and records the attempt.
func (p *tracingProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if !p.tracesScope(req.Scope) {
		return p.Provider.Complete(ctx, req)
	}
	start := time.Now().UTC()
	result, err := p.Provider.Complete(ctx, req)
	p.record(ctx, req, result, err, start)
	return result, err
}

// tracesScope reports whether the call's scope is recorded.
func (p *tracingProvider) tracesScope(scope string) bool {
	return traceScopeRecorded(p.cfg, scope)
}

// The trace scopes the embedding and rerank calls land under — the reference's
// own scope vocabulary, which is what the /llm-requests `call_type` filter
// derives the call kinds from (engine/llm_trace.py:643-647): an embedding row
// names its input type, the reranker's scope is exact.
const (
	embeddingDocumentTraceScope = "embedding_document"
	embeddingQueryTraceScope    = "embedding_query"
	rerankTraceScope            = "rerank"
)

// traceScopeRecorded reports whether a call in scope is recorded, with the
// reference recorder's precedence (engine/llm_trace.py:705-713): a scope the
// exclude list names is never recorded — exclusion wins over the allow list —
// an empty allow list records every scope, and both lists match with a
// trailing-`*` prefix rule rather than exact equality.
func traceScopeRecorded(cfg config.ProviderConfig, scope string) bool {
	if scopeMatchesPattern(scope, cfg.TraceExcludeScopes) {
		return false
	}
	if len(cfg.TraceScopes) == 0 {
		return true
	}
	return scopeMatchesPattern(scope, cfg.TraceScopes)
}

// scopeMatchesPattern reports whether scope matches one of the patterns: a
// pattern ending in `*` matches every scope carrying that prefix, the rest must
// equal the scope (engine/llm_trace.py:605-612).
func scopeMatchesPattern(scope string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(scope, strings.TrimSuffix(pattern, "*")) {
				return true
			}
			continue
		}
		if pattern == scope {
			return true
		}
	}
	return false
}

// traceBackend names the backend a traced row labels itself with: the
// reference reads the row's provider and model off the owner it wraps
// (engine/llm_trace.py:206-215).
type traceBackend struct {
	provider string
	model    string
}

// tracedCall is one finished model call reduced to the columns the trace row
// records. The completion, embedding and rerank wrappers each fill one; the
// shared emit assembles the row.
type tracedCall struct {
	scope   string
	backend traceBackend
	start   time.Time
	callErr error
	input   any
	output  any
	llmInfo map[string]any
	usage   provider.Usage
}

// record writes one trace row for a completed completion attempt.
func (p *tracingProvider) record(ctx context.Context, req provider.CompletionRequest, result provider.CompletionResult, callErr error, start time.Time) {
	// The trace records the *configured* model, not the one the response
	// echoes: the reference passes `model=self.model`, the LLM wrapper's own
	// resolved model (engine/llm_wrapper.py:1370, :1519). A gateway may answer
	// with a different spelling than the one configured (it strips its vendor
	// prefix, for instance), and the trace is meant to say which model the
	// deployment asked for.
	model := p.cfg.Model
	if model == "" {
		model = result.Model
	}
	emitTraceRow(p.store, p.cfg, ctx, tracedCall{
		scope:   req.Scope,
		backend: traceBackend{provider: p.Name(), model: model},
		start:   start,
		callErr: callErr,
		input:   traceInput(req, p.cfg.TraceMaxChars),
		output:  traceOutput(result, p.cfg.TraceMaxChars),
		llmInfo: traceLLMInfo(req, result, p.cfg.TraceMaxChars),
		usage:   result.Usage,
	})
}

// emitTraceRow assembles one trace row from a finished model call and hands it
// to the recorder. Best-effort: the recorder never returns an error to the
// caller, because the answer is what the caller asked for and the trace is
// diagnostic.
func emitTraceRow(store storage.Store, cfg config.ProviderConfig, ctx context.Context, call tracedCall) {
	ended := time.Now().UTC()
	if call.start.IsZero() {
		// A caller that never stamped a start time still gets a row the store
		// can record: the write runs on a background goroutine, and a zero
		// started_at is what the #llm-requests window filter cannot see.
		call.start = ended
	}
	durationMS := int(ended.Sub(call.start).Milliseconds())
	status := "success"
	errText := ""
	if call.callErr != nil {
		status = "error"
		errText = call.callErr.Error()
	}
	attribution, _ := provider.TraceContextFrom(ctx)
	// The OTel-style hierarchy the reference records (engine/llm_trace.py:530-536):
	// one trace id and one operation span per operation invocation, shared by
	// every model call of that run, and one fresh span per call that points at
	// the operation span as its parent. The context is the operation scope — the
	// caller attaches attribution once per run — so the pair is derived from it
	// and every call in the run agrees.
	traceID, operationSpanID := traceIDsFor(ctx)
	output := call.output
	if call.callErr != nil {
		// A failed call shows no output, the reference's `output=None if error
		// is not None else response_content` (engine/llm_trace.py:528).
		output = nil
	}
	request := storage.LLMRequest{
		BankID:       attribution.BankID,
		Operation:    attribution.Operation,
		Scope:        call.scope,
		TraceID:      traceID,
		SpanID:       newTraceSpanID(),
		ParentSpanID: operationSpanID,
		Provider:     call.backend.provider,
		Model:        call.backend.model,
		Status:       status,
		StartedAt:    call.start,
		EndedAt:      &ended,
		DurationMS:   &durationMS,
		Input:        truncateTrace(call.input, cfg.TraceMaxChars),
		Output:       truncateTrace(output, cfg.TraceMaxChars),
		Error:        errText,
		LLMInfo:      traceLLMInfoPayload(call.llmInfo, cfg.TraceMaxChars),
		Metadata:     traceMetadata(attribution),
	}
	setToken(&request.InputTokens, call.usage.InputTokens)
	setToken(&request.OutputTokens, call.usage.OutputTokens)
	setToken(&request.CachedTokens, call.usage.CachedInputTokens)
	setToken(&request.ThoughtsTokens, call.usage.ReasoningTokens)
	setToken(&request.TotalTokens, call.usage.TotalTokens)
	traceRecorder(store).record(ctx, request)
}

// setToken stores a token count, leaving it NULL when the provider reported
// nothing, so an unreported count is distinguishable from a genuine zero.
func setToken(target **int, value int) {
	if value == 0 {
		return
	}
	*target = &value
}

// traceMetadata renders the row's `metadata` from the call's attribution: the
// object a caller merged onto the trace context for this call.
//
// The reference always writes an object — `_safe_json(record.metadata) or "{}"`
// (engine/llm_trace.py:899) — so a call that bound nothing stores `{}` rather
// than NULL, and the `?document_id=` and `?memory_id=` filters read one shape
// whichever call wrote the row.
func traceMetadata(attribution provider.TraceContext) map[string]any {
	metadata := make(map[string]any, len(attribution.Metadata))
	for key, value := range attribution.Metadata {
		metadata[key] = value
	}
	return metadata
}

// traceInput renders the trace column the way the reference stores it: the
// message list itself (engine/llm_trace.py:616-617 writes `record.input`, which
// the recorder fills with `list(messages)`), not a wrapper object.
func traceInput(req provider.CompletionRequest, maxChars int) any {
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, map[string]any{
			"role":    string(message.Role),
			"content": message.Content,
		})
	}
	return truncateTrace(messages, maxChars)
}

// traceOutput renders the response for the trace: the completion's content as
// the reference stores it (`output=None if error is not None else
// response_content`, engine/llm_trace.py:528), not a wrapper object. A failed
// call's output is dropped by the shared emit.
func traceOutput(result provider.CompletionResult, maxChars int) any {
	return truncateTrace(result.Content, maxChars)
}

// traceLLMInfo renders the per-call requested parameters the reference records
// in `llm_info` — the caller's requested shape, not the prompt. It only carries
// values the caller actually set, so an unset member is absent rather than null.
func traceLLMInfo(req provider.CompletionRequest, result provider.CompletionResult, maxChars int) map[string]any {
	request := map[string]any{}
	if req.MaxTokens > 0 {
		request["max_completion_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		request["temperature"] = *req.Temperature
	}
	info := map[string]any{}
	if len(request) > 0 {
		info["request"] = request
	}
	if result.FinishReason != "" {
		info["finish_reason"] = result.FinishReason
	}
	if len(info) == 0 {
		return map[string]any{}
	}
	// Clipping reads a cut document back into an object, so the stored row
	// keeps the decoded form the storage layer takes.
	return traceLLMInfoPayload(info, maxChars)
}

// traceLLMInfoPayload clips an llm_info object to the character budget. The
// storage layer takes the row's llm_info as a decoded object, so a clipped
// payload is read back into one.
func traceLLMInfoPayload(info map[string]any, maxChars int) map[string]any {
	if clipped := truncateTrace(info, maxChars); clipped != nil {
		if raw, ok := clipped.(json.RawMessage); ok {
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err == nil {
				return decoded
			}
		}
	}
	return info
}

// tracingEmbedder records every embedding call in the model-request trace, the
// way the reference wraps its embedding backends' encode calls in
// trace_model_call (engine/retain/embedding_utils.py:139): one row per call,
// its input the text list, its llm_info the input count, and no output — an
// embedding's vectors would only be noise.
type tracingEmbedder struct {
	embed.Embedder
	store storage.Store
	cfg   config.ProviderConfig
}

// TraceEmbedder wraps an embedding backend so its calls are recorded in the
// model-request trace. When tracing is disabled, or the backend is nil, it
// returns the backend unchanged.
func TraceEmbedder(e embed.Embedder, store storage.Store, cfg config.ProviderConfig) embed.Embedder {
	if e == nil || !cfg.TraceEnabled {
		return e
	}
	return &tracingEmbedder{Embedder: e, store: store, cfg: cfg}
}

// Embed runs the embedding call and records the attempt. The scope follows the
// input kind the reference's own scopes name: stored passages are
// embedding_document, recall queries embedding_query.
func (e *tracingEmbedder) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	scope := embeddingDocumentTraceScope
	if req.Kind == provider.EmbeddingQuery {
		scope = embeddingQueryTraceScope
	}
	if !traceScopeRecorded(e.cfg, scope) {
		return e.Embedder.Embed(ctx, req)
	}
	start := time.Now().UTC()
	result, err := e.Embedder.Embed(ctx, req)
	emitTraceRow(e.store, e.cfg, ctx, tracedCall{
		scope:   scope,
		backend: traceBackend{provider: e.Name(), model: e.Model()},
		start:   start,
		callErr: err,
		input:   map[string]any{"texts": req.Texts},
		llmInfo: map[string]any{"input_count": len(req.Texts)},
		usage:   result.Usage,
	})
	return result, err
}

// tracingReranker records every rerank call in the model-request trace, the
// way the reference wraps its cross-encoder backends' predict calls in
// trace_model_call (engine/cross_encoder.py:220): one row per call, its input
// the deduplicated queries and the candidate documents, its output the scores,
// its llm_info the input count.
type tracingReranker struct {
	provider.Reranker
	store   storage.Store
	cfg     config.ProviderConfig
	backend traceBackend
}

// TraceReranker wraps a reranker so its calls are recorded in the model-request
// trace. backend names the row's provider and model, the labels the reference
// reads off the backend it wraps. When tracing is disabled, or the reranker is
// nil, it returns the reranker unchanged.
func TraceReranker(r provider.Reranker, store storage.Store, cfg config.ProviderConfig, backend traceBackend) provider.Reranker {
	if r == nil || !cfg.TraceEnabled {
		return r
	}
	return &tracingReranker{Reranker: r, store: store, cfg: cfg, backend: backend}
}

// ScoresByRankPosition forwards the wrapped backend's capability. The wrapper
// must not hide it: the recall pipeline asks the reranker it was handed, so a
// rank-position backend behind this wrapper would otherwise be read as a
// pointwise one and have its positions published as relevance.
func (r *tracingReranker) ScoresByRankPosition() bool {
	scorer, ok := r.Reranker.(interface{ ScoresByRankPosition() bool })
	return ok && scorer.ScoresByRankPosition()
}

// PrunesCandidates forwards the wrapped backend's capability, for the same
// reason: a pruning backend behind this wrapper must still shrink recall.
func (r *tracingReranker) PrunesCandidates() bool {
	pruner, ok := r.Reranker.(interface{ PrunesCandidates() bool })
	return ok && pruner.PrunesCandidates()
}

// SeedsFromFused forwards the wrapped backend's capability, so the RRF
// passthrough keeps seeding recall from the fused order through the wrapper.
func (r *tracingReranker) SeedsFromFused() bool {
	seeder, ok := r.Reranker.(interface{ SeedsFromFused() bool })
	return ok && seeder.SeedsFromFused()
}

// Rerank runs the scoring call and records the attempt.
func (r *tracingReranker) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	if !traceScopeRecorded(r.cfg, rerankTraceScope) {
		return r.Reranker.Rerank(ctx, req)
	}
	start := time.Now().UTC()
	result, err := r.Reranker.Rerank(ctx, req)
	emitTraceRow(r.store, r.cfg, ctx, tracedCall{
		scope:   rerankTraceScope,
		backend: r.backend,
		start:   start,
		callErr: err,
		input:   map[string]any{"queries": []string{req.Query}, "documents": req.Documents},
		output:  map[string]any{"scores": result.Scores},
		llmInfo: map[string]any{"input_count": len(req.Documents)},
	})
	return result, err
}

// rerankTraceBackend derives the row identity for the dedicated reranker
// chain: the chain's selected provider and the primary member's configured
// model, the labels the reference reads off the rerank backend it wraps. The
// member mapping is the one the chain itself is built from, so the row names
// the model the same selection chose.
func rerankTraceBackend(cfg config.ProviderConfig) traceBackend {
	return traceBackend{provider: cfg.Rerank.Provider, model: rerankMember(cfg.Rerank.Primary).Model}
}

// truncateTrace serialises a trace payload and clips it to the character
// budget. A budget of zero or less disables truncation. An oversized payload is
// wrapped the way the reference wraps it — a marker object carrying the original
// length and a preview — so the stored value stays valid JSON rather than being
// cut mid-document (engine/llm_trace.py:297-299).
func truncateTrace(payload any, maxChars int) any {
	if payload == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	if maxChars > 0 && len(raw) > maxChars {
		wrapped, err := json.Marshal(map[string]any{
			"_truncated":      true,
			"_original_chars": len(raw),
			"preview":         string(raw[:maxChars]),
		})
		if err != nil {
			return payload
		}
		return json.RawMessage(wrapped)
	}
	return json.RawMessage(raw)
}

// traceScopeIDs is the trace id and operation span a single operation
// invocation's model calls are grouped under.
type traceScopeIDs struct {
	traceID         string
	operationSpanID string
}

// traceScopeKey keys the per-operation id pair on the values the operation
// attached to its context. Two calls from the same run share it, and a later
// run of the same operation on the same bank gets a fresh pair.
type traceScopeKey struct {
	bankID    string
	operation string
}

// traceIDsFor returns the trace id and operation span id for the run the call
// belongs to, minting and remembering them the first time the run is seen.
//
// The reference mints both in `with_config` — once per operation invocation
// (engine/llm_wrapper.py:1683-1693) — and stamps them on every row of that run
// (engine/llm_trace.py:530-536). The run here is identified by the attribution
// its caller attached, which is what the context carries.
func traceIDsFor(ctx context.Context) (traceID, operationSpanID string) {
	attribution, ok := provider.TraceContextFrom(ctx)
	if !ok || (attribution.BankID == "" && attribution.Operation == "") {
		// A call with no attribution is not part of a known run; it still gets
		// its own trace rather than a null one, so every row is addressable.
		return newTraceSpanID(), newTraceSpanID()
	}
	key := traceScopeKey{bankID: attribution.BankID, operation: attribution.Operation}
	traceScopeMu.Lock()
	defer traceScopeMu.Unlock()
	if pair, seen := traceScopes[key]; seen {
		return pair.traceID, pair.operationSpanID
	}
	pair := traceScopeIDs{traceID: newTraceSpanID(), operationSpanID: newTraceSpanID()}
	if traceScopes == nil {
		traceScopes = make(map[traceScopeKey]traceScopeIDs)
	}
	traceScopes[key] = pair
	return pair.traceID, pair.operationSpanID
}

// newTraceSpanID mints a canonical (dashed) uuid the way the reference's
// str(uuid.uuid4()) renders one.
func newTraceSpanID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		binary.BigEndian.PutUint64(raw[:], uint64(time.Now().UnixNano()))
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(raw[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

var (
	traceScopeMu sync.Mutex
	traceScopes  = make(map[traceScopeKey]traceScopeIDs)
)

// traceFlushInterval and traceFlushMaxRows are the reference's buffering
// thresholds (engine/llm_trace.py:647-648): a row waits at most this long, and
// this many rows trigger a write at once.
//
// They are variables rather than constants only so a test can shrink the
// interval; production reads them once and never writes them after start.
//
// Buffering exists because a recall's embedding, rerank and decision calls would
// otherwise each take a pooled connection for one small row; the reference pays
// for that with rows still in the buffer being lost on a crash, and accepts it
// (engine/llm_trace.py:640-645).
var (
	traceFlushInterval = 500 * time.Millisecond
	traceFlushMaxRows  = 500
)

// traceWriteTimeout bounds one batch write, so a wedged database cannot pile
// flushes up behind a stuck connection.
const traceWriteTimeout = 5 * time.Second

// llmTraceRecorder buffers trace rows and writes them in batches, one INSERT
// per flush rather than one per model call.
type llmTraceRecorder struct {
	store storage.Store

	mu     sync.Mutex
	buffer []pendingTraceRow
	// rowsWritten records the trace ids that have produced at least one row,
	// newest last. It is what lets attachMemoryIDs skip a trace that never wrote
	// anything: an UPDATE for it would match zero rows and cost a connection
	// (engine/llm_trace.py:670-690). Bounded, and only ever holds ids.
	rowsWritten *lruSet
	timer       *time.Timer
	timerSet    bool
	// pending counts the metadata patches still in flight, so a shutdown can wait
	// for them: the reference drains them on the way out
	// (engine/llm_trace.py:865-875 over its PendingWrites). A patch is the only
	// trace write that outlives the call that scheduled it.
	pending sync.WaitGroup
	// closed stops accepting rows: a flush after shutdown would fail against a
	// closed pool on every call, which is noise rather than diagnosis.
	closed bool
}

// pendingTraceRow is one buffered row and the attribution context it was
// recorded under, so the write runs on a background goroutine that no longer
// has the request's context values.
type pendingTraceRow struct {
	request storage.LLMRequest
}

// traceRecorders keeps one recorder per store, because the trace entry points
// (TraceProvider, TraceEmbedder, TraceReranker) each reach a buffer and all of
// them must land in the same one for the batching to mean anything.
var (
	traceRecorderMu sync.Mutex
	traceRecorders  = map[storage.Store]*llmTraceRecorder{}
)

// traceRecorder returns the process-wide recorder for a store.
func traceRecorder(store storage.Store) *llmTraceRecorder {
	traceRecorderMu.Lock()
	defer traceRecorderMu.Unlock()
	recorder, ok := traceRecorders[store]
	if !ok {
		recorder = &llmTraceRecorder{store: store, rowsWritten: newLRUSet(traceRowsWrittenMax)}
		traceRecorders[store] = recorder
	}
	return recorder
}

// record buffers one row, writing at once when the buffer is full and otherwise
// arming the flush timer.
func (r *llmTraceRecorder) record(ctx context.Context, request storage.LLMRequest) {
	if ctx.Err() != nil {
		// The call already lost its caller; a trace row for it is not worth
		// writing against a cancelled context.
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.buffer = append(r.buffer, pendingTraceRow{request: request})
	if request.TraceID != "" {
		r.rowsWritten.add(request.TraceID)
	}
	full := len(r.buffer) >= traceFlushMaxRows
	if !full && !r.timerSet {
		r.timerSet = true
		r.timer = time.AfterFunc(traceFlushInterval, r.flushAsync)
	}
	r.mu.Unlock()
	if full {
		r.flushAsync()
	}
}

// flushAsync takes the buffer and writes it on its own goroutine.
func (r *llmTraceRecorder) flushAsync() {
	batch := r.take()
	if len(batch) == 0 {
		return
	}
	go r.write(batch)
}

// flush writes everything buffered now. It is what a shutdown calls, so rows
// recorded just before the process stops are not lost with it.
func (r *llmTraceRecorder) flush() {
	if batch := r.take(); len(batch) > 0 {
		r.write(batch)
	}
}

// take empties the buffer, disarming the timer with it.
func (r *llmTraceRecorder) take() []pendingTraceRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch := r.buffer
	r.buffer = nil
	r.timerSet = false
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	return batch
}

// write persists one batch. A row the database refuses loses only itself: the
// reference retries a failed batch one row at a time for exactly that reason
// (engine/llm_trace.py:915-925). Errors are logged, never surfaced — the caller
// has already returned its model answer.
func (r *llmTraceRecorder) write(batch []pendingTraceRow) {
	ctx, cancel := context.WithTimeout(context.Background(), traceWriteTimeout)
	defer cancel()
	for _, row := range batch {
		if err := r.store.RecordLLMRequest(ctx, row.request); err != nil {
			slog.Default().Warn("LLM trace write failed",
				"bank_id", row.request.BankID, "scope", row.request.Scope, "error", err)
		}
	}
}

// closeTraceRecorder writes what is buffered and stops accepting new rows. It
// is what a shutdown calls, so the rows recorded before the process stopped are
// not lost with it.
func closeTraceRecorder(store storage.Store) {
	traceRecorderMu.Lock()
	recorder, ok := traceRecorders[store]
	delete(traceRecorders, store)
	traceRecorderMu.Unlock()
	if !ok {
		return
	}
	recorder.mu.Lock()
	recorder.closed = true
	recorder.mu.Unlock()
	recorder.flush()
	// And wait for the metadata patches still in flight, so a shutdown does not
	// lose the last one: each is bounded by traceWriteTimeout, so this cannot
	// hang the process.
	recorder.pending.Wait()
}

// traceRowsWrittenMax bounds the trace-id memory. Evicting the oldest can only
// cause a missed patch on a very long-lived trace, never a wrong one, and the
// patch is best-effort metadata either way (engine/llm_trace.py:684-690).
const traceRowsWrittenMax = 4096

// lruSet is a bounded set of strings, newest last.
type lruSet struct {
	order []string
	index map[string]int
	limit int
}

func newLRUSet(limit int) *lruSet {
	return &lruSet{index: map[string]int{}, limit: limit}
}

func (s *lruSet) add(value string) {
	if _, seen := s.index[value]; seen {
		return
	}
	s.order = append(s.order, value)
	s.index[value] = len(s.order) - 1
	for len(s.order) > s.limit {
		oldest := s.order[0]
		delete(s.index, oldest)
		s.order = s.order[1:]
		for key, at := range s.index {
			s.index[key] = at - 1
		}
	}
}

func (s *lruSet) has(value string) bool {
	_, ok := s.index[value]
	return ok
}

// attachMemoryIDs maps a finished operation's memory_units onto every row of
// its trace.
//
// The reference merges the ids the caller passes with the ones accumulated on the
// operation's trace context, de-duplicates them preserving order, and patches
// `metadata` on every row sharing the trace id: `memory_ids` for the units the
// operation produced, `source_memory_ids` for the ones it consumed
// (engine/llm_trace.py:931-970). The patch runs after the rows are flushed, so it
// updates rows that exist rather than racing ahead of them, and it is
// fire-and-forget: the retain or consolidation that just finished never waits on
// a trace write.
func (r *llmTraceRecorder) attachMemoryIDs(ctx context.Context, created, source []string) {
	attribution, ok := provider.TraceContextFrom(ctx)
	if !ok || attribution.IsEmpty() {
		return
	}
	created = distinctIDs(append(append([]string{}, created...), metadataIDs(attribution.Metadata, "memory_ids")...))
	source = distinctIDs(append(append([]string{}, source...), metadataIDs(attribution.Metadata, "source_memory_ids")...))
	patch := map[string]any{}
	if len(created) > 0 {
		patch["memory_ids"] = created
	}
	if len(source) > 0 {
		patch["source_memory_ids"] = source
	}
	if len(patch) == 0 {
		return
	}
	traceID, _ := traceIDsFor(ctx)
	r.mu.Lock()
	written := r.rowsWritten.has(traceID)
	r.mu.Unlock()
	if !written {
		// Every call of the run was excluded by scope, so there is no row to
		// patch.
		return
	}
	// Flush first: the rows this patch targets may still be buffered, and an
	// UPDATE would otherwise run before they exist (:971-980).
	r.flush()
	r.pending.Add(1)
	go func() {
		defer r.pending.Done()
		writeCtx, cancel := context.WithTimeout(context.Background(), traceWriteTimeout)
		defer cancel()
		if err := r.store.PatchLLMRequestMetadata(writeCtx, attribution.BankID, traceID, patch); err != nil {
			slog.Default().Warn("LLM trace memory_id attach failed",
				"bank_id", attribution.BankID, "trace_id", traceID, "error", err)
		}
	}()
}

// metadataIDs reads a list of ids a caller may already have bound onto the trace
// metadata.
func metadataIDs(metadata map[string]any, key string) []string {
	raw, ok := metadata[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]string)
	if !ok {
		values, isAny := raw.([]any)
		if !isAny {
			return nil
		}
		items = make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				items = append(items, text)
			}
		}
	}
	return items
}

// distinctIDs de-duplicates preserving first-seen order, dropping empties.
func distinctIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

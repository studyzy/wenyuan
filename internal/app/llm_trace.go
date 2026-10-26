package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
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
// provider never advertises a reranker the inner provider does not have.
func (p *tracingProvider) RerankCapability() provider.Reranker {
	return provider.ProviderReranker(p.Provider)
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

// tracesScope reports whether the call's scope is recorded. An empty scope
// filter records every scope.
func (p *tracingProvider) tracesScope(scope string) bool {
	if len(p.cfg.TraceScopes) == 0 {
		return true
	}
	for _, allowed := range p.cfg.TraceScopes {
		if allowed == scope {
			return true
		}
	}
	return false
}

// record writes one trace row for a completed attempt.
func (p *tracingProvider) record(ctx context.Context, req provider.CompletionRequest, result provider.CompletionResult, callErr error, start time.Time) {
	ended := time.Now().UTC()
	durationMS := int(ended.Sub(start).Milliseconds())
	status := "success"
	errText := ""
	if callErr != nil {
		status = "error"
		errText = callErr.Error()
	}
	attribution, _ := provider.TraceContextFrom(ctx)
	// The OTel-style hierarchy the reference records (engine/llm_trace.py:530-536):
	// one trace id and one operation span per operation invocation, shared by
	// every model call of that run, and one fresh span per call that points at
	// the operation span as its parent. The context is the operation scope — the
	// caller attaches attribution once per run — so the pair is derived from it
	// and every call in the run agrees.
	traceID, operationSpanID := traceIDsFor(ctx)
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
	llmInfo := traceLLMInfo(req, result, p.cfg.TraceMaxChars)
	request := storage.LLMRequest{
		BankID:       attribution.BankID,
		Operation:    attribution.Operation,
		Scope:        req.Scope,
		TraceID:      traceID,
		SpanID:       newTraceSpanID(),
		ParentSpanID: operationSpanID,
		Provider:     p.Name(),
		Model:        model,
		Status:       status,
		StartedAt:    start,
		EndedAt:      &ended,
		DurationMS:   &durationMS,
		Input:        traceInput(req, p.cfg.TraceMaxChars),
		Output:       traceOutput(result, callErr, p.cfg.TraceMaxChars),
		Error:        errText,
		LLMInfo:      llmInfo,
	}
	setToken(&request.InputTokens, result.Usage.InputTokens)
	setToken(&request.OutputTokens, result.Usage.OutputTokens)
	setToken(&request.CachedTokens, result.Usage.CachedInputTokens)
	setToken(&request.ThoughtsTokens, result.Usage.ReasoningTokens)
	setToken(&request.TotalTokens, result.Usage.TotalTokens)
	_ = p.store.RecordLLMRequest(ctx, request)
}

// setToken stores a token count, leaving it NULL when the provider reported
// nothing, so an unreported count is distinguishable from a genuine zero.
func setToken(target **int, value int) {
	if value == 0 {
		return
	}
	*target = &value
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
// response_content`, engine/llm_trace.py:528), not a wrapper object.
func traceOutput(result provider.CompletionResult, callErr error, maxChars int) any {
	if callErr != nil {
		return nil
	}
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
	// Truncation yields a JSON document; the storage layer takes the row's
	// llm_info as a decoded object, so a clipped payload is read back into one.
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

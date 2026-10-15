package app

import (
	"context"
	"encoding/json"
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
	model := result.Model
	if model == "" {
		model = p.cfg.Model
	}
	request := storage.LLMRequest{
		BankID:     attribution.BankID,
		Operation:  attribution.Operation,
		Scope:      req.Scope,
		Provider:   p.Name(),
		Model:      model,
		Status:     status,
		StartedAt:  start,
		EndedAt:    &ended,
		DurationMS: &durationMS,
		Input:      traceInput(req, p.cfg.TraceMaxChars),
		Output:     traceOutput(result, p.cfg.TraceMaxChars),
		Error:      errText,
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

// traceInput renders the request's prompt for the trace, truncated to the
// configured character budget.
func traceInput(req provider.CompletionRequest, maxChars int) any {
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, map[string]any{
			"role":    string(message.Role),
			"content": message.Content,
		})
	}
	payload := map[string]any{
		"messages":   messages,
		"max_tokens": req.MaxTokens,
		"scope":      req.Scope,
	}
	return truncateTrace(payload, maxChars)
}

// traceOutput renders the response for the trace, truncated to the configured
// character budget.
func traceOutput(result provider.CompletionResult, maxChars int) any {
	toolCalls := make([]map[string]any, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		toolCalls = append(toolCalls, map[string]any{
			"id":        call.ID,
			"name":      call.Name,
			"arguments": rawJSON(call.Arguments),
		})
	}
	payload := map[string]any{
		"content":       result.Content,
		"finish_reason": result.FinishReason,
		"tool_calls":    toolCalls,
	}
	return truncateTrace(payload, maxChars)
}

// rawJSON returns the argument payload as a decoded value so the trace stores
// it as JSON rather than a quoted string.
func rawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}

// truncateTrace serialises a trace payload and clips it to the character
// budget. A budget of zero or less disables truncation.
func truncateTrace(payload map[string]any, maxChars int) any {
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	if maxChars > 0 && len(raw) > maxChars {
		clipped := make([]byte, 0, maxChars+3)
		clipped = append(clipped, raw[:maxChars]...)
		clipped = append(clipped, '.', '.', '.')
		return json.RawMessage(clipped)
	}
	return json.RawMessage(raw)
}

package app

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/oteltrace"
	"github.com/studyzy/wenyuan/internal/provider"
)

// GenAI semantic-convention attribute keys, so a trace consumer that speaks
// the convention can read the calls without a mapping.
const (
	attrGenAISystem         = "gen_ai.system"
	attrGenAIRequestMaxTok  = "gen_ai.request.max_tokens"
	attrGenAIRequestTemp    = "gen_ai.request.temperature"
	attrGenAIResponseModel  = "gen_ai.response.model"
	attrGenAIInputTokens    = "gen_ai.usage.input_tokens"
	attrGenAIOutputTokens   = "gen_ai.usage.output_tokens"
	attrGenAIOperationName  = "gen_ai.operation.name"
	attrSpanScope           = "wenyuan.scope"
	attrSpanAttempts        = "wenyuan.attempts"
	spanNameChatCompletions = "chat"
)

// otelProvider emits one span per model call carrying the GenAI semantic
// conventions. Emission is best-effort and off the latency path: the tracer
// queues the span and a background worker exports it, so a tracing outage
// cannot fail or slow a completion.
type otelProvider struct {
	provider.Provider
	tracer *oteltrace.Tracer
}

// TraceSpans wraps a provider so its completions emit spans. When tracing is
// disabled, is nil, or the provider is nil, the provider is returned unchanged.
func TraceSpans(p provider.Provider, tracer *oteltrace.Tracer) provider.Provider {
	if p == nil || tracer == nil {
		return p
	}
	return &otelProvider{Provider: p, tracer: tracer}
}

// RerankCapability lets ProviderReranker see through the wrapper, so tracing a
// provider never advertises a reranker the inner provider does not have.
func (p *otelProvider) RerankCapability() provider.Reranker {
	return provider.ProviderReranker(p.Provider)
}

// Complete runs the completion and exports a span describing it.
func (p *otelProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	start := time.Now()
	span := oteltrace.Span{
		TraceID: oteltrace.NewTraceID(),
		SpanID:  oteltrace.NewSpanID(),
		Name:    spanNameChatCompletions,
		Start:   start,
		Attributes: map[string]any{
			attrGenAIOperationName: "chat",
			attrGenAISystem:        p.Name(),
		},
	}
	if req.MaxTokens > 0 {
		span.Attributes[attrGenAIRequestMaxTok] = req.MaxTokens
	}
	if req.Temperature != nil {
		span.Attributes[attrGenAIRequestTemp] = *req.Temperature
	}
	if req.Scope != "" {
		span.Attributes[attrSpanScope] = req.Scope
	}
	result, err := p.Provider.Complete(ctx, req)
	span.End = time.Now()
	if err != nil {
		span.StatusCode = oteltrace.StatusError
		span.StatusMessage = err.Error()
	} else {
		span.StatusCode = oteltrace.StatusOK
		if result.Model != "" {
			span.Attributes[attrGenAIResponseModel] = result.Model
		}
		if result.Usage.InputTokens > 0 {
			span.Attributes[attrGenAIInputTokens] = result.Usage.InputTokens
		}
		if result.Usage.OutputTokens > 0 {
			span.Attributes[attrGenAIOutputTokens] = result.Usage.OutputTokens
		}
	}
	p.tracer.Emit(span)
	return result, err
}

// NewTracer builds the span tracer from configuration, or returns nil when
// tracing is disabled, so the caller can wire a nil-safe wrapper.
func NewTracer(cfg config.TracingConfig) *oteltrace.Tracer {
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil
	}
	return oteltrace.New(oteltrace.Config{
		Endpoint:    cfg.Endpoint,
		Headers:     cfg.Headers,
		ServiceName: cfg.ServiceName,
		Environment: cfg.Environment,
	})
}

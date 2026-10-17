package provider

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/monitoring"
)

// metricsProvider records every completion at the provider boundary, so the LLM
// families report the calls that actually ran: duration, call count and the
// token classes the provider billed.
//
// The reference times its LLM calls at the same boundary, so a scrape of this
// service carries the provider, model, scope and success dimensions with the
// same values a reference deployment would.
type metricsProvider struct {
	Provider
	// model is the fallback model label for an adapter whose response does not
	// name the model it used.
	model string
}

// MetricsProvider wraps a provider so each completion is recorded in the
// process's metric registry. A nil provider is returned unchanged, so an
// unwired deployment records nothing.
func MetricsProvider(p Provider, fallbackModel string) Provider {
	if p == nil {
		return p
	}
	return &metricsProvider{Provider: p, model: fallbackModel}
}

// RerankCapability lets ProviderReranker see through the wrapper, so the
// metrics wrapper never advertises a reranker the inner provider does not have.
func (p *metricsProvider) RerankCapability() Reranker {
	return ProviderReranker(p.Provider)
}

// Complete runs the completion and records its duration, call and tokens.
func (p *metricsProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	start := time.Now()
	result, err := p.Provider.Complete(ctx, req)
	seconds := time.Since(start).Seconds()
	model := result.Model
	if model == "" {
		model = p.model
	}
	usage := result.Usage
	monitoring.Workload.ObserveLLMCall(
		p.Name(), model, req.Scope, err == nil, seconds,
		usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningTokens,
	)
	return result, err
}

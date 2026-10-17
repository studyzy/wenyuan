package provider_test

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/provider"
)

// usageProvider is a provider stub that reports a usage the metrics wrapper can
// record.
type usageProvider struct{}

func (usageProvider) Name() string                           { return "stub" }
func (usageProvider) Dimensions() int                        { return 1 }
func (usageProvider) VerifyConnection(context.Context) error { return nil }
func (usageProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (usageProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{
		Content: "ok",
		Model:   "stub-model",
		Usage:   provider.Usage{InputTokens: 120, OutputTokens: 7, CachedInputTokens: 3, ReasoningTokens: 2},
	}, nil
}

// The metrics wrapper records one LLM call per completion, carrying the
// provider, model, scope and success labels plus each token class with its
// bucket, and emits nothing before the first call.
func TestMetricsProviderRecordsCall(t *testing.T) {
	for _, sample := range monitoring.Workload.Samples() {
		if sample.Name == monitoring.LLMCallsName {
			t.Fatalf("LLM family present before the first call: %v", sample)
		}
	}

	wrapped := provider.MetricsProvider(usageProvider{}, "fallback-model")
	if _, err := wrapped.Complete(context.Background(), provider.CompletionRequest{Scope: "reflect"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	labels := map[string]string{
		"provider": "stub", "model": "stub-model", "scope": "reflect", "success": "true",
	}
	var calls monitoring.Metric
	found := false
	buckets := map[string]string{}
	for _, sample := range monitoring.Workload.Samples() {
		switch sample.Name {
		case monitoring.LLMCallsName:
			calls = sample
			found = true
		case monitoring.LLMTokensInputName:
			buckets["input"] = sample.Labels["token_bucket"]
		case monitoring.LLMTokensOutputName:
			buckets["output"] = sample.Labels["token_bucket"]
		case monitoring.LLMTokensCachedName:
			buckets["cached"] = sample.Labels["token_bucket"]
		case monitoring.LLMTokensThoughtsName:
			buckets["thoughts"] = sample.Labels["token_bucket"]
		}
	}
	if !found {
		t.Fatal("LLM call counter not recorded")
	}
	for name, want := range labels {
		if calls.Labels[name] != want {
			t.Fatalf("call label %s = %q, want %q (labels %v)", name, calls.Labels[name], want, calls.Labels)
		}
	}
	if len(calls.Labels) != len(labels) {
		t.Fatalf("call labels = %v, want %v", calls.Labels, labels)
	}
	if buckets["input"] != "100-500" || buckets["output"] != "0-100" ||
		buckets["cached"] != "0-100" || buckets["thoughts"] != "0-100" {
		t.Fatalf("token buckets = %v", buckets)
	}
}

// A nil provider is returned unchanged, so an unwired deployment records
// nothing.
func TestMetricsProviderNil(t *testing.T) {
	if provider.MetricsProvider(nil, "m") != nil {
		t.Fatal("nil provider must stay nil")
	}
}

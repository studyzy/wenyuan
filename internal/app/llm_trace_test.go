package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// traceStubProvider is a provider whose single completion is scripted, so the
// tracing wrapper can be exercised without a live endpoint.
type traceStubProvider struct {
	name   string
	result provider.CompletionResult
	err    error
}

func (s traceStubProvider) Name() string { return s.name }

func (s traceStubProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	return s.result, s.err
}

func (s traceStubProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}

func (s traceStubProvider) VerifyConnection(context.Context) error { return nil }

func (s traceStubProvider) Dimensions() int { return 0 }

// A completion run through the tracing wrapper lands in the model-request trace
// with its attribution and token accounting.
func TestTraceProviderRecordsCompletion(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubProvider{
		name: "openai",
		result: provider.CompletionResult{
			Content: "hello",
			Model:   "gpt-x",
			Usage:   provider.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
		},
	}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096, Model: "gpt-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "reflect"})
	if _, err := traced.Complete(ctx, provider.CompletionRequest{Scope: "reflect"}); err != nil {
		t.Fatal(err)
	}
	// A limit of zero is an empty page, the reference's own semantics for the
	// endpoint's `limit`, so a caller that wants rows asks for them.
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 {
		t.Fatalf("trace rows = %d, want 1", len(page.Requests))
	}
	row := page.Requests[0]
	if row.Provider != "openai" || row.Status != "success" || row.Operation != "reflect" || row.Scope != "reflect" {
		t.Fatalf("unexpected trace row: %+v", row)
	}
	if row.InputTokens == nil || *row.InputTokens != 3 || row.OutputTokens == nil || *row.OutputTokens != 5 {
		t.Fatalf("token accounting not recorded: %+v", row)
	}
}

// With tracing disabled the wrapper is a pass-through, so the default
// deployment writes no trace rows.
func TestTraceProviderDisabledIsPassThrough(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubProvider{name: "openai"}
	if _, ok := TraceProvider(inner, store, config.ProviderConfig{}).(*tracingProvider); ok {
		t.Fatal("disabled tracing must not wrap the provider")
	}
}

// A scope outside the configured filter is not recorded.
func TestTraceProviderScopeFilter(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubProvider{name: "openai"}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceScopes: []string{"retain"}})
	ctx := context.Background()
	if _, err := traced.Complete(ctx, provider.CompletionRequest{Scope: "reflect"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("filtered scope recorded %d rows", page.Total)
	}
}

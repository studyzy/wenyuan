package app

import (
	"context"
	"encoding/json"
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

// A traced call records the OTel-style hierarchy the reference writes
// (engine/llm_trace.py:530-536): a trace id and operation span shared by every
// call of one operation run, and a fresh span per call pointing at the
// operation span as its parent. Recording nulls there — which this wrapper used
// to do — leaves the trace listing unable to group a run's calls.
func TestTraceRecordsOperationScopedSpanHierarchy(t *testing.T) {
	traceScopeMu.Lock()
	traceScopes = make(map[traceScopeKey]traceScopeIDs)
	traceScopeMu.Unlock()

	store := storage.NewMemoryStore("public")
	inner := traceStubProvider{name: "openai", result: provider.CompletionResult{Content: "ok", Model: "gpt-x"}}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096, Model: "gpt-x"})
	retain := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	for i := 0; i < 2; i++ {
		if _, err := traced.Complete(retain, provider.CompletionRequest{Scope: "retain_extract_facts"}); err != nil {
			t.Fatal(err)
		}
	}
	// A different operation run is a different trace.
	reflect := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "reflect"})
	if _, err := traced.Complete(reflect, provider.CompletionRequest{Scope: "reflect"}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 3 {
		t.Fatalf("rows = %d, want 3", len(page.Requests))
	}
	seenSpans := make(map[string]string, len(page.Requests))
	var retainTrace, reflectTrace string
	for _, row := range page.Requests {
		if row.TraceID == "" || row.SpanID == "" || row.ParentSpanID == "" {
			t.Fatalf("row %s recorded trace/span/parent = %q/%q/%q, want all three",
				row.Scope, row.TraceID, row.SpanID, row.ParentSpanID)
		}
		if previous, reused := seenSpans[row.SpanID]; reused {
			t.Fatalf("span id %q reused by %s and %s", row.SpanID, previous, row.Scope)
		}
		seenSpans[row.SpanID] = row.Scope
		if row.Scope == "reflect" {
			reflectTrace = row.TraceID
			continue
		}
		if retainTrace != "" && retainTrace != row.TraceID {
			t.Fatalf("two calls of one retain run disagree on trace id: %q vs %q", retainTrace, row.TraceID)
		}
		retainTrace = row.TraceID
	}
	if retainTrace == reflectTrace {
		t.Fatalf("two operation runs share trace id %q", retainTrace)
	}
}

// The trace columns carry the shape the reference stores: `input` is the
// message list, `output` is the completion's content (nil on a failed call),
// and `llm_info` holds the caller's requested parameters rather than the prompt
// (engine/llm_trace.py:530-536, :616-619). Wrapping them in this service's own
// envelope objects made every row a different JSON shape from the reference's.
func TestTraceColumnsMatchTheReferenceShape(t *testing.T) {
	traceScopeMu.Lock()
	traceScopes = make(map[traceScopeKey]traceScopeIDs)
	traceScopeMu.Unlock()

	store := storage.NewMemoryStore("public")
	temperature := 0.1
	inner := traceStubProvider{name: "openai", result: provider.CompletionResult{
		Content: "the answer", Model: "gpt-x", FinishReason: "stop",
	}}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 100000, Model: "gpt-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	if _, err := traced.Complete(ctx, provider.CompletionRequest{
		Scope:       "retain_extract_facts",
		MaxTokens:   64000,
		Temperature: &temperature,
		Messages: []provider.Message{
			{Role: "system", Content: "extract"},
			{Role: "user", Content: "text"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 {
		t.Fatalf("rows = %d, want 1", len(page.Requests))
	}
	row := page.Requests[0]

	// input: the message list itself — a JSON array, not an object wrapper.
	rawInput, err := json.Marshal(row.Input)
	if err != nil {
		t.Fatal(err)
	}
	if rawInput[0] != '[' {
		t.Fatalf("input = %s, want a JSON array (the message list)", rawInput)
	}
	var messages []map[string]any
	if err := json.Unmarshal(rawInput, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0]["role"] != "system" || messages[1]["content"] != "text" {
		t.Fatalf("input messages = %+v", messages)
	}

	// output: the completion content as a plain JSON string.
	rawOutput, err := json.Marshal(row.Output)
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(rawOutput, &content); err != nil || content != "the answer" {
		t.Fatalf("output = %s, want the content string", rawOutput)
	}

	// llm_info: the requested parameters, keyed the way the reference keys them.
	if row.LLMInfo["request"] == nil {
		t.Fatalf("llm_info = %+v, want the requested parameters", row.LLMInfo)
	}
	request, ok := row.LLMInfo["request"].(map[string]any)
	if !ok {
		t.Fatalf("llm_info.request = %#v", row.LLMInfo["request"])
	}
	if request["temperature"] != 0.1 {
		t.Fatalf("llm_info.request.temperature = %#v, want 0.1", request["temperature"])
	}
	// The column round-trips through JSON, so a number reads back as float64.
	if tokens, ok := request["max_completion_tokens"].(float64); !ok || tokens != 64000 {
		t.Fatalf("llm_info.request.max_completion_tokens = %#v, want 64000", request["max_completion_tokens"])
	}
	if row.LLMInfo["finish_reason"] != "stop" {
		t.Fatalf("llm_info.finish_reason = %#v, want stop", row.LLMInfo["finish_reason"])
	}
}

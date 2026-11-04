package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// flushTraceRows writes the recorder's buffer to the store, which is what a test
// has to do before reading rows back: the recorder batches them the way the
// reference does (engine/llm_trace.py:640-660), so a row is not in the table the
// instant the call returns.
func flushTraceRows(store storage.Store) { traceRecorder(store).flush() }

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

// traceStubEmbedder is an embedding backend whose result is scripted, so the
// tracing wrapper can be exercised without a live endpoint.
type traceStubEmbedder struct {
	name    string
	model   string
	vectors [][]float32
	err     error
}

func (s traceStubEmbedder) Name() string  { return s.name }
func (s traceStubEmbedder) Model() string { return s.model }

func (s traceStubEmbedder) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{Vectors: s.vectors, Dimensions: len(s.vectors)}, s.err
}

func (s traceStubEmbedder) Dimensions() int { return 0 }

// traceStubReranker is a cross-encoder whose scores are scripted, so the
// tracing wrapper can be exercised without a live endpoint.
type traceStubReranker struct {
	scores []float64
	err    error
}

func (s traceStubReranker) Rerank(context.Context, provider.RerankRequest) (provider.RerankResult, error) {
	return provider.RerankResult{Scores: s.scores}, s.err
}

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
	flushTraceRows(store)
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
	flushTraceRows(store)
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

	flushTraceRows(store)
	flushTraceRows(store)
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

	flushTraceRows(store)
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

// The scope filters match with the reference's trailing-`*` prefix rule, and an
// excluded scope wins over the allow list (engine/llm_trace.py:605-612,
// :710-713). An empty allow list records everything and an empty exclude list
// excludes nothing.
func TestTraceScopeWildcardAndExclude(t *testing.T) {
	tests := []struct {
		name     string
		scopes   []string
		excluded []string
		scope    string
		want     bool
	}{
		{name: "empty allow and exclude record everything", scope: "rerank", want: true},
		{name: "exact allow", scopes: []string{"retain"}, scope: "retain", want: true},
		{name: "exact allow misses other scopes", scopes: []string{"retain"}, scope: "reflect", want: false},
		{name: "prefix allow matches its prefix", scopes: []string{"embedding_*"}, scope: "embedding_document", want: true},
		{name: "prefix allow misses other scopes", scopes: []string{"embedding_*"}, scope: "rerank", want: false},
		{name: "bare star allows everything", scopes: []string{"*"}, scope: "anything", want: true},
		{name: "exclude without allow drops the scope", excluded: []string{"rerank"}, scope: "rerank", want: false},
		{name: "exclude prefix drops its prefix", excluded: []string{"embedding_*"}, scope: "embedding_query", want: false},
		{name: "exclude misses other scopes", excluded: []string{"rerank"}, scope: "embedding_document", want: true},
		{name: "exclude wins over the allow list", scopes: []string{"embedding_*"}, excluded: []string{"embedding_query"}, scope: "embedding_query", want: false},
		{name: "allow survives an excluded sibling", scopes: []string{"embedding_*"}, excluded: []string{"embedding_query"}, scope: "embedding_document", want: true},
		{name: "exclude prefix beats allow prefix", scopes: []string{"embedding_*"}, excluded: []string{"embedding_*"}, scope: "embedding_document", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.ProviderConfig{TraceScopes: tt.scopes, TraceExcludeScopes: tt.excluded}
			if got := traceScopeRecorded(cfg, tt.scope); got != tt.want {
				t.Fatalf("traceScopeRecorded(%q) with allow %v exclude %v = %v, want %v",
					tt.scope, tt.scopes, tt.excluded, got, tt.want)
			}
		})
	}
}

// An embedding run through the tracing wrapper lands in the model-request trace
// with the row shape the reference writes for its embedding backends
// (engine/retain/embedding_utils.py:139 through trace_model_call): the scope
// names the input kind, the input is the text list, the output is absent — an
// embedding's vectors would only be noise — and llm_info carries the input
// count.
func TestTraceEmbedderRecordsEmbeddingRows(t *testing.T) {
	tests := []struct {
		kind  provider.EmbeddingKind
		scope string
	}{
		{provider.EmbeddingPassage, "embedding_document"},
		{provider.EmbeddingQuery, "embedding_query"},
	}
	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			inner := traceStubEmbedder{name: "openai", model: "text-embed", vectors: [][]float32{{1, 2}}}
			traced := TraceEmbedder(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096})
			ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
			result, err := traced.Embed(ctx, provider.EmbeddingRequest{Texts: []string{"one", "two"}, Kind: tt.kind})
			if err != nil || len(result.Vectors) != 1 {
				t.Fatalf("Embed = %+v, %v", result, err)
			}
			flushTraceRows(store)
			page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Requests) != 1 {
				t.Fatalf("trace rows = %d, want 1", len(page.Requests))
			}
			row := page.Requests[0]
			if row.Scope != tt.scope || row.Provider != "openai" || row.Model != "text-embed" ||
				row.Status != "success" || row.Operation != "retain" {
				t.Fatalf("unexpected embedding trace row: %+v", row)
			}
			rawInput, err := json.Marshal(row.Input)
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal(rawInput, &input); err != nil {
				t.Fatal(err)
			}
			texts, ok := input["texts"].([]any)
			if !ok || len(texts) != 2 || texts[0] != "one" || texts[1] != "two" {
				t.Fatalf("input = %s, want {\"texts\": [\"one\", \"two\"]}", rawInput)
			}
			if row.Output != nil {
				t.Fatalf("output = %#v, want none for an embedding row", row.Output)
			}
			if count, ok := row.LLMInfo["input_count"].(float64); !ok || count != 2 {
				t.Fatalf("llm_info.input_count = %#v, want 2", row.LLMInfo["input_count"])
			}
			if row.InputTokens != nil || row.OutputTokens != nil || row.TotalTokens != nil {
				t.Fatalf("token columns = %v/%v/%v, want none: no usage is reported",
					row.InputTokens, row.OutputTokens, row.TotalTokens)
			}
		})
	}
}

// A failed embedding call records the error row the reference writes for a
// failed backend call: status error, the error text, and no output.
func TestTraceEmbedderRecordsErrorRow(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubEmbedder{name: "openai", model: "text-embed", err: errors.New("backend down")}
	traced := TraceEmbedder(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	if _, err := traced.Embed(ctx, provider.EmbeddingRequest{Texts: []string{"one"}, Kind: provider.EmbeddingPassage}); err == nil {
		t.Fatal("the stub error must reach the caller")
	}
	flushTraceRows(store)
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 {
		t.Fatalf("trace rows = %d, want 1", len(page.Requests))
	}
	row := page.Requests[0]
	if row.Status != "error" || row.Error != "backend down" || row.Output != nil {
		t.Fatalf("error row = %+v", row)
	}
}

// A rerank run through the tracing wrapper lands in the model-request trace
// with the row shape the reference writes for its cross-encoder backends
// (engine/cross_encoder.py:220 through trace_model_call): the input is the
// deduplicated queries and the candidate documents, the output the scores, and
// llm_info carries the input count.
func TestTraceRerankerRecordsRerankRow(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubReranker{scores: []float64{0.9, 0.25}}
	traced := TraceReranker(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096},
		traceBackend{provider: "cohere", model: "rerank-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	result, err := traced.Rerank(ctx, provider.RerankRequest{Query: "q", Documents: []string{"d1", "d2"}})
	if err != nil || len(result.Scores) != 2 {
		t.Fatalf("Rerank = %+v, %v", result, err)
	}
	flushTraceRows(store)
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 {
		t.Fatalf("trace rows = %d, want 1", len(page.Requests))
	}
	row := page.Requests[0]
	if row.Scope != "rerank" || row.Provider != "cohere" || row.Model != "rerank-x" ||
		row.Status != "success" || row.Operation != "retain" {
		t.Fatalf("unexpected rerank trace row: %+v", row)
	}
	rawInput, err := json.Marshal(row.Input)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(rawInput, &input); err != nil {
		t.Fatal(err)
	}
	queries, ok := input["queries"].([]any)
	if !ok || len(queries) != 1 || queries[0] != "q" {
		t.Fatalf("input.queries = %#v, want [\"q\"]", input["queries"])
	}
	documents, ok := input["documents"].([]any)
	if !ok || len(documents) != 2 || documents[0] != "d1" || documents[1] != "d2" {
		t.Fatalf("input.documents = %#v, want [\"d1\", \"d2\"]", input["documents"])
	}
	rawOutput, err := json.Marshal(row.Output)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(rawOutput, &output); err != nil {
		t.Fatal(err)
	}
	scores, ok := output["scores"].([]any)
	if !ok || len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.25 {
		t.Fatalf("output = %s, want {\"scores\": [0.9, 0.25]}", rawOutput)
	}
	if count, ok := row.LLMInfo["input_count"].(float64); !ok || count != 2 {
		t.Fatalf("llm_info.input_count = %#v, want 2", row.LLMInfo["input_count"])
	}
}

// A failed rerank call records the error row the reference writes for a failed
// backend call: status error and no output.
func TestTraceRerankerRecordsErrorRow(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubReranker{err: errors.New("reranker down")}
	traced := TraceReranker(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096},
		traceBackend{provider: "cohere", model: "rerank-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	if _, err := traced.Rerank(ctx, provider.RerankRequest{Query: "q", Documents: []string{"d1"}}); err == nil {
		t.Fatal("the stub error must reach the caller")
	}
	flushTraceRows(store)
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 {
		t.Fatalf("trace rows = %d, want 1", len(page.Requests))
	}
	row := page.Requests[0]
	if row.Status != "error" || row.Error != "reranker down" || row.Output != nil {
		t.Fatalf("error row = %+v", row)
	}
}

// Disabled tracing leaves the embedding backend and the reranker unwrapped, and
// a nil backend stays nil so an unconfigured deployment keeps yielding no
// embedding boundary at all.
func TestTraceEmbedderAndRerankerDisabledArePassThrough(t *testing.T) {
	if _, wrapped := TraceEmbedder(traceStubEmbedder{}, nil, config.ProviderConfig{}).(*tracingEmbedder); wrapped {
		t.Fatal("disabled tracing must not wrap the embedding backend")
	}
	if _, wrapped := TraceReranker(traceStubReranker{}, nil, config.ProviderConfig{}, traceBackend{}).(*tracingReranker); wrapped {
		t.Fatal("disabled tracing must not wrap the reranker")
	}
	if got := TraceEmbedder(nil, storage.NewMemoryStore("public"), config.ProviderConfig{TraceEnabled: true}); got != nil {
		t.Fatalf("nil embedding backend wrapped as %#v", got)
	}
	if got := TraceReranker(nil, storage.NewMemoryStore("public"), config.ProviderConfig{TraceEnabled: true}, traceBackend{}); got != nil {
		t.Fatalf("nil reranker wrapped as %#v", got)
	}
}

// An excluded scope still runs the call — only the recording is skipped.
func TestTraceEmbedderExcludedScopeStillRuns(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubEmbedder{name: "openai", model: "text-embed", vectors: [][]float32{{1, 2}}}
	traced := TraceEmbedder(inner, store, config.ProviderConfig{
		TraceEnabled:       true,
		TraceExcludeScopes: []string{"embedding_*"},
	})
	ctx := context.Background()
	result, err := traced.Embed(ctx, provider.EmbeddingRequest{Texts: []string{"one"}, Kind: provider.EmbeddingPassage})
	if err != nil || len(result.Vectors) != 1 {
		t.Fatalf("Embed = %+v, %v", result, err)
	}
	flushTraceRows(store)
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("excluded scope recorded %d rows", page.Total)
	}
}

// The rows the embedding and rerank wrappers write are what the /llm-requests
// call_type filters select: the storage layer derives the call kind from the
// scope alone, so an embedding row lands in the embedding filter and the scope
// filter, a rerank row in the rerank filter, and none of them in the llm one
// (engine/llm_trace.py:565-602). The stats histogram counts them the same way.
func TestTraceCallTypeFiltersSeeEmbeddingAndRerankRows(t *testing.T) {
	store := storage.NewMemoryStore("public")
	cfg := config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 4096}
	chat := TraceProvider(traceStubProvider{name: "openai", result: provider.CompletionResult{Content: "ok", Model: "gpt-x"}}, store, cfg)
	embedder := TraceEmbedder(traceStubEmbedder{name: "openai", model: "text-embed", vectors: [][]float32{{1}}}, store, cfg)
	reranker := TraceReranker(traceStubReranker{scores: []float64{0.5}}, store, cfg, traceBackend{provider: "cohere", model: "rerank-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	if _, err := chat.Complete(ctx, provider.CompletionRequest{Scope: "retain_extract_facts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(ctx, provider.EmbeddingRequest{Texts: []string{"one"}, Kind: provider.EmbeddingPassage}); err != nil {
		t.Fatal(err)
	}
	if _, err := reranker.Rerank(ctx, provider.RerankRequest{Query: "q", Documents: []string{"d1"}}); err != nil {
		t.Fatal(err)
	}
	flushTraceRows(store)

	tests := []struct {
		callType string
		want     int
	}{
		{"llm", 1},
		{"embedding", 1},
		{"rerank", 1},
		{"decision", 0},
	}
	for _, tt := range tests {
		t.Run("call_type="+tt.callType, func(t *testing.T) {
			page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", CallType: tt.callType, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != tt.want {
				t.Fatalf("call_type=%s rows = %d, want %d", tt.callType, page.Total, tt.want)
			}
			stats, err := store.LLMRequestStats(ctx, "b1", "1d", "", tt.callType)
			if err != nil {
				t.Fatal(err)
			}
			counted := 0
			for _, bucket := range stats.Bucket {
				counted += bucket.Total
			}
			if counted != tt.want {
				t.Fatalf("call_type=%s stats total = %d, want %d", tt.callType, counted, tt.want)
			}
		})
	}
	// The scope filter names an embedding row's scope exactly as the reference's
	// `scope=embedding_document` does.
	flushTraceRows(store)
	page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "b1", Scope: "embedding_document", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Requests[0].Scope != "embedding_document" {
		t.Fatalf("scope=embedding_document rows = %d, want 1", page.Total)
	}
}

// A trace row reaches the table through the buffer rather than the call's own
// path, and a shutdown flushes what is still buffered.
//
// The reference buffers rows and writes them in batches so a recall's embedding,
// rerank and decision calls do not each take a pooled connection
// (engine/llm_trace.py:640-660); the cost is that rows still buffered when the
// process stops are lost, which is why it flushes on shutdown (:865-875).
func TestTraceRowsAreBufferedAndFlushedOnClose(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	previous := traceFlushInterval
	traceFlushInterval = time.Hour // nothing flushes on the timer inside a test
	defer func() { traceFlushInterval = previous }()
	closeTraceRecorder(store) // start from an empty recorder
	defer closeTraceRecorder(store)

	cfg := config.ProviderConfig{TraceEnabled: true}
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "reflect"})
	emitTraceRow(store, cfg, ctx, tracedCall{
		scope:   "reflect",
		backend: traceBackend{provider: "openai", model: "m"},
		start:   time.Now().UTC(),
	})

	before, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Requests) != 0 {
		t.Fatalf("rows = %d before any flush, want the buffer to hold them", len(before.Requests))
	}

	closeTraceRecorder(store)
	after, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Requests) != 1 {
		t.Fatalf("rows = %d after the shutdown flush, want the buffered one written", len(after.Requests))
	}
	if after.Requests[0].Scope != "reflect" || after.Requests[0].Status != "success" {
		t.Fatalf("row = %+v", after.Requests[0])
	}
}

// A full buffer writes without waiting for the timer.
func TestTraceBatchWritesWhenFull(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	previousInterval, previousRows := traceFlushInterval, traceFlushMaxRows
	traceFlushInterval = time.Hour
	traceFlushMaxRows = 2
	defer func() {
		traceFlushInterval, traceFlushMaxRows = previousInterval, previousRows
		closeTraceRecorder(store)
	}()
	closeTraceRecorder(store)

	cfg := config.ProviderConfig{TraceEnabled: true}
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "reflect"})
	for i := 0; i < 2; i++ {
		emitTraceRow(store, cfg, ctx, tracedCall{
			scope: "reflect", backend: traceBackend{provider: "openai"}, start: time.Now().UTC(),
		})
	}
	// The write runs on its own goroutine; wait for it rather than assume it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Requests) == 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rows = %d, want the full buffer written without waiting for the timer", len(page.Requests))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A retain's trace rows are mapped onto the memories it produced, which is what
// makes `GET .../llm-requests?memory_id=` find the calls that made a memory
// (engine/memory_engine.py:8025-8029, engine/llm_trace.py:931-990).
//
// Reverting the patch leaves every row's metadata without `memory_ids`, so the
// memory_id filter matches nothing.
func TestRetainTraceRowsCarryTheMemoryIDsItCreated(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := factsProvider{result: provider.CompletionResult{
		Structured: map[string]any{"facts": []any{map[string]any{"text": "Alice works at Google."}}},
	}}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 100000, Model: "gpt-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	outcome, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID:     "b1",
		Items:      []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-a"}},
		Extraction: newFactExtractor(traced, ExtractionSettings{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	created := make([]string, 0)
	for _, group := range outcome.ItemUnitIDs {
		created = append(created, group...)
	}
	if len(created) == 0 {
		t.Fatal("the retain created no memory units to map")
	}
	// The worker's post-retain step, called directly: the patch is fire-and-forget
	// and flushes the trace before it runs.
	(&archiveProcessor{store: store}).attachRetainMemoryIDs(ctx, outcome)

	deadline := time.Now().Add(2 * time.Second)
	for {
		page, listErr := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{
			BankID: "b1", MemoryID: created[0], MemoryIDSet: true, Limit: 10,
		})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(page.Requests) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no trace row maps to the memory it created (%v)", created)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A trace that never wrote a row is not patched: the UPDATE would match nothing
// and only cost a connection (engine/llm_trace.py:670-690).
func TestAttachMemoryIDsSkipsATraceWithNoRows(t *testing.T) {
	store := storage.NewMemoryStore("public")
	closeTraceRecorder(store)
	defer closeTraceRecorder(store)
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	// No call was recorded under this context, so its trace has no rows.
	traceRecorder(store).attachMemoryIDs(ctx, []string{"unit-a"}, nil)
	page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("rows = %d, want the recorder to have written (and patched) nothing", page.Total)
	}
}

// A shutdown waits for the metadata patches still in flight, so the mapping a
// retain scheduled just before the process stopped is not lost with it — the
// reference drains them the same way (engine/llm_trace.py:865-875 over
// PendingWrites).
func TestCloseTraceRecorderDrainsPendingPatches(t *testing.T) {
	store := storage.NewMemoryStore("public")
	closeTraceRecorder(store)
	cfg := config.ProviderConfig{TraceEnabled: true}
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	emitTraceRow(store, cfg, ctx, tracedCall{
		scope: "retain_extract_facts", backend: traceBackend{provider: "openai"}, start: time.Now().UTC(),
	})
	traceRecorder(store).attachMemoryIDs(ctx, []string{"unit-a"}, nil)
	// The close flushes the row and waits for the patch that follows it.
	closeTraceRecorder(store)

	page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{
		BankID: "b1", MemoryID: "unit-a", MemoryIDSet: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) == 0 {
		t.Fatal("the patch scheduled before the shutdown never landed")
	}
}

// A consolidation run's trace rows are mapped onto the observations it actually
// wrote, so a second run's output is not attributed to the first.
//
// The pass reports the ids it wrote (internal/consolidation.Result.ObservationIDs,
// the reference's record_created_memory_ids), so the mapping is exact rather than
// "the newest N observations" — an approximation that would over-report on a bank
// two writers are feeding.
func TestConsolidationTraceRowsCarryTheExactObservationsWritten(t *testing.T) {
	store := storage.NewMemoryStore("public")
	closeTraceRecorder(store)
	defer closeTraceRecorder(store)
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "consolidation"})
	emitTraceRow(store, config.ProviderConfig{TraceEnabled: true}, ctx, tracedCall{
		scope: "consolidate", backend: traceBackend{provider: "openai"}, start: time.Now().UTC(),
	})
	// The pass wrote two observations; a third, unrelated one already exists in
	// the bank under a different writer. The deprecated scan-based mapping would
	// have swept it in.
	store.SeedUnit(storage.MemoryUnit{
		ID: "unrelated-obs", BankID: "b1", FactType: storage.TypeObservation, Text: "someone else's",
	})
	traceRecorder(store).attachMemoryIDs(ctx, []string{"obs-new", "o1"}, nil)
	closeTraceRecorder(store)

	page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{
		BankID: "b1", MemoryID: "unrelated-obs", MemoryIDSet: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 0 {
		t.Fatal("a memory this run did not write was attributed to its trace")
	}
	for _, id := range []string{"obs-new", "o1"} {
		found, listErr := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{
			BankID: "b1", MemoryID: id, MemoryIDSet: true, Limit: 10,
		})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(found.Requests) == 0 {
			t.Fatalf("observation %q this run wrote is not on its trace", id)
		}
	}
}

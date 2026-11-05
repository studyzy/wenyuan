package reflect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// splitProvider answers the two shapes of a split synthesis: an extraction
// (map) call for each chunk and one reduce call. It is safe under the parallel
// map calls, keying its reply off the system prompt rather than a call counter.
type splitProvider struct {
	mu       sync.Mutex
	mapCalls int
	requests []provider.CompletionRequest
}

func (p *splitProvider) Name() string { return "split" }

func (p *splitProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Content, "You are an evidence-extraction pass") {
		p.mapCalls++
		return provider.CompletionResult{
			Content: fmt.Sprintf("- claim %d (mentioned_at: 2024-01-0%d; memory_ids: m%d)", p.mapCalls, p.mapCalls, p.mapCalls),
			Usage:   provider.Usage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10},
		}, nil
	}
	return provider.CompletionResult{Content: "the reduced answer", Usage: provider.Usage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10}}, nil
}

func (p *splitProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (p *splitProvider) VerifyConnection(context.Context) error { return nil }
func (p *splitProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// TestSplitSynthesisMapsChunksThenReduces: evidence larger than one prompt is
// split, each chunk compressed into claims under its own final_map_<n> scope,
// and one final call reduces every claim set into the answer.
func TestSplitSynthesisMapsChunksThenReduces(t *testing.T) {
	p := &splitProvider{}
	loop := NewLoop(p, &stubRetrieval{}, Config{Question: "what happened?", ContextBudget: 4000})
	for i := 0; i < 3; i++ {
		loop.evidence = append(loop.evidence, Evidence{
			Kind: "memory", ID: fmt.Sprintf("m-%d", i),
			Text: strings.Repeat("event and detail ", 470),
		})
	}
	outcome := Outcome{}
	answer, err := loop.compose(context.Background(), &outcome)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "the reduced answer" {
		t.Fatalf("answer = %q, want the reduce call's answer", answer)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mapCalls < 2 {
		t.Fatalf("map calls = %d, want at least two evidence chunks", p.mapCalls)
	}
	mapScopes := 0
	sawFinal := false
	for _, call := range loop.llmCalls {
		if strings.HasPrefix(call.Scope, "final_map_") {
			mapScopes++
			continue
		}
		if call.Scope == "final" {
			sawFinal = true
		}
	}
	if mapScopes < 2 || !sawFinal {
		t.Fatalf("trace = %+v, want map scopes then final", loop.llmCalls)
	}
	var reduce *provider.CompletionRequest
	for i := range p.requests {
		if len(p.requests[i].Messages) > 0 && strings.HasPrefix(p.requests[i].Messages[0].Content, "CRITICAL") {
			reduce = &p.requests[i]
		}
	}
	if reduce == nil || len(reduce.Messages) < 2 {
		t.Fatal("the reduce call never ran")
	}
	for _, section := range []string{"Evidence pass 1:", "Evidence pass 2:", "claim 1", "claim 2"} {
		if !strings.Contains(reduce.Messages[1].Content, section) {
			t.Fatalf("reduce prompt missing %q", section)
		}
	}
}

// TestSplitEvidenceChunksKeepsChronologicalOrder: the splitter partitions by
// budget and never drops a line.
func TestSplitEvidenceChunksKeepsChronologicalOrder(t *testing.T) {
	lines := []string{"alpha", "beta", "gamma", "delta"}
	chunks := splitEvidenceChunks(lines, 1)
	if len(chunks) != len(lines) {
		t.Fatalf("chunks = %d, want one per line at a tiny budget: %v", len(chunks), chunks)
	}
	var joined []string
	for _, chunk := range chunks {
		joined = append(joined, chunk...)
	}
	if strings.Join(joined, ",") != strings.Join(lines, ",") {
		t.Fatalf("evidence reordered or dropped: %v", joined)
	}
	if got := splitEvidenceChunks(nil, 0); got != nil {
		t.Fatalf("empty input should produce no chunks, got %v", got)
	}
}

// TestResolveToolArgCeiling pins the batch formula.
func TestResolveToolArgCeiling(t *testing.T) {
	cases := []struct {
		remaining, concurrent, want int
	}{
		{24000, 1, 16000},
		{8000, 1, 8000},
		{8000, 4, 2000},
		{1000, 4, 1000},
		{0, 1, 1000},
		{5000, 0, 5000},
	}
	for _, tc := range cases {
		if got := resolveToolArgCeiling(tc.remaining, tc.concurrent); got != tc.want {
			t.Fatalf("resolveToolArgCeiling(%d, %d) = %d, want %d", tc.remaining, tc.concurrent, got, tc.want)
		}
	}
}

// ceilingRetrieval records the token ceiling a tool call was clamped to.
type ceilingRetrieval struct {
	observationsTokens int
	memoriesTokens     int
}

func (r *ceilingRetrieval) ReflectMentalModels(context.Context, string, int) ([]Evidence, error) {
	return nil, nil
}
func (r *ceilingRetrieval) ReflectObservations(_ context.Context, _ string, maxTokens int) ([]Evidence, error) {
	r.observationsTokens = maxTokens
	return nil, nil
}
func (r *ceilingRetrieval) ReflectMemories(_ context.Context, _ string, maxTokens int) ([]Evidence, error) {
	r.memoriesTokens = maxTokens
	return nil, nil
}
func (r *ceilingRetrieval) ReflectReadModels(context.Context, []string) ([]Evidence, error) {
	return nil, nil
}
func (r *ceilingRetrieval) ReflectExpand(context.Context, []string, string) ([]Evidence, error) {
	return nil, nil
}
func (r *ceilingRetrieval) Directives(context.Context) []Directive { return nil }

// TestParallelToolCallsTightenTokenCeiling: a model asking for the maximum is
// clamped to the remaining budget (10000 - 2000 = 8000) rather than 16000.
func TestParallelToolCallsTightenTokenCeiling(t *testing.T) {
	p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolSearchObservations, Arguments: []byte(`{"query":"q","max_tokens":16000}`)}}},
	}}
	retrieval := &ceilingRetrieval{}
	loop := NewLoop(p, retrieval, Config{Question: "q", ContextBudget: 10000})
	loop.evidence = []Evidence{{Kind: "memory", ID: "m1", Text: strings.Repeat("x", 7996)}}
	if _, _, err := loop.step(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if retrieval.observationsTokens != 8000 {
		t.Fatalf("search_observations max_tokens = %d, want 8000", retrieval.observationsTokens)
	}
}

// TestOverBudgetConversationCompactsEarliestToolResults: an over-budget
// model-facing conversation blanks the earliest tool result, but the stored
// evidence and turn batches keep their full text.
func TestOverBudgetConversationCompactsEarliestToolResults(t *testing.T) {
	text := strings.Repeat("detail ", 220)
	loop := NewLoop(&scriptedProvider{name: "stub"}, &stubRetrieval{}, Config{Question: "q", ContextBudget: 300})
	loop.turns = []Turn{{
		Iteration: 1,
		Calls:     []ToolCall{{ID: "c1", Name: ToolSearchObservations, Input: map[string]any{"query": "q"}}},
		Batches:   [][]Evidence{{{Kind: "observation", ID: "o1", Text: text}}},
	}}
	loop.evidence = []Evidence{{Kind: "observation", ID: "o1", Text: text}}
	messages := loop.messages(context.Background())
	if !loop.compacted {
		t.Fatal("an over-budget conversation must be reported as compacted")
	}
	blanked := false
	for _, message := range messages {
		if message.Role == provider.RoleTool && message.Content == compactedToolResult {
			blanked = true
		}
	}
	if !blanked {
		t.Fatal("the earliest tool result was not replaced in the model-facing copy")
	}
	if loop.evidence[0].Text != text || loop.turns[0].Batches[0][0].Text != text {
		t.Fatal("compaction must not change the full evidence or the stored turn batches")
	}
}

// TestCompactionSkipsClosingForIndependentSynthesis: once compacted, a stop is
// answered by standalone synthesis over the full evidence.
func TestCompactionSkipsClosingForIndependentSynthesis(t *testing.T) {
	p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolSearchObservations, Arguments: []byte(`{"query":"q"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: ToolDone, Arguments: []byte(`{}`)}}},
		{Content: "the independent answer"},
	}}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "o1", Text: "a fact"}}}
	loop := NewLoop(p, retrieval, Config{Question: "q", MaxIterations: 3})
	loop.compacted = true
	outcome, err := loop.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the independent answer" {
		t.Fatalf("answer = %q, want the standalone synthesis", outcome.Answer)
	}
	scopes := map[string]bool{}
	for _, call := range outcome.LLMCalls {
		scopes[call.Scope] = true
	}
	if scopes["closing_done"] {
		t.Fatal("closing ran despite compaction")
	}
	if !scopes["final"] {
		t.Fatal("the independent synthesis did not run")
	}
}

// TestClosingRequiresToolResultTail: a stop whose conversation does not end in
// a tool result falls back to independent synthesis.
func TestClosingRequiresToolResultTail(t *testing.T) {
	p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolDone, Arguments: []byte(`{}`)}}},
		{Content: "the independent answer"},
	}}
	loop := NewLoop(p, &stubRetrieval{}, Config{Question: "q", MaxIterations: 1})
	outcome, err := loop.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the independent answer" {
		t.Fatalf("answer = %q, want the standalone synthesis", outcome.Answer)
	}
	scopes := map[string]bool{}
	for _, call := range outcome.LLMCalls {
		scopes[call.Scope] = true
	}
	if scopes["closing_done"] {
		t.Fatal("closing ran without a tool-result tail")
	}
	if !scopes["final"] {
		t.Fatal("the independent synthesis did not run")
	}
}

// TestAuxiliaryCallScopesAreNamed: reshape and rewrite carry final_* / reflect_* scopes.
func TestAuxiliaryCallScopesAreNamed(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
	}
	longAnswer := strings.Repeat("word ", 200)
	p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolRecall, Arguments: []byte(`{"query":"q"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: ToolDone, Arguments: []byte(`{}`)}}},
		closingDone(map[string]any{"answer": longAnswer}),
		{Content: "short rewrite"},
		{Structured: map[string]any{"city": "Berlin"}},
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "Berlin"}}}
	loop := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 5, ResponseSchema: schema})
	outcome, err := loop.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]bool{}
	for _, call := range outcome.LLMCalls {
		scopes[call.Scope] = true
	}
	if !scopes["reflect_structured"] || !scopes["final_rewrite"] {
		t.Fatalf("trace = %+v, want reflect_structured and final_rewrite", outcome.LLMCalls)
	}
	if scopes["structured"] || scopes["rewrite"] {
		t.Fatalf("old scope names still present: %+v", outcome.LLMCalls)
	}
}

// failingTurnProvider fails every model call.
type failingTurnProvider struct{}

func (f *failingTurnProvider) Name() string { return "failing-turn" }
func (f *failingTurnProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{}, errors.New("turn failed")
}
func (f *failingTurnProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (f *failingTurnProvider) VerifyConnection(context.Context) error { return nil }
func (f *failingTurnProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// TestFailedTurnUsesErrorScope: a failed loop turn is recorded under agent_<n>_err.
func TestFailedTurnUsesErrorScope(t *testing.T) {
	loop := NewLoop(&failingTurnProvider{}, &stubRetrieval{}, Config{Question: "q"})
	if _, err := loop.Run(context.Background()); err == nil {
		t.Fatal("a failed turn must fail the run")
	}
	if len(loop.llmCalls) == 0 || loop.llmCalls[0].Scope != "agent_1_err" {
		t.Fatalf("llm calls = %+v, want agent_1_err", loop.llmCalls)
	}
}

// TestDeterministicTemperatureOnlyWhenConfigured: mechanical passes pin zero
// only when a reflect temperature was configured; otherwise omit the parameter.
func TestDeterministicTemperatureOnlyWhenConfigured(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
	}
	cases := []struct {
		name       string
		configured bool
		wantNil    bool
	}{
		{name: "configured pins zero", configured: true, wantNil: false},
		{name: "unset omits", configured: false, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{
				{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolRecall, Arguments: []byte(`{"query":"q"}`)}}},
				{ToolCalls: []provider.ToolCall{{ID: "c2", Name: ToolDone, Arguments: []byte(`{}`)}}},
				closingDone(map[string]any{"answer": "Berlin"}),
				{Structured: map[string]any{"city": "Berlin"}},
			}}
			retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "Berlin"}}}
			loop := NewLoop(p, retrieval, Config{
				Question: "q", ResponseSchema: schema, TemperatureConfigured: tc.configured,
			})
			if _, err := loop.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			var extraction *provider.CompletionRequest
			for i := range p.requests {
				if p.requests[i].ResponseSchema != nil {
					extraction = &p.requests[i]
				}
			}
			if extraction == nil {
				t.Fatal("the extraction call never ran")
			}
			if tc.wantNil {
				if extraction.Temperature != nil {
					t.Fatalf("temperature = %v, want omitted", *extraction.Temperature)
				}
				return
			}
			if extraction.Temperature == nil || *extraction.Temperature != 0 {
				t.Fatalf("temperature = %v, want 0", extraction.Temperature)
			}
		})
	}
}

package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// scriptedProvider answers each turn from a script. Past the script it answers
// in prose, so a test's final turn terminates the loop.
type scriptedProvider struct {
	name     string
	script   []provider.CompletionResult
	turns    int
	requests []provider.CompletionRequest
}

func (s *scriptedProvider) Name() string { return s.name }

func (s *scriptedProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	s.requests = append(s.requests, req)
	if s.turns >= len(s.script) {
		return provider.CompletionResult{Content: "answer from what was gathered"}, nil
	}
	result := s.script[s.turns]
	s.turns++
	return result, nil
}

func (s *scriptedProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (s *scriptedProvider) VerifyConnection(context.Context) error { return nil }
func (s *scriptedProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// stubRetrieval records which tools the loop asked for and returns canned
// evidence, so a test can prove the model drove the choice.
type stubRetrieval struct {
	calls        []string
	models       []Evidence
	observations []Evidence
	facts        []Evidence
	expanded     []Evidence
	directives   []string
	// askedIDs records the ids the id-addressed tools were called with, so a
	// test can prove an alias resolved before the lookup.
	askedIDs []string
}

func (s *stubRetrieval) note(name string) { s.calls = append(s.calls, name) }

func (s *stubRetrieval) ReflectMentalModels(context.Context, string, int) ([]Evidence, error) {
	s.note(ToolSearchMentalModels)
	return s.models, nil
}
func (s *stubRetrieval) ReflectObservations(context.Context, string, int) ([]Evidence, error) {
	s.note(ToolSearchObservations)
	return s.observations, nil
}
func (s *stubRetrieval) ReflectMemories(context.Context, string, int) ([]Evidence, error) {
	s.note(ToolRecall)
	return s.facts, nil
}
func (s *stubRetrieval) ReflectReadModels(_ context.Context, ids []string) ([]Evidence, error) {
	s.note(ToolReadMentalModels)
	s.askedIDs = append(s.askedIDs, ids...)
	return s.models, nil
}
func (s *stubRetrieval) ReflectExpand(_ context.Context, ids []string, _ string) ([]Evidence, error) {
	s.note(ToolExpand)
	s.askedIDs = append(s.askedIDs, ids...)
	return s.expanded, nil
}
func (s *stubRetrieval) Directives(context.Context) []string { return s.directives }

// toolCall builds a scripted tool call.
func toolCall(name string, args map[string]any) provider.ToolCall {
	raw, _ := json.Marshal(args)
	return provider.ToolCall{ID: name + "-1", Name: name, Arguments: raw}
}

// turnScript builds a script of tool-call turns, so a test only states the
// calls it cares about.
func turnScript(calls ...provider.ToolCall) []provider.CompletionResult {
	out := make([]provider.CompletionResult, 0, len(calls))
	for _, call := range calls {
		out = append(out, provider.CompletionResult{ToolCalls: []provider.ToolCall{call}})
	}
	return out
}

// test-the-model-decides-which-tool-runs: the loop runs the tools the model
// asked for, in the order it asked, rather than a fixed sequence.
func TestModelDecidesWhichToolRuns(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolSearchObservations, map[string]any{"query": "deploy"}),
		toolCall(ToolRecall, map[string]any{"query": "deploy"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{
		observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "weekly deployments"}},
		facts:        []Evidence{{Kind: "memory", ID: "mem-1", Text: "deploys on Tuesday"}},
	}
	outcome, err := NewLoop(p, retrieval, Config{Question: "when do we deploy?"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) != 2 ||
		retrieval.calls[0] != ToolSearchObservations || retrieval.calls[1] != ToolRecall {
		t.Fatalf("tools ran in the wrong order: %v", retrieval.calls)
	}
	if len(outcome.Turns) != 3 {
		t.Fatalf("loop ran %d turns, want 3 (two retrievals and done)", len(outcome.Turns))
	}
	if len(outcome.Evidence) != 2 {
		t.Fatalf("gathered %d evidence items, want 2", len(outcome.Evidence))
	}
	if outcome.Answer != "answer from what was gathered" {
		t.Fatalf("answer = %q", outcome.Answer)
	}
}

// test-done-ends-the-loop-immediately: the done tool retrieves nothing and ends
// the loop once evidence has been gathered. A done before any evidence is
// ignored by the loop's guardrail.
func TestDoneEndsTheLoop(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "anything"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "fact", ID: "f1", Text: "gathered"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "anything?"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) != 1 || retrieval.calls[0] != ToolRecall {
		t.Fatalf("done must retrieve nothing, but ran %v", retrieval.calls)
	}
	if len(outcome.Turns) != 2 {
		t.Fatalf("loop ran %d turns, want 2", len(outcome.Turns))
	}
}

// A done with no evidence gathered is ignored, so the model cannot answer from
// nothing.
func TestDoneBeforeEvidenceIsIgnored(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolDone, nil),
		toolCall(ToolRecall, map[string]any{"query": "x"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "fact", ID: "f1", Text: "gathered"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) == 0 {
		t.Fatal("the first done must be ignored and a retrieval forced")
	}
	if len(outcome.Evidence) == 0 {
		t.Fatal("the loop must gather evidence before it finishes")
	}
}

// test-iteration-bound-stops-a-loop-that-never-finishes: a model that keeps
// calling tools is stopped at the bound and still answers from what it got.
func TestIterationBoundStopsTheLoop(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "again"}),
		toolCall(ToolRecall, map[string]any{"query": "again"}),
		toolCall(ToolRecall, map[string]any{"query": "again"}),
	)}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "mem-1", Text: "a fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "loop?", MaxIterations: 3}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Turns) != 3 {
		t.Fatalf("loop ran %d turns, want the bound 3", len(outcome.Turns))
	}
	if outcome.Answer == "" {
		t.Fatal("a bounded loop must still answer from what it gathered")
	}
}

// test-repeated-evidence-is-not-counted-twice: asking for the same content
// again must not spend the evidence budget on it a second time.
func TestRepeatedEvidenceIsDeduplicated(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "x"}),
		toolCall(ToolRecall, map[string]any{"query": "x"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "mem-1", Text: "the same fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "repeat?"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Evidence) != 1 {
		t.Fatalf("gathered %d evidence items, want the one unique item", len(outcome.Evidence))
	}
}

// test-context-budget-stops-retrieval: once the evidence budget is spent the
// loop stops asking for more.
func TestContextBudgetStopsRetrieval(t *testing.T) {
	big := strings.Repeat("word ", 20000)
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "x"}),
	)}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "mem-1", Text: big}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "big?", ContextBudget: 100}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) != 1 {
		t.Fatalf("tools ran %d times, want 1 once the budget was spent", len(retrieval.calls))
	}
	if len(outcome.Turns) != 1 {
		t.Fatalf("loop ran %d turns, want 1", len(outcome.Turns))
	}
}

// test-no-model-is-an-explicit-error: the loop is model-driven, so without one
// there is nothing to decide the next step and the caller must be told.
func TestNoModelIsAnExplicitError(t *testing.T) {
	if _, err := NewLoop(nil, &stubRetrieval{}, Config{Question: "q"}).Run(context.Background()); !errors.Is(err, ErrNoModel) {
		t.Fatalf("error = %v, want ErrNoModel", err)
	}
	none := NewLoop(&scriptedProvider{name: "none"}, &stubRetrieval{}, Config{Question: "q"})
	if _, err := none.Run(context.Background()); !errors.Is(err, ErrNoModel) {
		t.Fatalf("the no-model selection must report ErrNoModel, got %v", err)
	}
}

// test-directives-reach-the-model: a bank's standing instructions are part of
// what the model sees.
func TestDirectivesReachTheModel(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(toolCall(ToolDone, nil))}
	retrieval := &stubRetrieval{directives: []string{"always answer in past tense"}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range p.requests[0].Messages {
		if strings.Contains(message.Content, "always answer in past tense") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the directive did not reach the model: %+v", p.requests[0].Messages)
	}
}

// test-tools-are-offered-to-the-model: the loop must advertise its tool set, or
// the model has nothing to choose from.
func TestToolsAreOfferedToTheModel(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(toolCall(ToolDone, nil))}
	if _, err := NewLoop(p, &stubRetrieval{}, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	offered := p.requests[0].Tools
	// The descent opens by offering only the cheapest rung, so a model that
	// ignores tool_choice cannot skip it by calling something else.
	if len(offered) != 1 || offered[0].Name != ToolSearchMentalModels {
		t.Fatalf("opening turn offered %d tools (%v), want just %q", len(offered), offered, ToolSearchMentalModels)
	}
	for _, definition := range offered {
		if !IsKnownTool(definition.Name) {
			t.Fatalf("offered an unknown tool %q", definition.Name)
		}
		if len(definition.Parameters) == 0 {
			t.Fatalf("tool %q carries no parameter schema", definition.Name)
		}
	}
}

// test-tool-name-normalisation: a provider-added namespace prefix or call
// suffix resolves to the same tool.
func TestToolNameNormalisation(t *testing.T) {
	cases := map[string]string{
		"functions.recall": "recall",
		"tools.expand":     "expand",
		"recall_tool":      "recall",
		"done":             "done",
		"  recall  ":       "recall",
	}
	for raw, want := range cases {
		if got := NormalizeToolName(raw); got != want {
			t.Fatalf("normalise %q = %q, want %q", raw, got, want)
		}
	}
}

// test-tool-argument-defaults: a tool called without its optional arguments
// uses its documented default rather than a zero that would retrieve nothing.
func TestToolArgumentDefaults(t *testing.T) {
	if got := intArg(map[string]any{}, "max_tokens", 2048); got != 2048 {
		t.Fatalf("missing argument = %d, want the default 2048", got)
	}
	if got := intArg(map[string]any{"max_tokens": float64(500)}, "max_tokens", 2048); got != 500 {
		t.Fatalf("supplied argument = %d, want 500", got)
	}
	if got := stringArg(map[string]any{"query": "  spaced  "}, "query"); got != "spaced" {
		t.Fatalf("string argument = %q, want it trimmed", got)
	}
	if got := stringListArg(map[string]any{"ids": []any{"a", " b ", ""}}, "ids"); len(got) != 2 {
		t.Fatalf("identifier list = %v, want two non-empty entries", got)
	}
}

// test-empty-answer-is-reported: an answering call that asks for another tool
// instead of answering must be reported, not returned as an empty answer.
func TestEmptyAnswerIsReported(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "x"}),
		toolCall(ToolDone, nil),
		// The answering call returns a tool call, so it produced no prose.
		toolCall(ToolRecall, map[string]any{"query": "x"}),
	)}
	if _, err := NewLoop(p, &stubRetrieval{facts: []Evidence{{Kind: "fact", ID: "f1", Text: "gathered"}}}, Config{Question: "q"}).Run(context.Background()); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("error = %v, want ErrNoAnswer", err)
	}
}

// Every tool schema requires a reason, and the recall/read/expand schemas carry
// the parameters the reference documents.
func TestToolSchemasMatchReference(t *testing.T) {
	for _, name := range ToolNames() {
		var schema map[string]any
		if err := json.Unmarshal(toolParameters(name), &schema); err != nil {
			t.Fatalf("%s schema is not JSON: %v", name, err)
		}
		required, _ := schema["required"].([]any)
		found := false
		for _, item := range required {
			if item == "reason" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s must require reason: %v", name, required)
		}
	}
	recall := string(toolParameters(ToolRecall))
	if !strings.Contains(recall, "max_chunk_tokens") {
		t.Fatalf("recall schema must carry max_chunk_tokens: %s", recall)
	}
	read := string(toolParameters(ToolReadMentalModels))
	if !strings.Contains(read, "mental_model_ids") {
		t.Fatalf("read schema must use mental_model_ids: %s", read)
	}
	expand := string(toolParameters(ToolExpand))
	if !strings.Contains(expand, "depth") {
		t.Fatalf("expand schema must carry depth: %s", expand)
	}
}

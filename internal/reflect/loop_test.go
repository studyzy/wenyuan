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
	directives   []Directive
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
func (s *stubRetrieval) Directives(context.Context) []Directive { return s.directives }

// ctxRecordingRetrieval records the context each Directives read was given, so
// a test can prove the loop's request context reaches the directive reads.
type ctxRecordingRetrieval struct {
	*stubRetrieval
	got []context.Context
}

func (c *ctxRecordingRetrieval) Directives(ctx context.Context) []Directive {
	c.got = append(c.got, ctx)
	return c.directives
}

// test-directive-reads-carry-the-loop-context: the loop serves a request, so
// the directive reads inside message building and composition must run on the
// loop's context — a client disconnect or read timeout has to cancel them too.
func TestDirectiveReadsCarryLoopContext(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "anything"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &ctxRecordingRetrieval{stubRetrieval: &stubRetrieval{
		facts: []Evidence{{Kind: "fact", ID: "f1", Text: "gathered"}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(retrieval.got) < 2 {
		t.Fatalf("Directives called %d times, want at least 2 (prompt build and compose)", len(retrieval.got))
	}
	for i, got := range retrieval.got {
		if got != ctx {
			t.Fatalf("Directives call %d did not receive the loop's context", i)
		}
	}
}

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
	retrieval := &stubRetrieval{directives: []Directive{{Content: "always answer in past tense"}}}
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

// Every tool schema requires a reason, and the recall/read/expand/done schemas
// carry the parameters the reference documents. `done` requires the answer it
// is trusted for; `read_mental_models` requires the ids it exists to read.
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
	done := string(toolParameters(ToolDone))
	if !strings.Contains(done, `"required":["reason","answer"]`) {
		t.Fatalf("done schema must require the answer it is trusted for: %s", done)
	}
}

// test-read-mental-models-requires-ids: the reference's read tool requires the
// ids it exists to read, so a model cannot call it to read nothing.
func TestReadMentalModelsRequiresIDs(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(toolParameters(ToolReadMentalModels), &schema); err != nil {
		t.Fatal(err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 2 || required[1] != "mental_model_ids" {
		t.Fatalf("read_mental_models required = %v, want reason and mental_model_ids", required)
	}
}

// test-document-mode-done-schema: document mode swaps `done`'s answer field for
// the document the stored answer is rendered from, the way the reference does.
func TestDocumentModeDoneSchemaSwapsAnswerForDocument(t *testing.T) {
	var offered map[string]any
	for _, def := range ToolDefinitions(true) {
		if def.Name == ToolDone {
			if err := json.Unmarshal(def.Parameters, &offered); err != nil {
				t.Fatal(err)
			}
		}
	}
	if offered == nil {
		t.Fatal("document mode offers no done tool")
	}
	properties, _ := offered["properties"].(map[string]any)
	if _, ok := properties["document"]; !ok {
		t.Fatalf("document-mode done schema has no document field: %v", properties)
	}
	required, _ := offered["required"].([]any)
	if len(required) != 1 || required[0] != "reason" {
		t.Fatalf("document-mode done required = %v, want reason alone", required)
	}
}

// The final synthesis completion carries the deployment's reflect
// completion-token cap as its transport MaxTokens when one is set. With none
// set the transport level is left unset, never the caller's visible-length
// target: that value is a page-length target enforced by the rewrite, and
// sending it as the provider's output cap lets a thinking model spend it on
// reasoning tokens and truncate the answer mid-word (the reference decouples
// the two for exactly that reason).
func TestSynthesisCarriesReflectMaxCompletionTokens(t *testing.T) {
	cases := []struct {
		name    string
		cap     int
		maxTok  int
		wantCap int
	}{
		{name: "cap set wins", cap: 512, maxTok: 128, wantCap: 512},
		{name: "unset leaves the transport level unset", cap: 0, maxTok: 128, wantCap: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedProvider{name: "openai", script: turnScript(
				toolCall(ToolRecall, map[string]any{"query": "q"}),
			)}
			retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "gathered"}}}
			_, err := NewLoop(p, retrieval, Config{
				Question:            "q",
				MaxTokens:           tc.maxTok,
				MaxCompletionTokens: tc.cap,
			}).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// The synthesis call is the tool-less reflect-scope completion; the
			// reasoning turns carry a tool list.
			synthesis := -1
			for _, req := range p.requests {
				if req.Scope == "reflect" && len(req.Tools) == 0 {
					synthesis = req.MaxTokens
				}
			}
			if synthesis != tc.wantCap {
				t.Fatalf("synthesis MaxTokens = %d, want %d", synthesis, tc.wantCap)
			}
		})
	}
}

// test-loop-sums-turn-usage: every retrieval turn's model call is billed, so
// the run's usage must include them, not only the closing calls. The reference
// reports the whole loop's tokens; a total that omits the agent turns
// under-reports the run by everything the retrieval cost.
func TestLoopSumsTurnUsage(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{
			ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})},
			Usage:     provider.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		},
		{
			ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)},
			Usage:     provider.Usage{InputTokens: 200, OutputTokens: 30, TotalTokens: 230},
		},
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "fact", ID: "f1", Text: "gathered"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Usage.InputTokens != 300 || outcome.Usage.OutputTokens != 50 || outcome.Usage.TotalTokens != 350 {
		t.Fatalf("usage = %+v, want input 300/output 50/total 350", outcome.Usage)
	}
}

// test-done-answer-is-the-answer: a model that calls done with its answer has
// answered. The loop returns that text verbatim — the reference trusts
// done.answer — instead of paying for a second synthesis call that would
// restate what the loop already has. The two tool turns are the whole run.
func TestDoneAnswerIsTrustedWithoutSynthesis(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
		toolCall(ToolDone, map[string]any{"answer": "  the answer  "}),
	)}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 1000}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the answer" {
		t.Fatalf("answer = %q, want the done call's own answer", outcome.Answer)
	}
	if len(p.requests) != 2 {
		t.Fatalf("provider saw %d calls, want only the two loop turns", len(p.requests))
	}
	for _, req := range p.requests {
		if len(req.Tools) == 0 {
			t.Fatalf("a tool-less synthesis call was made for a done answer")
		}
	}
}

// The done path is not a bypass of the post-answer work: an over-budget done
// answer goes through the same length rewrite a composed one does, and the
// rewrite's default template still reaches the provider.
func TestDoneAnswerStillRewrittenToBudget(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("detail. ", 200))
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, map[string]any{"answer": long}),
		),
		provider.CompletionResult{Content: "short"},
	)}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 20}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "short" {
		t.Fatalf("answer = %q, want the shortened done answer", outcome.Answer)
	}
	if len(p.requests) != 3 {
		t.Fatalf("provider saw %d calls, want the two loop turns plus the rewrite", len(p.requests))
	}
	if p.requests[2].Messages[0].Content != rewriteSystem {
		t.Fatalf("third call is not the length rewrite: %#v", p.requests[2].Messages)
	}
}

// truncatingProvider drives two tool turns and then answers the synthesis call
// with a generation that stopped at its output limit, carrying the text it
// wrote before stopping — the shape a small max_tokens produces.
type truncatingProvider struct {
	name     string
	toolCall int
	requests []provider.CompletionRequest
}

func (t *truncatingProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	t.requests = append(t.requests, req)
	if len(req.Tools) > 0 {
		t.toolCall++
		if t.toolCall == 1 {
			return provider.CompletionResult{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}}, nil
		}
		return provider.CompletionResult{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}}, nil
	}
	return provider.CompletionResult{Content: "short syn", FinishReason: "length"}, &provider.OutputTooLongError{FinishReason: "length"}
}

func (t *truncatingProvider) Name() string { return t.name }
func (t *truncatingProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (t *truncatingProvider) VerifyConnection(context.Context) error { return nil }
func (t *truncatingProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// test-output-limit-is-not-fatal: the synthesis call stopping at its output
// limit is not a failure. The reference tolerates finish_reason=length and
// answers with the returned content; treating it as fatal is what turned a
// small max_tokens into a 500.
func TestOutputLimitInSynthesisIsTolerated(t *testing.T) {
	p := &truncatingProvider{name: "openai"}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "fact", ID: "f1", Text: "gathered"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "short syn" {
		t.Fatalf("answer = %q, want the truncated synthesis text", outcome.Answer)
	}
}

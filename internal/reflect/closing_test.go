package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// closingDone builds the closing step's response: the `done` call the closing
// step pins by tool_choice, carrying the answer (or the document) the run
// reports.
func closingDone(args map[string]any) provider.CompletionResult {
	return provider.CompletionResult{ToolCalls: []provider.ToolCall{toolCall(ToolDone, args)}}
}

// test-the-free-turns-answer-is-discarded: a done call ends retrieval, but its
// own answer is not the answer. The closing step asks for the answer again —
// with the done-request prompt appended to the conversation the evidence was
// gathered in and `done` pinned by tool_choice — and that call's answer is what
// the run reports. The reference writes every default-path answer this way: the
// free turn's wording is thrown away, not trusted.
func TestDoneAnswerIsDiscardedForTheClosingStep(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, map[string]any{"answer": "the free turn's wording"})}},
		closingDone(map[string]any{"answer": "the closing answer"}),
	}}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the closing answer" {
		t.Fatalf("answer = %q, want the closing step's answer", outcome.Answer)
	}
	if len(p.requests) != 3 {
		t.Fatalf("provider saw %d calls, want the two loop turns plus the closing step", len(p.requests))
	}
	closing := p.requests[2]
	if got := pinnedTool(closing); got != ToolDone {
		t.Fatalf("closing tool choice = %q, want %q", got, ToolDone)
	}
	last := closing.Messages[len(closing.Messages)-1]
	if !strings.Contains(last.Content, "## Answer now") {
		t.Fatalf("the closing request must append the done-request prompt, got %q", last.Content)
	}
	if strings.Contains(last.Content, "the free turn's wording") {
		t.Fatal("the discarded answer must not ride the closing request")
	}
	// The trace names the closing call its own scope, so a reader can tell
	// which path produced the answer.
	if len(outcome.LLMCalls) != 3 ||
		outcome.LLMCalls[0].Scope != "agent_1" || outcome.LLMCalls[1].Scope != "agent_2" ||
		outcome.LLMCalls[2].Scope != "closing_done" {
		t.Fatalf("llm calls = %+v, want agent_1, agent_2, closing_done", outcome.LLMCalls)
	}
}

// The closing step's prompt restates the question, so the answer is written
// against the request and the evidence rather than recalled from the free
// turn's wording.
func TestClosingPromptCarriesTheQuestion(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "done"}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "the actual question?"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	closing := p.requests[2]
	last := closing.Messages[len(closing.Messages)-1]
	if !strings.Contains(last.Content, "the actual question?") {
		t.Fatalf("closing prompt = %q, want the question restated", last.Content)
	}
}

// test-prose-stop-is-not-the-answer: a model that stops with prose after
// gathering evidence routes through the closing step like a done call does —
// the prose is dropped, not salvaged into the answer where a raw done payload
// could leak ids into user-visible text.
func TestProseStopRoutesThroughTheClosingStep(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{Content: "free-text prose the loop must discard"},
		closingDone(map[string]any{"answer": "the closing answer"}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the closing answer" {
		t.Fatalf("answer = %q, want the closing step's answer", outcome.Answer)
	}
	if len(p.requests) != 3 {
		t.Fatalf("provider saw %d calls, want the loop turn, the prose turn and the closing step", len(p.requests))
	}
}

// test-answer-ready-is-the-stop-signal: the lone answer_ready tool — one
// required bool — ends retrieval and routes through the closing step, the cheap
// way to say "enough" when the answer itself would be discarded anyway.
func TestAnswerReadyStopsTheLoop(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolAnswerReady, map[string]any{"answer_found": true})}},
		closingDone(map[string]any{"answer": "the closing answer"}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the closing answer" {
		t.Fatalf("answer = %q, want the closing step's answer", outcome.Answer)
	}
	if len(outcome.Turns) != 2 {
		t.Fatalf("loop ran %d turns, want the retrieval turn and the answer_ready turn", len(outcome.Turns))
	}
}

// answer_ready is offered next to done once the forced descent releases, so the
// model has the cheap stop signal to reach for.
func TestAnswerReadyIsOfferedToTheModel(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchMentalModels, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "done"}),
	}}
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "stale page"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The turn after the descent completes is the first free turn.
	free := p.requests[3]
	if free.ToolChoice == nil || free.ToolChoice.Name != "" {
		t.Fatalf("turn 4 tool choice = %+v, want a free turn", free.ToolChoice)
	}
	offered := false
	for _, tool := range free.Tools {
		if tool.Name == ToolAnswerReady {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("free turn offered %v, want answer_ready among them", free.Tools)
	}
}

// answer_ready alongside other calls is not a stop: those calls run and the
// model is asked again, so a batch that keeps retrieving is not cut short by the
// signal riding along with it.
func TestAnswerReadyAlongsideOtherCallsIsNotAStop(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchMentalModels, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{
			toolCall(ToolAnswerReady, map[string]any{"answer_found": true}),
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
		}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "the closing answer"}),
	}}
	// Fresh pages release the forced descent, so the second turn is free and the
	// answer_ready rides alongside a real retrieval.
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "fresh page", Fresh: true}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) != 2 {
		t.Fatalf("tools ran %v, want the answer_ready's neighbour to run too", retrieval.calls)
	}
	if outcome.Answer != "the closing answer" {
		t.Fatalf("answer = %q, want the closing step's answer", outcome.Answer)
	}
}

// test-closing-failure-falls-back-to-synthesis: the closing step is the answer
// path only while it works. A provider failure on that call falls back to the
// standalone synthesis, whose scope stays `final` — the two paths must stay
// distinguishable in the trace.
func TestClosingFailureFallsBackToStandaloneSynthesis(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		{Content: "closing prose, not the done call"},
		{Content: "the synthesized answer"},
	}}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the synthesized answer" {
		t.Fatalf("answer = %q, want the standalone synthesis", outcome.Answer)
	}
	if len(outcome.LLMCalls) != 4 || outcome.LLMCalls[3].Scope != "final" {
		t.Fatalf("llm calls = %+v, want the synthesis recorded under final", outcome.LLMCalls)
	}
}

// A closing call that fails outright — a provider outage, say — falls back the
// same way, so an answer still reaches the caller.
func TestClosingErrorFallsBackToStandaloneSynthesis(t *testing.T) {
	p := &failingClosingProvider{}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "the synthesized answer" {
		t.Fatalf("answer = %q, want the standalone synthesis", outcome.Answer)
	}
	if len(outcome.LLMCalls) != 4 || outcome.LLMCalls[2].Scope != "closing_done" || outcome.LLMCalls[3].Scope != "final" {
		t.Fatalf("llm calls = %+v, want the failed closing and the synthesis", outcome.LLMCalls)
	}
}

// failingClosingProvider answers the loop turns from a fixed shape and fails
// the closing call, so the fallback path can be exercised end to end.
type failingClosingProvider struct {
	turns    int
	requests []provider.CompletionRequest
}

func (f *failingClosingProvider) Name() string { return "failing-closing" }

func (f *failingClosingProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	f.requests = append(f.requests, req)
	if req.ToolChoice != nil && req.ToolChoice.Name == ToolDone {
		return provider.CompletionResult{}, errors.New("closing call failed")
	}
	if len(req.Tools) == 0 {
		return provider.CompletionResult{Content: "the synthesized answer"}, nil
	}
	f.turns++
	if f.turns == 1 {
		return provider.CompletionResult{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}}, nil
	}
	return provider.CompletionResult{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}}, nil
}

func (f *failingClosingProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (f *failingClosingProvider) VerifyConnection(context.Context) error { return nil }
func (f *failingClosingProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// test-search-first-gate: a stop before any search has returned is refused with
// the search-first message replayed as the call's own tool result, and the loop
// keeps going. "A retrieval tool returned" is the gate — even an empty one —
// not "evidence is non-empty".
func TestDoneBeforeAnySearchIsRefusedWithTheSearchFirstMessage(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "the closing answer"}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) == 0 {
		t.Fatal("the refused done must not end the loop")
	}
	// The refusal rides the next request as the done call's own tool result.
	sawRefusal := false
	for _, request := range p.requests[1:] {
		for _, message := range request.Messages {
			if message.Role == provider.RoleTool && strings.Contains(message.Content, "You must search for information first") {
				sawRefusal = true
			}
		}
	}
	if !sawRefusal {
		t.Fatal("the search-first refusal never reached the model")
	}
	if outcome.Answer != "the closing answer" {
		t.Fatalf("answer = %q, want the closing step's answer after the refusal", outcome.Answer)
	}
}

// An empty search still counts as having searched: a done that reports "nothing
// is recorded" after a retrieval returned is accepted, because "nothing" is then
// the honest answer.
func TestDoneAfterAnEmptySearchIsAccepted(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "nothing is recorded"}),
	}}
	// The search returns nothing: under an evidence-based gate this done would
	// be ignored, and "nothing is recorded" would never be reachable.
	retrieval := &stubRetrieval{}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Turns) != 2 {
		t.Fatalf("loop ran %d turns, want the search and the accepted done", len(outcome.Turns))
	}
	if outcome.Answer != "nothing is recorded" {
		t.Fatalf("answer = %q, want the closing answer over the empty evidence", outcome.Answer)
	}
}

// test-negative-answer-forces-recall: a stop reporting nothing found before
// recall ran is refused once, the next turn is pinned to recall, and only after
// recall returns can the run stop on a negative answer. A negative must not come
// from the page or observation layer alone.
func TestNegativeAnswerForcesRecallOnce(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchMentalModels, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, map[string]any{"answer_found": false})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, map[string]any{"answer_found": false})}},
		closingDone(map[string]any{"answer": "nothing is recorded"}),
	}}
	// Fresh pages release the forced descent, so the refusal's next turn can be
	// pinned to recall rather than to the descent's next layer.
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "fresh page", Fresh: true}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.calls) != 2 || retrieval.calls[1] != ToolRecall {
		t.Fatalf("tools ran %v, want the search and the forced recall", retrieval.calls)
	}
	// The turn after the refusal is pinned to recall.
	if got := pinnedTool(p.requests[2]); got != ToolRecall {
		t.Fatalf("turn after refusal tool choice = %q, want %q", got, ToolRecall)
	}
	// The refusal names the missing recall.
	sawRefusal := false
	for _, message := range p.requests[2].Messages {
		if message.Role == provider.RoleTool && strings.Contains(message.Content, "recall has not run yet") {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Fatal("the recall-first refusal never reached the model")
	}
	if outcome.Answer != "nothing is recorded" {
		t.Fatalf("answer = %q, want the closing answer after the forced recall", outcome.Answer)
	}
}

// test-free-turns-pin-required: a free turn pins tool_choice required rather
// than leaving the model free — left to choose, most models stop with the answer
// as prose, which the closing step throws away and writes again.
func TestFreeTurnsPinRequired(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchMentalModels, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "done"}),
	}}
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "stale page"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	free := p.requests[3]
	if free.ToolChoice == nil || free.ToolChoice.Mode != "required" || free.ToolChoice.Name != "" {
		t.Fatalf("free turn tool choice = %+v, want required with no named tool", free.ToolChoice)
	}
}

// test-closing-usage-is-billed: the closing step is one more model call, so its
// tokens — the cached and reasoning counts included — ride the run's total the
// way the reference sums every call's usage.
func TestClosingUsageIsBilledIncludingCachedAndReasoningTokens(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{
			ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})},
			Usage: provider.Usage{
				InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
				CachedInputTokens: 40, ReasoningTokens: 5,
			},
		},
		{
			ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)},
			Usage: provider.Usage{
				InputTokens: 200, OutputTokens: 30, TotalTokens: 230,
				CachedInputTokens: 80, ReasoningTokens: 7,
			},
		},
		{
			ToolCalls: []provider.ToolCall{toolCall(ToolDone, map[string]any{"answer": "done"})},
			Usage: provider.Usage{
				InputTokens: 300, OutputTokens: 10, TotalTokens: 310,
				CachedInputTokens: 60, ReasoningTokens: 3,
			},
		},
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Usage.InputTokens != 600 || outcome.Usage.OutputTokens != 60 || outcome.Usage.TotalTokens != 660 {
		t.Fatalf("usage = %+v, want every call's tokens summed", outcome.Usage)
	}
	if outcome.Usage.CachedInputTokens != 180 || outcome.Usage.ReasoningTokens != 15 {
		t.Fatalf("usage = %+v, want the cached and reasoning counts summed too", outcome.Usage)
	}
}

// test-citation-narrows-the-evidence: a closing done that cites ids narrows the
// reported evidence to them, per collection — memories by memory_ids,
// perspectives by mental_model_ids, observations by observation_ids — while an
// uncited collection keeps everything it retrieved (the reference's based_on
// membership rule).
func TestClosingCitationsNarrowTheReportedEvidence(t *testing.T) {
	memory := "11111111-1111-1111-1111-111111111111"
	otherMemory := "22222222-2222-2222-2222-222222222222"
	model := "33333333-3333-3333-3333-333333333333"
	observation := "44444444-4444-4444-4444-444444444444"
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchMentalModels, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{
			"answer":           "done",
			"memory_ids":       []string{memory},
			"mental_model_ids": []string{model},
		}),
	}}
	retrieval := &storageRetrieval{
		memories: []storage.ReflectCitation{
			{ID: memory, Text: "cited fact"},
			{ID: otherMemory, Text: "uncited fact"},
		},
		observations: []storage.ReflectCitation{{ID: observation, Text: "uncited observation"}},
		models:       []storage.ReflectCitation{{ID: model, Text: "cited page"}},
	}
	outcome, err := NewRunner(p).Run(context.Background(), storage.AgenticReflectRequest{
		BankID: "b1", Question: "q", Retrieval: retrieval,
	})
	if err != nil {
		t.Fatal(err)
	}
	citedMemory := 0
	citedOther := false
	for _, fact := range outcome.Facts {
		if fact.ID == memory {
			citedMemory++
		}
		if fact.ID == otherMemory {
			citedOther = true
		}
	}
	if citedMemory != 1 || citedOther {
		t.Fatalf("facts = %+v, want only the cited memory among the facts", outcome.Facts)
	}
	// Observations were not cited, so that collection keeps everything it
	// retrieved; the cited memories narrow to their ids.
	if len(outcome.Facts) != 2 {
		t.Fatalf("facts = %+v, want the cited memory and the uncited observation", outcome.Facts)
	}
	if len(outcome.MentalModels) != 1 || outcome.MentalModels[0].ID != model {
		t.Fatalf("mental models = %+v, want only the cited page", outcome.MentalModels)
	}
}

// A done that cites nothing keeps every collection whole — the ids are a
// narrowing, not a requirement.
func TestClosingWithoutCitationsKeepsAllEvidence(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"answer": "done"}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want everything the loop retrieved", outcome.Evidence)
	}
}

// A done citing only identifiers nothing retrieved narrows nothing: unknown ids
// are dropped before the filter, so a hallucinated citation cannot empty the
// evidence out.
func TestClosingUnknownCitationsNarrowNothing(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{
			"answer":     "done",
			"memory_ids": []string{"99999999-9999-9999-9999-999999999999"},
		}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want the retrieved memory kept", outcome.Evidence)
	}
}

// The closing done's citations arrive as the aliases the evidence was shown
// under, and the membership filter reads the resolved identifiers.
func TestClosingCitationsResolveAliases(t *testing.T) {
	longID := "9f8e7d6c-5b4a-3928-1716-0f1e2d3c4b5a"
	otherID := "8f8e7d6c-5b4a-3928-1716-0f1e2d3c4b5a"
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		// The model saw both facts as f1 and f2 and cites f1 only.
		closingDone(map[string]any{"answer": "done", "memory_ids": []string{"f1"}}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{
		{Kind: "memory", ID: longID, Text: "cited"},
		{Kind: "memory", ID: otherID, Text: "uncited"},
	}}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Evidence) != 1 || outcome.Evidence[0].ID != longID {
		t.Fatalf("evidence = %+v, want only the aliased citation resolved to its memory", outcome.Evidence)
	}
}

// test-closing-document-rejected-then-accepted: a document-mode closing done
// that misses the declared shape is re-asked once with the reasons attached to
// the closing conversation; the accepted document renders the stored answer.
func TestClosingDocumentRejectionIsReAsked(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"document": "not a document"}),
		closingDone(map[string]any{"document": map[string]any{"sections": []any{
			map[string]any{"heading": "Title", "level": 2, "blocks": []any{"Body text."}},
		}}}),
	}}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	outcome, err := NewLoop(p, retrieval, Config{AnswerAsDocument: true, Question: "q"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "## Title\n\nBody text." {
		t.Fatalf("answer = %q, want the rendered document", outcome.Answer)
	}
	if len(p.requests) != 4 {
		t.Fatalf("provider saw %d calls, want the loop turns plus the closing re-ask", len(p.requests))
	}
	// The re-ask carries the refusal after the done-request prompt, so the
	// model fixes the shape instead of repeating it.
	last := p.requests[3].Messages
	if !strings.Contains(last[len(last)-1].Content, "did not match the required shape") {
		t.Fatalf("re-ask messages = %#v, want the shape refusal attached", last)
	}
}

// Out of re-asks, a document that never matches the shape fails the run rather
// than store something nobody can trust.
func TestClosingDocumentRejectionsAreBounded(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolSearchObservations, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{"document": "still not a document"}),
		closingDone(map[string]any{"document": "still not a document"}),
	}}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	if _, err := NewLoop(p, retrieval, Config{AnswerAsDocument: true, Question: "q"}).Run(context.Background()); err == nil {
		t.Fatal("a document that never matches the shape must fail the run")
	}
}

// A closing done carrying no usable answer fails the run: standing in a
// placeholder would read as a real answer downstream.
func TestClosingDoneWithoutAnAnswerFails(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{toolCall(ToolRecall, map[string]any{"query": "q"})}},
		{ToolCalls: []provider.ToolCall{toolCall(ToolDone, nil)}},
		closingDone(map[string]any{}),
	}}
	retrieval := &stubRetrieval{facts: []Evidence{{Kind: "memory", ID: "m1", Text: "fact"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("error = %v, want ErrNoAnswer", err)
	}
}

// The answer_ready schema is the cheap stop signal: one required bool, nothing
// else — adding a reason or an answer would defeat the point.
func TestAnswerReadySchemaIsTheSingleBool(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(toolParameters(ToolAnswerReady), &schema); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) != 1 {
		t.Fatalf("answer_ready properties = %v, want answer_found alone", properties)
	}
	if _, ok := properties["answer_found"]; !ok {
		t.Fatalf("answer_ready properties = %v, want answer_found", properties)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "answer_found" {
		t.Fatalf("answer_ready required = %v, want answer_found", required)
	}
}

// The done schema declares the id arrays the citation membership reads, and the
// answer_found verdict the absence gate reads — in both prose and document mode.
func TestDoneSchemaCarriesCitationsAndVerdict(t *testing.T) {
	for _, document := range []bool{false, true} {
		var schema map[string]any
		raw := toolParameters(ToolDone)
		if document {
			for _, def := range ToolDefinitions(true) {
				if def.Name == ToolDone {
					raw = def.Parameters
				}
			}
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, field := range []string{"memory_ids", "mental_model_ids", "observation_ids", "answer_found"} {
			if _, ok := properties[field]; !ok {
				t.Fatalf("document=%v done properties = %v, want %s", document, properties, field)
			}
		}
		required, _ := schema["required"].([]any)
		found := false
		for _, name := range required {
			if name == "answer_found" {
				found = true
			}
		}
		if !found {
			t.Fatalf("document=%v done required = %v, want answer_found", document, required)
		}
	}
}

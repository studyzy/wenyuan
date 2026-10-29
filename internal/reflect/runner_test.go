package reflect

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// storageRetrieval is a scripted storage-side retrieval, so the adapter can be
// exercised without a store.
type storageRetrieval struct {
	banks        []string
	models       []storage.ReflectCitation
	memories     []storage.ReflectCitation
	observations []storage.ReflectCitation
}

func (s *storageRetrieval) ReflectMentalModels(_ context.Context, bankID, _ string, _ int) ([]storage.ReflectCitation, error) {
	s.banks = append(s.banks, bankID)
	return s.models, nil
}

func (s *storageRetrieval) ReflectObservations(_ context.Context, bankID, _ string, _ int) ([]storage.ReflectCitation, error) {
	s.banks = append(s.banks, bankID)
	return s.observations, nil
}

func (s *storageRetrieval) ReflectMemories(_ context.Context, bankID, _ string, _ int) ([]storage.ReflectCitation, error) {
	s.banks = append(s.banks, bankID)
	return s.memories, nil
}

func (s *storageRetrieval) ReflectReadModels(_ context.Context, bankID string, _ []string) ([]storage.ReflectCitation, error) {
	s.banks = append(s.banks, bankID)
	return s.models, nil
}

func (s *storageRetrieval) ReflectExpand(_ context.Context, req storage.ReflectExpandRequest) ([]storage.ReflectCitation, error) {
	s.banks = append(s.banks, req.BankID)
	return s.memories, nil
}

// A reflection driven by a model returns the model's answer and reports the
// evidence it relied on, tagged with the kind of retrieval that produced it.
func TestRunnerReturnsAnswerAndEvidence(t *testing.T) {
	model := &scriptedProvider{
		name: "openai-compatible",
		script: []provider.CompletionResult{{
			ToolCalls: []provider.ToolCall{{
				ID: "c1", Name: ToolRecall, Arguments: []byte(`{"query":"topic"}`),
			}},
		}},
	}
	retrieval := &storageRetrieval{
		memories: []storage.ReflectCitation{{ID: "m-1", Text: "a memory", DocumentID: "doc-1", ChunkID: "chunk-1"}},
	}
	runner := NewRunner(model)
	outcome, err := runner.Run(context.TODO(), storage.AgenticReflectRequest{
		BankID:    "b1",
		Question:  "what happened?",
		Retrieval: retrieval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer == "" {
		t.Fatal("the runner must report the model's answer")
	}
	if len(outcome.Facts) != 1 {
		t.Fatalf("reported %d cited facts, want 1: %+v", len(outcome.Facts), outcome.Facts)
	}
	fact := outcome.Facts[0]
	if fact.ID != "m-1" || fact.Text != "a memory" || fact.Type != "memory" {
		t.Fatalf("citation = %+v, want the retrieved memory tagged as such", fact)
	}
	if fact.DocumentID != "doc-1" || fact.ChunkID != "chunk-1" {
		t.Fatalf("citation lost its provenance: %+v", fact)
	}
}

// The adapter binds the bank the request named, so the loop's methods need not
// carry it.
func TestRunnerBindsTheRequestedBank(t *testing.T) {
	model := &scriptedProvider{
		name: "openai-compatible",
		script: []provider.CompletionResult{{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolRecall, Arguments: []byte(`{}`)}},
		}},
	}
	retrieval := &storageRetrieval{}
	if _, err := NewRunner(model).Run(context.TODO(), storage.AgenticReflectRequest{
		BankID: "bank-under-test", Question: "q", Retrieval: retrieval,
	}); err != nil {
		t.Fatal(err)
	}
	if len(retrieval.banks) == 0 {
		t.Fatal("the retrieval was never called")
	}
	for _, bank := range retrieval.banks {
		if bank != "bank-under-test" {
			t.Fatalf("retrieval called with bank %q, want the requested one", bank)
		}
	}
}

// Directives supplied with the request reach the model as standing
// instructions, so a bank's rules shape the reflection.
func TestRunnerPassesDirectivesToTheModel(t *testing.T) {
	// A tool-calling turn first: a model that answers in prose before calling
	// any tool has not driven the loop at all, which is a different story.
	model := &scriptedProvider{name: "openai-compatible", script: turnScript(
		toolCall(ToolRecall, map[string]any{"query": "q"}),
	)}
	outcome, err := NewRunner(model).Run(context.TODO(), storage.AgenticReflectRequest{
		BankID:     "b1",
		Question:   "q",
		Retrieval:  &storageRetrieval{},
		Directives: []storage.ReflectDirective{{Name: "Preference", Content: "prefer the newest fact"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer == "" {
		t.Fatal("expected an answer")
	}
	found := false
	for _, request := range model.requests {
		for _, message := range request.Messages {
			if message.Role == "system" && strings.Contains(message.Content, "**Preference**: prefer the newest fact") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the directive never reached the model")
	}
}

// Without a model the loop cannot run, and that is reported rather than
// answered from nothing.
func TestRunnerWithoutModelReportsNoModel(t *testing.T) {
	runner := NewRunner(nil)
	if _, err := runner.Run(context.TODO(), storage.AgenticReflectRequest{
		BankID: "b1", Question: "q", Retrieval: &storageRetrieval{},
	}); err == nil {
		t.Fatal("expected an error when no model is configured")
	}
}

// test-trace-excludes-done: `done` ends the run and the closing step writes the
// answer; it retrieves nothing, so the trace must list only the tools that
// gathered evidence. The reference's tool trace never contains a done entry.
func TestRunnerTraceExcludesDoneTool(t *testing.T) {
	model := &scriptedProvider{name: "openai-compatible", script: []provider.CompletionResult{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolRecall, Arguments: []byte(`{"query":"q"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: ToolDone, Arguments: []byte(`{}`)}}},
		closingDone(map[string]any{"answer": "the closing answer"}),
	}}
	retrieval := &storageRetrieval{memories: []storage.ReflectCitation{{ID: "m-1", Text: "a memory"}}}
	outcome, err := NewRunner(model).Run(context.TODO(), storage.AgenticReflectRequest{
		BankID: "b1", Question: "q", Retrieval: retrieval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Trace == nil {
		t.Fatal("expected a trace")
	}
	if len(outcome.Trace.ToolCalls) != 1 {
		t.Fatalf("trace = %+v, want the one retrieval entry", outcome.Trace.ToolCalls)
	}
	for _, call := range outcome.Trace.ToolCalls {
		if call.Tool == ToolDone {
			t.Fatalf("the trace listed the done tool: %+v", outcome.Trace.ToolCalls)
		}
		// The reference's trace entry names the tool inside its input, so a
		// reader can tell what was asked for without walking back to `tool`.
		if call.Input["tool"] != ToolRecall {
			t.Fatalf("trace input = %+v, want the called tool named inside it", call.Input)
		}
		if call.Input["query"] != "q" {
			t.Fatalf("trace input = %+v, want the model's own arguments kept", call.Input)
		}
	}
	// The closing step's answer is the run's answer; the free turn's done
	// wording was discarded.
	if outcome.Answer != "the closing answer" {
		t.Fatalf("answer = %q, want the closing step's answer", outcome.Answer)
	}
}

// The runner maps the loop's cached and reasoning token counts onto the storage
// outcome's usage, so the served response reports them instead of zeros: the
// reference sums both across the run for cost attribution.
func TestRunnerMapsCachedAndReasoningTokens(t *testing.T) {
	model := &scriptedProvider{name: "openai-compatible", script: []provider.CompletionResult{
		{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolRecall, Arguments: []byte(`{"query":"q"}`)}},
			Usage:     provider.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CachedInputTokens: 40, ReasoningTokens: 6},
		},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: ToolDone, Arguments: []byte(`{}`)}}},
		{
			ToolCalls: []provider.ToolCall{{ID: "c3", Name: ToolDone, Arguments: []byte(`{"answer":"done"}`)}},
			Usage:     provider.Usage{InputTokens: 50, OutputTokens: 5, TotalTokens: 55, CachedInputTokens: 20, ReasoningTokens: 4},
		},
	}}
	outcome, err := NewRunner(model).Run(context.TODO(), storage.AgenticReflectRequest{
		BankID: "b1", Question: "q", Retrieval: &storageRetrieval{memories: []storage.ReflectCitation{{ID: "m-1", Text: "a memory"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Usage.CachedTokens != 60 || outcome.Usage.ThoughtsTokens != 10 {
		t.Fatalf("usage = %+v, want cached 60 and thoughts 10 summed across the run", outcome.Usage)
	}
}

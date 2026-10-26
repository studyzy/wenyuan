package reflect

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The reflection reports one trace entry per MODEL CALL it made, not just per
// tool call: the reference's trace carries `llm_calls` with the scope that names
// each one and the milliseconds it took, next to the `tool_calls` list. A run
// that made model calls and reported an empty list told a caller no model was
// involved.
func TestRunnerReportsOneLLMCallPerModelCall(t *testing.T) {
	model := &scriptedProvider{
		name: "openai-compatible",
		script: []provider.CompletionResult{
			// Turn 1: call a retrieval tool.
			{ToolCalls: []provider.ToolCall{{ID: "c1", Name: ToolRecall, Arguments: []byte(`{"query":"topic"}`)}}},
			// Turn 2: finish with the answer.
			{ToolCalls: []provider.ToolCall{{ID: "c2", Name: ToolDone, Arguments: []byte(`{"answer":"the answer"}`)}}},
		},
	}
	retrieval := &storageRetrieval{
		memories: []storage.ReflectCitation{{ID: "m-1", Text: "a memory"}},
	}
	outcome, err := NewRunner(model).Run(context.TODO(), storage.AgenticReflectRequest{
		BankID:                            "b1",
		Question:                          "what?",
		Retrieval:                         retrieval,
		MaxIterations:                     3,
		SearchObservationsMaxTokens:       500,
		SearchObservationsIncludeEntities: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Trace == nil {
		t.Fatal("a reflective run must carry a trace")
	}
	calls := outcome.Trace.LLMCalls
	if len(calls) != model.turns {
		t.Fatalf("%d llm_calls reported for %d model calls: %+v", len(calls), model.turns, calls)
	}
	for index, call := range calls {
		// The loop's turns are scoped `agent_<n>`, 1-based, which is what the
		// reference names them.
		want := "agent_" + itoa(index+1)
		if call.Scope != want {
			t.Fatalf("llm_calls[%d].scope = %q, want %q", index, call.Scope, want)
		}
		if call.DurationMs < 0 {
			t.Fatalf("llm_calls[%d].duration_ms = %d, want a non-negative duration", index, call.DurationMs)
		}
	}
}

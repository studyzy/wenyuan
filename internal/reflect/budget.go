package reflect

import (
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Bounds applied to a tool call's token argument. The ceiling starts at the
// fixed maximum and is tightened for the batch by resolveToolArgCeiling.
const (
	toolArgMinTokens = 1000
	toolArgMaxTokens = 16000
)

// compactedToolResult replaces an early tool result in the model-facing copy of
// the conversation when the conversation no longer fits the budget. The full
// result stays in the evidence the answer is written from.
const compactedToolResult = "[Earlier tool result omitted to fit the context budget; it is kept for the final answer.]"

// evidenceDigest is the leading line of the loop's local evidence summary. It
// lets the closing decision tell that trailing user message apart from a turn.
const evidenceDigest = "Evidence gathered so far:"

// resolveToolArgCeiling bounds one tool call's token ask by what is left of the
// context budget shared across the calls running together in this batch. The
// loop only notices an overshoot between iterations, so bounding the ask here
// is what keeps one parallel round from exceeding the budget before anything
// reacts. It never falls below the usable floor.
func resolveToolArgCeiling(remaining, concurrent int) int {
	if concurrent < 1 {
		concurrent = 1
	}
	share := remaining / concurrent
	return clampInt(share, toolArgMinTokens, toolArgMaxTokens)
}

// messagesTokens approximates the size of a model-facing conversation. A real
// token count is used for the text, with a small per-message overhead, so the
// compaction below reacts to the same budget the provider is asked to hold.
func messagesTokens(messages []provider.Message) int {
	total := 0
	for _, message := range messages {
		total += storage.CountTokens(message.Content)
		for _, call := range message.ToolCalls {
			total += storage.CountTokens(string(call.Arguments))
		}
		total += 4
	}
	return total
}

// compactToolResults blanks the earliest tool results in the model-facing copy
// until the conversation fits the budget again. Only this copy is changed: the
// turns and the evidence the answer is synthesized from keep their full text.
// A run that had to compact reports it, so a later stop answers by standalone
// synthesis instead of appending a closing turn to a conversation whose early
// results were blanked.
func (l *Loop) compactToolResults(messages []provider.Message) []provider.Message {
	total := messagesTokens(messages)
	if total <= l.cfg.ContextBudget {
		return messages
	}
	for index := range messages {
		if total <= l.cfg.ContextBudget {
			break
		}
		if messages[index].Role != provider.RoleTool {
			continue
		}
		content := messages[index].Content
		if content == compactedToolResult {
			continue
		}
		total -= storage.CountTokens(content) - storage.CountTokens(compactedToolResult)
		messages[index].Content = compactedToolResult
		l.compacted = true
	}
	return messages
}

// conversationEndsWithTool reports whether the loop's conversation ends in a
// tool result — the precondition for asking the model to close with a done
// call. The local evidence digest is a summary, not a turn, so trailing digest
// messages are ignored by the decision.
func conversationEndsWithTool(messages []provider.Message) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser &&
			strings.HasPrefix(messages[index].Content, evidenceDigest) {
			continue
		}
		return messages[index].Role == provider.RoleTool
	}
	return false
}

// recordCall appends one model call to the run's trace. Parallel segment calls
// record themselves here, in chunk order, after their results are gathered.
func (l *Loop) recordCall(scope string, durationMs int) {
	l.llmCalls = append(l.llmCalls, LLMCall{Scope: scope, DurationMs: durationMs})
}

// markFailedTurn names a loop turn that failed under the error scope, so the
// trace can tell a turn that did not finish from one that ran to completion.
func (l *Loop) markFailedTurn(iteration int) {
	if index := len(l.llmCalls) - 1; index >= 0 {
		l.llmCalls[index].Scope = fmt.Sprintf("agent_%d_err", iteration)
	}
}

// deterministicTemperature is the temperature of the mechanical passes that
// copy or parse text rather than reason. They run at zero only when a reflect
// temperature was configured at all; otherwise the parameter is omitted, which
// is how an endpoint that rejects temperature is driven.
func (l *Loop) deterministicTemperature() *float64 {
	if !l.cfg.TemperatureConfigured {
		return nil
	}
	value := 0.0
	return &value
}

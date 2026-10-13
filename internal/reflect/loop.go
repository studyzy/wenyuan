package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// DefaultMaxIterations bounds the loop. A reflection that has not converged in
// this many turns is answered from what it has, because an unbounded loop would
// spend the caller's budget without producing an answer.
const DefaultMaxIterations = 10

// DefaultContextBudget approximates the evidence budget in tokens. Once the
// gathered evidence reaches it the loop stops asking for more, so a single
// reflection cannot grow without bound.
const DefaultContextBudget = 24000

// ErrNoModel reports a reflection attempted with no model configured. The loop
// is model-driven, so without one there is nothing to decide the next step.
var ErrNoModel = errors.New("reflection requires a configured model")

// ErrNoAnswer reports a reflection that finished its turns without producing an
// answer, which happens when the answering call asks for another tool.
var ErrNoAnswer = errors.New("reflection produced no answer")

// Evidence is one piece of retrieved context.
type Evidence struct {
	// Kind is which tool produced it.
	Kind string
	// ID identifies the memory, observation or perspective.
	ID string
	// Text is the content the answer may cite.
	Text string
	// DocumentID and ChunkID are the source context when known.
	DocumentID string
	ChunkID    string
}

// Turn is one iteration of the loop.
type Turn struct {
	Iteration int
	Calls     []ToolCall
	Results   []Evidence
}

// Outcome is the finished reflection.
type Outcome struct {
	Answer   string
	Turns    []Turn
	Evidence []Evidence
	Usage    provider.Usage
}

// Retrieval is what the loop needs from the store. It is an interface so the
// loop can be exercised without a database, and so the storage area keeps
// owning the queries.
type Retrieval interface {
	// ReflectMentalModels returns reusable perspectives matching a query.
	ReflectMentalModels(ctx context.Context, query string, maxResults int) ([]Evidence, error)
	// ReflectObservations returns consolidated conclusions matching a query.
	ReflectObservations(ctx context.Context, query string, maxTokens int) ([]Evidence, error)
	// ReflectMemories returns raw memories matching a query.
	ReflectMemories(ctx context.Context, query string, maxTokens int) ([]Evidence, error)
	// ReflectReadModels returns specific perspectives by identifier.
	ReflectReadModels(ctx context.Context, ids []string) ([]Evidence, error)
	// ReflectExpand returns more context for specific memories.
	ReflectExpand(ctx context.Context, ids []string) ([]Evidence, error)
	// Directives returns the standing instructions that apply to this request.
	Directives(ctx context.Context) []string
}

// Config tunes the loop.
type Config struct {
	// MaxIterations bounds the turns.
	MaxIterations int
	// ContextBudget is the evidence token budget.
	ContextBudget int
	// Question is the reflection question.
	Question string
	// MaxTokens caps the answer length.
	MaxTokens int
}

// Loop runs a reflection.
type Loop struct {
	provider  provider.Provider
	retrieval Retrieval
	cfg       Config
	evidence  []Evidence
	seen      map[string]bool
}

// NewLoop builds a loop over the provider and retrieval.
func NewLoop(p provider.Provider, retrieval Retrieval, cfg Config) *Loop {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = DefaultMaxIterations
	}
	if cfg.ContextBudget <= 0 {
		cfg.ContextBudget = DefaultContextBudget
	}
	return &Loop{provider: p, retrieval: retrieval, cfg: cfg, seen: map[string]bool{}}
}

// Run performs the reflection.
//
// The model decides each turn which tool to call. The loop stops when the model
// calls the done tool, when the iteration bound is reached, or when the
// evidence budget is spent; in every case the answer is composed from the
// evidence actually gathered, so the loop never answers from nothing.
func (l *Loop) Run(ctx context.Context) (Outcome, error) {
	if !l.modelAvailable() {
		return Outcome{}, ErrNoModel
	}
	outcome := Outcome{Turns: make([]Turn, 0, l.cfg.MaxIterations)}
	for iteration := 1; iteration <= l.cfg.MaxIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		turn, finish, err := l.step(ctx, iteration)
		if err != nil {
			return outcome, err
		}
		outcome.Turns = append(outcome.Turns, turn)
		outcome.Usage = addUsage(outcome.Usage, turnUsage(turn))
		if finish {
			break
		}
		if l.evidenceTokens() >= l.cfg.ContextBudget {
			break
		}
	}
	outcome.Evidence = append([]Evidence(nil), l.evidence...)
	answer, err := l.compose(ctx, &outcome)
	if err != nil {
		return outcome, err
	}
	outcome.Answer = answer
	return outcome, nil
}

// step runs one turn: ask the model what to do, then do it.
func (l *Loop) step(ctx context.Context, iteration int) (Turn, bool, error) {
	turn := Turn{Iteration: iteration}
	response, err := l.provider.Complete(ctx, provider.CompletionRequest{
		Messages:   l.messages(),
		Tools:      ToolDefinitions(),
		ToolChoice: toolAuto(),
		Scope:      "reflect",
	})
	if err != nil {
		return turn, false, fmt.Errorf("reflection turn %d failed: %w", iteration, err)
	}

	// A model that answered in prose has answered; there is no tool to run.
	if len(response.ToolCalls) == 0 {
		if strings.TrimSpace(response.Content) != "" {
			l.evidence = append(l.evidence, Evidence{Kind: "answer", Text: response.Content})
		}
		return turn, true, nil
	}

	finish := false
	for _, call := range response.ToolCalls {
		// The model supplies its arguments as JSON; a malformed payload is
		// treated as an empty argument set, which the tool's own defaults read.
		input := map[string]any{}
		if len(call.Arguments) > 0 {
			var decoded map[string]any
			if err := json.Unmarshal(call.Arguments, &decoded); err == nil {
				input = decoded
			}
		}
		toolCall := ToolCall{ID: call.ID, Name: NormalizeToolName(call.Name), Input: input}
		turn.Calls = append(turn.Calls, toolCall)
		if toolCall.Name == ToolDone {
			// The done tool retrieves nothing; it only ends the loop.
			finish = true
			continue
		}
		results, err := l.execute(ctx, toolCall)
		if err != nil {
			// A failed retrieval is recorded as an empty result rather than
			// failing the whole reflection, because the model can choose a
			// different tool next turn.
			results = nil
		}
		l.record(results)
		turn.Results = append(turn.Results, results...)
	}
	return turn, finish, nil
}

// execute dispatches one tool call to the retriever.
func (l *Loop) execute(ctx context.Context, call ToolCall) ([]Evidence, error) {
	switch call.Name {
	case ToolSearchMentalModels:
		return l.retrieval.ReflectMentalModels(ctx, stringArg(call.Input, "query"), intArg(call.Input, "max_results", 5))
	case ToolSearchObservations:
		return l.retrieval.ReflectObservations(ctx, stringArg(call.Input, "query"), intArg(call.Input, "max_tokens", 5000))
	case ToolRecall:
		return l.retrieval.ReflectMemories(ctx, stringArg(call.Input, "query"), intArg(call.Input, "max_tokens", 2048))
	case ToolReadMentalModels:
		return l.retrieval.ReflectReadModels(ctx, stringListArg(call.Input, "ids"))
	case ToolExpand:
		return l.retrieval.ReflectExpand(ctx, stringListArg(call.Input, "ids"))
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
}

// record adds newly retrieved evidence, skipping anything already gathered so a
// repeated call does not spend the budget twice on the same content.
func (l *Loop) record(results []Evidence) {
	for _, item := range results {
		key := item.Kind + "\x00" + item.ID
		if l.seen[key] {
			continue
		}
		l.seen[key] = true
		l.evidence = append(l.evidence, item)
	}
}

// messages builds the conversation the model sees this turn.
func (l *Loop) messages() []provider.Message {
	messages := []provider.Message{{Role: "system", Content: systemPrompt}}
	if directives := l.retrieval.Directives(context.Background()); len(directives) > 0 {
		messages = append(messages, provider.Message{
			Role:    "system",
			Content: "Follow these standing instructions:\n- " + strings.Join(sortStrings(directives), "\n- "),
		})
	}
	messages = append(messages, provider.Message{Role: "user", Content: l.cfg.Question})
	if len(l.evidence) > 0 {
		messages = append(messages, provider.Message{
			Role:    "user",
			Content: "Evidence gathered so far:\n" + l.renderEvidence(),
		})
	}
	return messages
}

// renderEvidence renders the gathered evidence for the model, bounded so the
// conversation cannot grow past what the provider accepts.
func (l *Loop) renderEvidence() string {
	var b strings.Builder
	for _, item := range l.evidence {
		line := "[" + item.Kind + "] " + item.Text
		if b.Len()+len(line) > l.cfg.ContextBudget {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// compose asks the model for the final answer from the gathered evidence.
func (l *Loop) compose(ctx context.Context, outcome *Outcome) (string, error) {
	messages := []provider.Message{
		{Role: "system", Content: answerPrompt},
		{Role: "user", Content: l.cfg.Question},
	}
	if rendered := l.renderEvidence(); strings.TrimSpace(rendered) != "" {
		messages = append(messages, provider.Message{Role: "user", Content: "Evidence:\n" + rendered})
	}
	response, err := l.provider.Complete(ctx, provider.CompletionRequest{
		Messages:  messages,
		Scope:     "reflect",
		MaxTokens: l.cfg.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("reflection answer failed: %w", err)
	}
	outcome.Usage = addUsage(outcome.Usage, response.Usage)
	answer := strings.TrimSpace(response.Content)
	if answer == "" {
		// The answering call asked for another tool instead of answering, so
		// there is no answer to return. Reporting that is better than handing
		// the caller an empty string that reads as "the answer is nothing".
		return "", ErrNoAnswer
	}
	return answer, nil
}

// modelAvailable reports whether a model is configured to drive the loop.
func (l *Loop) modelAvailable() bool {
	if l == nil || l.provider == nil {
		return false
	}
	return l.provider.Name() != "none"
}

// evidenceTokens approximates the gathered evidence size in tokens.
func (l *Loop) evidenceTokens() int {
	total := 0
	for _, item := range l.evidence {
		// Four characters per token is the usual rough ratio and is enough for
		// a budget check; a tokeniser would only sharpen a bound.
		total += len(item.Text)/4 + 1
	}
	return total
}

// systemPrompt sets the loop's behavior.
const systemPrompt = `You answer questions about a memory bank.
Use the retrieval tools to gather evidence before answering. Prefer a reusable
perspective over raw memories when one covers the question, and prefer raw
memories over expanding a memory to its source.
Call the done tool once the evidence answers the question. Do not answer from
general knowledge: if the tools return nothing relevant, say so.`

// answerPrompt asks for the final answer.
const answerPrompt = `Answer the question using only the evidence provided.
Cite the memories you relied on by their identifiers. If the evidence does not
answer the question, say that plainly rather than guessing.`

// turnUsage sums the usage a turn's calls reported. The loop records usage from
// the provider responses it receives, so this is a placeholder for the shape
// rather than a second source of truth.
func turnUsage(Turn) provider.Usage { return provider.Usage{} }

// addUsage sums two usage values.
func addUsage(left, right provider.Usage) provider.Usage {
	return provider.Usage{
		InputTokens:  left.InputTokens + right.InputTokens,
		OutputTokens: left.OutputTokens + right.OutputTokens,
		TotalTokens:  left.TotalTokens + right.TotalTokens,
	}
}

// toolAuto lets the model choose whether to call a tool.
func toolAuto() *provider.ToolSelectionPolicy {
	return &provider.ToolSelectionPolicy{Mode: "auto"}
}

// stringArg reads a string argument.
func stringArg(input map[string]any, key string) string {
	value, ok := input[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// intArg reads an integer argument with a default.
func intArg(input map[string]any, key string, fallback int) int {
	switch typed := input[key].(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return fallback
	}
}

// stringListArg reads a list-of-strings argument.
func stringListArg(input map[string]any, key string) []string {
	raw, ok := input[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// DefaultMaxIterations bounds the loop. A reflection that has not converged in
// this many turns is answered from what it has, because an unbounded loop would
// spend the caller's budget without producing an answer.
const DefaultMaxIterations = 10

// DefaultContextBudget approximates the evidence budget in tokens. Once the
// gathered evidence reaches it the loop stops asking for more, so a single
// reflection cannot grow without bound.
const DefaultContextBudget = 24000

// DefaultObservationToolTokens is the budget the search_observations tool
// applies when the model omits a max_tokens argument and neither the request
// nor the bank configured one.
const DefaultObservationToolTokens = 5000

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
	// Type is the memory's fact type (world, experience, observation), which
	// is what a citation reports; empty for a perspective.
	Type string
	// ID identifies the memory, observation or perspective.
	ID string
	// Text is the content the answer may cite.
	Text string
	// DocumentID and ChunkID are the source context when known.
	DocumentID string
	ChunkID    string
	// Metadata, Tags, OccurredStart and OccurredEnd are the source document's
	// stamp and the memory's temporal fields, carried so a tool result can show
	// the model where a fact came from.
	Metadata map[string]any
	Tags     []string
	// Entities are the resolved entity names the fact mentions, carried so an
	// observation's tool result can list them.
	Entities      []string
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	// Attachments are the images the memory was read off, if any.
	Attachments []storage.RecallAttachment
	// Fresh reports that a retrieved perspective is known to be current, so
	// the loop may answer from it without descending to raw facts. It is false
	// for every non-perspective kind, and for a perspective whose staleness is
	// unknown, which the loop treats as unsafe.
	Fresh bool
	// SnippetOnly marks a perspective that arrived as a snippet from a search,
	// so the prompt shows the snippet the model was given — not the whole page
	// it has not read yet. A later read of the same page upgrades the item.
	SnippetOnly bool
}

// Turn is one iteration of the loop.
type Turn struct {
	Iteration int
	Calls     []ToolCall
	Results   []Evidence
	// Batches holds each call's results, indexed to match Calls, so a tool
	// result can be rendered per call rather than as the merged evidence set.
	Batches [][]Evidence
	// Usage is the token accounting the provider reported for this turn's
	// model call. The loop sums it into the run's total, so the caller sees the
	// cost of every retrieval turn rather than only the closing calls.
	Usage provider.Usage
}

// Outcome is the finished reflection.
type Outcome struct {
	Answer   string
	Turns    []Turn
	Evidence []Evidence
	Usage    provider.Usage
	// StructuredOutput is the answer reshaped into the caller's schema, when
	// the request supplied one. It is nil both when no schema was asked for and
	// when the extraction failed; StructuredOutputError tells the two apart.
	StructuredOutput map[string]any
	// StructuredOutputError reports why an asked-for extraction produced no
	// output, so a failed reshape cannot read as an empty one.
	StructuredOutputError string
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
	// ReflectExpand returns more context for specific memories, opened to the
	// requested depth (chunk or document).
	ReflectExpand(ctx context.Context, ids []string, depth string) ([]Evidence, error)
	// Directives returns the standing instructions that apply to this request.
	Directives(ctx context.Context) []Directive
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
	// MaxCompletionTokens is the transport-level output cap for the final
	// synthesis call. It is decoupled from MaxTokens (a visible-length target
	// enforced by prompt and post-hoc rewrite): a zero value leaves the shipped
	// behaviour where MaxTokens is the call's cap, and a positive value makes
	// this the provider cap so a thinking model cannot truncate the page
	// mid-word while an operator still gets a hard cost ceiling.
	MaxCompletionTokens int
	// Budget is the caller's effort level, one of storage.ReflectBudget*.
	// The fresh-page short-circuit does not apply at the high level, where the
	// full descent is wanted regardless of how fresh a page looks.
	Budget string
	// ResponseSchema is the caller's structured-output schema. When set, a
	// second call reshapes the answer into it.
	ResponseSchema map[string]any
	// BankName, Mission and Disposition are the bank's profile, rendered into
	// the prompt. Mission replaces the default role and rides the identity tail;
	// an all-neutral disposition carries the bare traits line.
	BankName    string
	Mission     string
	Disposition map[string]int
	// ObservationMaxTokens is the budget the search_observations tool applies
	// when the model omits a max_tokens argument. A zero value keeps the
	// shipped default.
	ObservationMaxTokens int
	// ObservationIncludeEntities attaches resolved entity names to each
	// observation a search returns.
	ObservationIncludeEntities bool
	// AnswerAsDocument makes the loop take its answer from the `done` call's
	// structure rather than its prose: the model states a structured `document`
	// (or a plain `answer`) and the markdown is derived from it. It is what a
	// mental-model refresh wants — the model's structure, not its prose, is the
	// source of truth — so no free-text render is ever read back to work out
	// what the model meant. Without it the loop still answers from the `done`
	// call's `answer`; this only changes which field that is.
	AnswerAsDocument bool
}

// profile packages the bank's identity for the prompt builders.
func (l *Loop) profile() bankProfile {
	return bankProfile{Name: l.cfg.BankName, Mission: l.cfg.Mission, Disposition: l.cfg.Disposition}
}

// Loop runs a reflection.
type Loop struct {
	provider  provider.Provider
	retrieval Retrieval
	cfg       Config
	evidence  []Evidence
	// seen indexes an evidence item's position by kind and id, so a repeated
	// call does not spend the budget twice on the same content and a later read
	// can upgrade a snippet to the whole page.
	seen    map[string]int
	aliases *aliaser
	// forcedSequence is the retrieval hierarchy in descent order; the loop
	// opens by forcing each layer in turn so the model cannot answer from raw
	// facts, or from nothing, while a cheaper layer is untried.
	forcedSequence []string
	// forcedStepsDone counts the forced layers the model has honoured. The
	// next forced layer is indexed by it.
	forcedStepsDone int
	// forcingReleased is set once a search_mental_models result is fresh and
	// usable enough to hand control back to the model under auto.
	forcingReleased bool
	// freshGuidance, once set, is shown with the evidence on every later turn
	// so the model knows why the forced descent stopped.
	freshGuidance string
	// sawToolCall is set once any turn produced a tool call, which is what makes
	// a later text-only turn a legitimate stop rather than a provider that
	// cannot drive the loop at all.
	sawToolCall bool
	// documentAnswer is the markdown a document-mode `done` call produced, held
	// until the loop stops.
	documentAnswer string
	// doneAnswer is the answer a prose-mode `done` call carried, held until the
	// loop stops. The model's own done call is the answer: the reference trusts
	// it verbatim rather than paying for a second synthesis call that would
	// restate what the loop already has. Empty means no done call produced a
	// usable answer, which is when synthesis takes over.
	doneAnswer string
	// documentRejections counts `done` documents the shape refused, which is
	// what bounds the re-asks.
	documentRejections int
	// documentFeedback is the reason each refused document was handed back, sent
	// with the re-ask so the model can fix the shape instead of repeating it.
	documentFeedback []string
	// turns records the retrieval turns already run, so a later turn replays the
	// model's own tool calls and their results as the conversation they were.
	turns []Turn
}

// NewLoop builds a loop over the provider and retrieval.
func NewLoop(p provider.Provider, retrieval Retrieval, cfg Config) *Loop {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = DefaultMaxIterations
	}
	if cfg.ContextBudget <= 0 {
		cfg.ContextBudget = DefaultContextBudget
	}
	return &Loop{
		provider: p, retrieval: retrieval, cfg: cfg, seen: map[string]int{},
		aliases: newAliaser(), forcedSequence: forcedLayers(),
	}
}

// forcedLayers is the retrieval hierarchy in descent order: the cheapest,
// most reusable layer first, raw facts last.
func forcedLayers() []string {
	return []string{ToolSearchMentalModels, ToolSearchObservations, ToolRecall}
}

// Run performs the reflection.
//
// The loop opens by forcing the retrieval hierarchy — perspectives, then
// observations, then raw memories — so the model cannot answer from raw facts,
// or from nothing, while a cheaper layer is untried. A fresh, usable
// perspective releases the remaining forced steps, and the model then decides
// each turn which tool to call. The loop stops when the model calls the done
// tool, when the iteration bound is reached, or when the evidence budget is
// spent. A done call carries the answer and the run ends on it; a stop without
// one is answered by synthesis over the evidence actually gathered — never from
// nothing.
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
		l.turns = append(l.turns, turn)
		outcome.Usage = addUsage(outcome.Usage, turnUsage(turn))
		if finish {
			break
		}
		if l.evidenceTokens() >= l.cfg.ContextBudget {
			break
		}
	}
	outcome.Evidence = append([]Evidence(nil), l.evidence...)
	// In document mode the `done` call already carried the answer as a
	// structure, so there is nothing to compose or extend: reading markdown
	// back out of free text would be exactly the round trip this mode exists
	// to avoid.
	if l.cfg.AnswerAsDocument && strings.TrimSpace(l.documentAnswer) != "" {
		outcome.Answer = l.aliases.scrubUnverifiedIDs(l.aliases.resolve(strings.TrimSpace(l.documentAnswer)))
		return outcome, nil
	}
	// The `done` call's own answer is the answer: the model wrote it, and the
	// reference trusts it verbatim rather than spending a second call
	// restating what the loop already has. It still goes through the same
	// length rewrite and structured extraction every answer does. Synthesis is
	// the fallback for a loop that stopped without a done answer — on the
	// iteration bound, the evidence budget, or a text-only turn.
	answer := strings.TrimSpace(l.doneAnswer)
	if answer == "" {
		composed, err := l.compose(ctx, &outcome)
		if err != nil {
			return outcome, err
		}
		answer = composed
	} else {
		finalized, err := l.finalize(ctx, &outcome, answer)
		if err != nil {
			return outcome, err
		}
		answer = finalized
	}
	answer = l.aliases.resolve(answer)
	answer = l.aliases.scrubUnverifiedIDs(answer)
	outcome.Answer = answer
	return outcome, nil
}

// ErrNoToolCall reports a reflection whose model never produced a usable tool
// call. The loop is driven by tool calls, so a model that answers in prose on
// its first turn was pointed at the wrong endpoint, not asked about an empty
// bank.
var ErrNoToolCall = errors.New("reflection requires a tool-calling model")

// noToolCallError describes a text-only first turn: which provider answered,
// which tool it was asked for, how it stopped, and what it said instead. All
// four are needed to act on the failure — the diagnosis is a configuration
// mismatch, and the response text is what says whether the endpoint ignored the
// tool choice or does not support tools at all.
func (l *Loop) noToolCallError(iteration int, response provider.CompletionResult) error {
	snippet := strings.TrimSpace(response.Content)
	if len(snippet) > 500 {
		snippet = snippet[:500] + "..."
	}
	detail := " The model returned no content."
	if snippet != "" {
		detail = fmt.Sprintf(" Response: %q", snippet)
	}
	return fmt.Errorf("%w, but %s/%s produced no usable tool call during initial tool selection "+
		"(iteration=%d, tool_choice=%s, finish_reason=%s). Check tool-choice handling for this prompt on the "+
		"configured endpoint; a successful tool call for another prompt does not establish compatibility.%s",
		ErrNoToolCall, l.provider.Name(), response.Model, iteration, choiceLabel(l.toolChoice()), response.FinishReason, detail)
}

// choiceLabel names the tool selection a turn was pinned to the way the
// provider reports it: the requested tool when one was named, otherwise the
// mode.
func choiceLabel(choice *provider.ToolSelectionPolicy) string {
	if choice == nil {
		return "auto"
	}
	if choice.Name != "" {
		return choice.Name
	}
	return choice.Mode
}

// step runs one turn: ask the model what to do, then do it.
func (l *Loop) step(ctx context.Context, iteration int) (Turn, bool, error) {
	turn := Turn{Iteration: iteration}
	response, err := l.provider.Complete(ctx, provider.CompletionRequest{
		Messages:   l.messages(ctx),
		Tools:      l.offeredTools(),
		ToolChoice: l.toolChoice(),
		Scope:      "reflect",
	})
	if err != nil {
		return turn, false, fmt.Errorf("reflection turn %d failed: %w", iteration, err)
	}
	turn.Usage = response.Usage

	// A model that answered in prose has answered; there is no tool to run.
	//
	// Before any tool call at all, though, prose is not an answer: the loop
	// cannot know whether the bank holds nothing or the endpoint simply
	// ignored the prompt's tool choice. Reporting the second as the first is
	// the one degradation worth refusing, because a caller acting on memory
	// cannot tell the two apart.
	if len(response.ToolCalls) == 0 {
		if !l.sawToolCall {
			return turn, false, l.noToolCallError(iteration, response)
		}
		if strings.TrimSpace(response.Content) != "" {
			l.evidence = append(l.evidence, Evidence{Kind: "answer", Text: response.Content})
		}
		return turn, true, nil
	}
	l.sawToolCall = true

	// The forced layer this turn is pinned to, read before the calls are parsed
	// so it names the layer the provider was actually asked to run.
	forcedLayer := ""
	if l.forcedStepPending() {
		forcedLayer = l.forcedSequence[l.forcedStepsDone]
	}

	finish := false
	honouredForced := false
	calls := make([]ToolCall, 0, len(response.ToolCalls))
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
		toolCall := ToolCall{ID: call.ID, Name: NormalizeToolName(call.Name), Input: l.resolveAliases(input)}
		turn.Calls = append(turn.Calls, toolCall)
		if toolCall.Name == forcedLayer {
			honouredForced = true
		}
		if toolCall.Name == ToolDone {
			// The evidence guardrail: a model that answers before gathering
			// anything is answered from nothing, so the first done with an
			// empty evidence set is ignored and the loop keeps going. The
			// done tool retrieves nothing; it only ends the loop.
			if len(l.evidence) == 0 {
				continue
			}
			if l.cfg.AnswerAsDocument {
				answer, err := doneDocumentAnswer(toolCall.Input)
				if err != nil {
					// The document did not match the declared shape. Hand the
					// reason back and re-ask, rather than storing something
					// nobody can trust; out of attempts, the run fails.
					l.documentRejections++
					if l.documentRejections > maxDocumentRejections {
						return turn, false, err
					}
					l.documentFeedback = append(l.documentFeedback, err.Error())
					continue
				}
				l.documentAnswer = answer
			} else if answer := strings.TrimSpace(stringArg(toolCall.Input, "answer")); answer != "" {
				// A prose-mode done call carries the answer itself, and the
				// model wrote it: the loop ends on that answer rather than
				// paying for a synthesis call that would restate it. A done
				// carrying nothing usable is not an answer, so the run stops
				// without one and synthesis covers that stop.
				l.doneAnswer = answer
			}
			finish = true
			continue
		}
		calls = append(calls, toolCall)
	}
	// The descent advances only when the model actually ran the pinned layer.
	// A provider that ignores or downgrades tool_choice returns some other
	// tool, and advancing anyway would silently abandon the remaining forced
	// layers, letting the model answer from raw facts — or from nothing —
	// while a cheaper layer is untried. Not advancing re-pins the same layer
	// on the next turn, so ignoring the pin retries it rather than skipping it.
	if honouredForced {
		l.forcedStepsDone++
	}
	// The retrieval calls a turn asked for are independent of each other, so
	// they run together rather than one after another. The results are still
	// recorded in the order the model asked for them, so a later turn's context
	// does not depend on which lookup answered first.
	results, err := l.executeAll(ctx, calls)
	if err != nil {
		// A failed retrieval fails the run rather than silently yielding
		// "nothing found", which would be indistinguishable from an empty bank.
		return turn, false, err
	}
	for index, batch := range results {
		// A search returns the best-ranked page whole and the rest as
		// snippets, so the prompt shows the model exactly what the tool result
		// showed it — a snippet it has not chosen to read is not the page.
		if calls[index].Name == ToolSearchMentalModels {
			markSearchSnippets(batch)
		}
		l.record(batch)
		turn.Results = append(turn.Results, batch...)
		turn.Batches = append(turn.Batches, batch)
		// A fresh, usable perspective hands control back to the model: the
		// forced descent stops here, and the model is told why on the result it
		// is about.
		if calls[index].Name == ToolSearchMentalModels && l.releaseForcing(batch) {
			l.freshGuidance = freshPagesGuidance
		}
	}
	return turn, finish, nil
}

// maxParallelToolCalls bounds how many retrievals one turn runs at once, so a
// model that asks for twenty lookups does not open twenty database
// transactions against a single request.
const maxParallelToolCalls = 4

// executeAll runs a turn's tool calls, up to maxParallelToolCalls at a time, and
// returns one result set per call in call order.
func (l *Loop) executeAll(ctx context.Context, calls []ToolCall) ([][]Evidence, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if len(calls) == 1 {
		results, err := l.execute(ctx, calls[0])
		if err != nil {
			return nil, err
		}
		return [][]Evidence{results}, nil
	}
	type outcome struct {
		results []Evidence
		err     error
	}
	limit := maxParallelToolCalls
	if len(calls) < limit {
		limit = len(calls)
	}
	sem := make(chan struct{}, limit)
	outcomes := make([]outcome, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(index int, toolCall ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results, err := l.execute(ctx, toolCall)
			outcomes[index] = outcome{results: results, err: err}
		}(i, call)
	}
	wg.Wait()
	batches := make([][]Evidence, 0, len(calls))
	for _, result := range outcomes {
		if result.err != nil {
			return nil, result.err
		}
		batches = append(batches, result.results)
	}
	return batches, nil
}

// observationToolTokens is the budget the search_observations tool applies when
// the model names none: the caller's resolved value, else the shipped default.
func (l *Loop) observationToolTokens() int {
	if l.cfg.ObservationMaxTokens > 0 {
		return l.cfg.ObservationMaxTokens
	}
	return DefaultObservationToolTokens
}

// execute dispatches one tool call to the retriever.
func (l *Loop) execute(ctx context.Context, call ToolCall) ([]Evidence, error) {
	switch call.Name {
	case ToolSearchMentalModels:
		return l.retrieval.ReflectMentalModels(ctx, stringArg(call.Input, "query"), clampInt(intArg(call.Input, "max_results", 5), 1, 50))
	case ToolSearchObservations:
		return l.retrieval.ReflectObservations(ctx, stringArg(call.Input, "query"),
			clampInt(intArg(call.Input, "max_tokens", l.observationToolTokens()), 1000, 16000))
	case ToolRecall:
		return l.retrieval.ReflectMemories(ctx, stringArg(call.Input, "query"), clampInt(intArg(call.Input, "max_tokens", 2048), 1000, 16000))
	case ToolReadMentalModels:
		ids := stringListArg(call.Input, "mental_model_ids")
		if len(ids) == 0 {
			ids = stringListArg(call.Input, "ids")
		}
		return l.retrieval.ReflectReadModels(ctx, ids)
	case ToolExpand:
		ids := stringListArg(call.Input, "memory_ids")
		if len(ids) == 0 {
			ids = stringListArg(call.Input, "ids")
		}
		return l.retrieval.ReflectExpand(ctx, ids, stringArg(call.Input, "depth"))
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
}

// resolveAliases rewrites the alias tokens in a tool call's arguments back to
// the identifiers they stand for, so a model that cites "o3" still retrieves
// the observation. Only string values and string lists carry identifiers.
func (l *Loop) resolveAliases(input map[string]any) map[string]any {
	if len(input) == 0 || len(l.aliases.byID) == 0 {
		return input
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case string:
			out[key] = l.aliases.resolve(typed)
		case []any:
			items := make([]any, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok {
					items = append(items, l.aliases.resolve(text))
					continue
				}
				items = append(items, item)
			}
			out[key] = items
		default:
			out[key] = value
		}
	}
	return out
}

// clampInt bounds a model-supplied integer into the range the tool accepts, so
// a model asking for a million tokens cannot blow up the context.
func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// record adds newly retrieved evidence, skipping anything already gathered so a
// repeated call does not spend the budget twice on the same content.
//
// A page that arrived as a snippet and comes back in full is an exception: the
// read is the whole point of the snippet/full split, so the stored item is
// upgraded rather than skipped, and the prompt then shows the page the model
// asked for.
func (l *Loop) record(results []Evidence) {
	for _, item := range results {
		key := item.Kind + "\x00" + item.ID
		if index, ok := l.seen[key]; ok {
			if l.evidence[index].SnippetOnly && !item.SnippetOnly {
				l.evidence[index] = item
			}
			continue
		}
		l.seen[key] = len(l.evidence)
		l.evidence = append(l.evidence, item)
	}
}

// markSearchSnippets marks every result after the first as a snippet. A search
// returns the best-ranked page in full and the rest truncated, so anything past
// the first is presented to the model as the snippet it received.
func markSearchSnippets(batch []Evidence) {
	for i := 1; i < len(batch); i++ {
		batch[i].SnippetOnly = true
	}
}

// messages builds the conversation the model sees this turn. The loop serves a
// request, so the directive reads run on its context: a client disconnect or
// read timeout cancels them like any other storage call.
func (l *Loop) messages(ctx context.Context) []provider.Message {
	directives := l.retrieval.Directives(ctx)
	messages := []provider.Message{{Role: provider.RoleSystem, Content: buildSystemPromptForTools(systemPromptOptions{
		Budget:              l.cfg.Budget,
		Directives:          directives,
		HasMentalModels:     true,
		IncludeObservations: true,
		Profile:             l.profile(),
	})}}
	messages = append(messages, provider.Message{Role: "user", Content: l.cfg.Question})
	// Each retrieval turn is replayed as the assistant tool call it was and the
	// tool results it produced, so the model sees its own requests and what came
	// back. A loop that only ever saw a summarized evidence blob could not tell
	// that a tool had already run, which is exactly what a "call a tool once,
	// then finish" turn keys on — and a call whose source text was withheld must
	// look withheld, not absent.
	for _, turn := range l.turns {
		if len(turn.Calls) == 0 {
			continue
		}
		calls := make([]provider.ToolCall, 0, len(turn.Calls))
		ids := make([]string, 0, len(turn.Calls))
		for index, call := range turn.Calls {
			arguments, err := json.Marshal(call.Input)
			if err != nil || len(arguments) == 0 {
				arguments = json.RawMessage("{}")
			}
			id := call.ID
			if id == "" {
				// A provider that omits the call id still needs one so the
				// result can be keyed back to its call.
				id = fmt.Sprintf("call_%d_%d", turn.Iteration, index)
			}
			calls = append(calls, provider.ToolCall{
				ID: id, Name: call.Name, Arguments: json.RawMessage(l.aliasToolText(string(arguments), nil)),
			})
			ids = append(ids, id)
		}
		messages = append(messages, provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
		for index, call := range turn.Calls {
			var batch []Evidence
			if index < len(turn.Batches) {
				batch = turn.Batches[index]
			}
			kindByID := map[string]string{}
			for _, item := range batch {
				if item.ID != "" {
					kindByID[item.ID] = item.Kind
				}
				if item.ChunkID != "" {
					kindByID[item.ChunkID] = "chunk"
				}
			}
			content, err := json.Marshal(toolOutput(call, batch))
			if err != nil {
				content = []byte("{}")
			}
			messages = append(messages, provider.Message{
				Role: provider.RoleTool, ToolCallID: ids[index], Content: l.aliasToolText(string(content), kindByID),
			})
		}
	}
	if len(l.evidence) > 0 {
		content := "Evidence gathered so far:\n" + l.renderEvidence()
		// The guidance rides with the evidence it is about, once the loop has
		// released the forced descent, so the model knows why it is free to
		// answer (or to descend on its own).
		if l.freshGuidance != "" {
			content += "\n" + l.freshGuidance
		}
		messages = append(messages, provider.Message{Role: "user", Content: content})
	}
	// A refused document rides back with the reason it was refused, so the
	// re-ask fixes the shape instead of repeating it.
	if len(l.documentFeedback) > 0 {
		messages = append(messages, provider.Message{
			Role:    "user",
			Content: documentRejectionPrompt + "\nFixing: " + strings.Join(l.documentFeedback, "; "),
		})
	}
	return messages
}

// renderEvidence renders the gathered evidence for the model, bounded so the
// conversation cannot grow past what the provider accepts. Each item is written
// under its short alias, so a full identifier is never re-sent on every turn.
func (l *Loop) renderEvidence() string {
	var b strings.Builder
	for _, item := range l.evidence {
		line := l.renderEvidenceLine(item)
		if b.Len()+len(line) > l.cfg.ContextBudget {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// aliasToolText rewrites every identifier in a replayed tool message to its
// alias, registering any it has not named yet under the kind the batch's own
// evidence gives it (a bare id defaults to a fact). A tool call the model made
// is replayed under the aliases it was shown, so a full identifier never
// re-enters the prompt through the conversation.
func (l *Loop) aliasToolText(text string, kindByID map[string]string) string {
	if text == "" || !uuidPattern.MatchString(text) {
		return text
	}
	return uuidPattern.ReplaceAllStringFunc(text, func(id string) string {
		kind := kindByID[id]
		if kind == "" {
			kind = "memory"
		}
		return l.aliases.aliasFor(kind, id)
	})
}

// compose asks the model for the final answer from the gathered evidence.
//
// It is a separate call with its own system prompt: the reasoning turns carry
// retrieval instructions and a tool list, neither of which belongs on the call
// that writes prose. The evidence is rendered under the aliases the reasoning
// turns used, so a citation the model writes reads the same on both sides.
//
// It is the fallback, not the answer path: a loop that stopped on a `done` call
// already has its answer and never reaches here.
func (l *Loop) compose(ctx context.Context, outcome *Outcome) (string, error) {
	messages := []provider.Message{
		{Role: "system", Content: buildFinalSystemPrompt(l.retrieval.Directives(ctx), l.profile())},
		{Role: "user", Content: buildFinalPrompt(l.cfg.Question, l.renderEvidence(), l.cfg.MaxTokens)},
	}
	response, err := l.provider.Complete(ctx, provider.CompletionRequest{
		Messages:  messages,
		Scope:     "reflect",
		MaxTokens: l.synthesisMaxTokens(),
	})
	outcome.Usage = addUsage(outcome.Usage, response.Usage)
	answer := strings.TrimSpace(response.Content)
	if answer == "" {
		if err != nil {
			return "", fmt.Errorf("reflection answer failed: %w", err)
		}
		// The answering call asked for another tool instead of answering, so
		// there is no answer to return. Reporting that is better than handing
		// the caller an empty string that reads as "the answer is nothing".
		return "", ErrNoAnswer
	}
	// A generation that stopped at its own output limit still wrote text, and
	// the reference uses it: failing the run here is what turned a small
	// max_tokens into a 500. The truncated answer is a real answer, so it is
	// finalized like any other; only a failure that carried no text is fatal.
	if err != nil && !provider.IsOutputTooLong(err) {
		return "", fmt.Errorf("reflection answer failed: %w", err)
	}
	return l.finalize(ctx, outcome, answer)
}

// finalize applies the post-answer work every answer source shares: the
// visible-length rewrite, then the structured extraction when the request asked
// for one. The `done` path and the synthesis fallback both end here, so the two
// cannot drift apart.
func (l *Loop) finalize(ctx context.Context, outcome *Outcome, answer string) (string, error) {
	answer, err := l.rewriteToLength(ctx, outcome, answer)
	if err != nil {
		return "", err
	}
	if err := l.extractStructured(ctx, outcome, answer); err != nil {
		return "", err
	}
	return answer, nil
}

// synthesisMaxTokens is the transport cap for the final synthesis call: the
// deployment's reflect cap when one is set, else unset.
//
// It is deliberately *not* the caller's max_tokens. That value is a target for
// the visible answer length, enforced by the rewrite once the answer exists;
// sending it as the provider's output cap lets a thinking model spend it on
// reasoning tokens and truncate the answer mid-word — the reference decouples
// the two for exactly that reason, leaving the transport level unset by
// default.
func (l *Loop) synthesisMaxTokens() int {
	if l.cfg.MaxCompletionTokens > 0 {
		return l.cfg.MaxCompletionTokens
	}
	return 0
}

// extractStructured runs the second call that reshapes the prose answer into
// the caller's schema, when the request asked for one.
//
// It is a *second* call on purpose: the schema does not constrain the answering
// turn, so the prose is written first and then read back into the shape the
// caller wants. The prose is unaffected by asking for structure, and a request
// without a schema pays for no extraction at all.
//
// A failure is reported on the outcome rather than returned: an answer that
// could not be reshaped is still an answer, and a caller that asked for both
// needs to be told which half is missing.
func (l *Loop) extractStructured(ctx context.Context, outcome *Outcome, answer string) error {
	if len(l.cfg.ResponseSchema) == 0 {
		return nil
	}
	prompt, err := buildStructuredExtractionPrompt(answer, l.cfg.ResponseSchema)
	if err != nil {
		outcome.StructuredOutputError = err.Error()
		return nil
	}
	schema, err := schemaFromMap(l.cfg.ResponseSchema)
	if err != nil {
		outcome.StructuredOutputError = err.Error()
		return nil
	}
	response, err := l.provider.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: structuredExtractionSystemPrompt},
			{Role: provider.RoleUser, Content: prompt},
		},
		ResponseSchema: schema,
		// The extraction is read back by hand: a schema violation is reported
		// to the caller as a failed extraction, not raised as a provider
		// failure that loses the answer that was already written.
		SkipValidation: true,
		Scope:          "reflect_structured",
		MaxTokens:      l.cfg.MaxTokens,
		Temperature:    deterministicTemperature(),
	})
	outcome.Usage = addUsage(outcome.Usage, response.Usage)
	if err != nil {
		outcome.StructuredOutputError = err.Error()
		return nil
	}
	parsed, ok := response.Structured.(map[string]any)
	if !ok || parsed == nil {
		outcome.StructuredOutputError = "the extraction call returned no JSON object"
		return nil
	}
	outcome.StructuredOutput = parsed
	return nil
}

// deterministicTemperature pins the extraction call: reading values out of a
// finished answer is a clerical pass, and sampling would only make it vary.
func deterministicTemperature() *float64 {
	value := 0.0
	return &value
}

// rewriteSystem asks for a shortened answer.
const rewriteSystem = `Rewrite the user's text so it fits within the requested token budget.
Preserve the key facts and structure; drop lower-priority detail.
Respond with the rewritten text only, no preamble.`

// rewriteToLength shortens an answer that overruns the caller's visible-length
// budget.
//
// The budget is a target for the *visible* answer, not a provider cap: on a
// thinking model a hard cap is eaten by reasoning tokens and truncates the
// answer mid-word, so it is enforced here, once the answer exists. An empty
// rewrite never empties the answer — a model hiccup must not throw away a
// complete synthesis — and every attempt is billed, so its usage is added.
func (l *Loop) rewriteToLength(ctx context.Context, outcome *Outcome, answer string) (string, error) {
	if l.cfg.MaxTokens <= 0 || storage.CountTokens(answer) <= l.cfg.MaxTokens {
		return answer, nil
	}
	response, err := l.provider.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: rewriteSystem},
			{Role: provider.RoleUser, Content: fmt.Sprintf("Target budget: %d tokens.\n\nText to rewrite:\n%s", l.cfg.MaxTokens, answer)},
		},
		Scope: "reflect",
		// The rewrite's cost is bounded by the deployment's completion cap, not
		// by the visible-length target: the same knob the final synthesis call
		// uses, and unset (zero) by default, which is what the reference does.
		MaxTokens: l.cfg.MaxCompletionTokens,
	})
	if err != nil && !provider.IsOutputTooLong(err) {
		return "", fmt.Errorf("reflection length rewrite failed: %w", err)
	}
	outcome.Usage = addUsage(outcome.Usage, response.Usage)
	// A rewrite that stopped at its output limit still returned its shortened
	// text, and the reference uses it; a rewrite that produced nothing keeps
	// the original answer rather than emptying it, which is also what the
	// reference does.
	if rewritten := strings.TrimSpace(response.Content); rewritten != "" {
		return rewritten, nil
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

// turnUsage reports the usage the turn's model call was billed, so the run's
// total includes every retrieval turn rather than only the closing calls.
func turnUsage(turn Turn) provider.Usage { return turn.Usage }

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

// toolNamed pins the model to one tool.
func toolNamed(name string) *provider.ToolSelectionPolicy {
	return &provider.ToolSelectionPolicy{Mode: "required", Name: name}
}

// toolChoice constrains what the model may call this turn: the next layer of
// the retrieval hierarchy while a forced step is still owed, otherwise the
// model's own choice.
func (l *Loop) toolChoice() *provider.ToolSelectionPolicy {
	if l.forcedStepPending() {
		return toolNamed(l.forcedSequence[l.forcedStepsDone])
	}
	return toolAuto()
}

// offeredTools returns the tool definitions a turn presents. During the forced
// descent only the pinned rung is offered, matching the reference: a model that
// ignores tool_choice should not be able to advance the ladder by calling a
// different tool, and `done` must stay out of reach until every cheaper layer
// has been tried. Once forcing is released the full set, `done` included, is
// offered.
func (l *Loop) offeredTools() []provider.ToolDefinition {
	if l.forcedStepPending() {
		return []provider.ToolDefinition{ToolDefinition(l.forcedSequence[l.forcedStepsDone])}
	}
	return ToolDefinitions(l.cfg.AnswerAsDocument)
}

// forcedStepPending reports whether a forced retrieval layer is still owed.
func (l *Loop) forcedStepPending() bool {
	return !l.forcingReleased && l.forcedStepsDone < len(l.forcedSequence)
}

// freshPagesGuidance tells the model why the forced descent stopped, appended
// to the result it is about.
const freshPagesGuidance = "These pages are fresh. If they answer the question, call done now and answer from them alone; " +
	"search further only for a specific detail they lack."

// releaseForcing reports whether a search_mental_models result is trustworthy
// enough to stop forcing the lower layers: the caller must not be on the high
// budget, which always wants the full descent, and every returned page must be
// explicitly fresh and carry usable content. Stale, empty, or missing pages
// keep the forcing.
func (l *Loop) releaseForcing(batch []Evidence) bool {
	if l.forcingReleased || l.cfg.Budget == storage.ReflectBudgetHigh {
		return false
	}
	if len(batch) == 0 {
		return false
	}
	for _, item := range batch {
		if !item.Fresh || strings.TrimSpace(item.Text) == "" {
			return false
		}
	}
	l.forcingReleased = true
	return true
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

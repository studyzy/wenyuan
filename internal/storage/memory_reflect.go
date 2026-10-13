package storage

import (
	"context"
	"sort"
	"strings"
)

// Reflection loop internals. The loop layers retrieval from a synthesized
// perspective down to raw facts, stops early once accumulated evidence would
// exceed the model context budget, and forces a final synthesis at the
// iteration bound.

// reflectMaxIterations bounds the retrieval loop turns.
const reflectMaxIterations = 4

// reflectContextBudget approximates the model context budget in tokens; once
// accumulated evidence would cross it the loop forces synthesis.
const reflectContextBudget = 4000

// reflectAllToolNames lists every retrieval tool in stable order. A filter or
// exclusion removes the matching entry so it is never invoked.
var reflectAllToolNames = []string{"search_facts", "search_mental_models", "search_observations"}

// Reflect runs the in-process reflection loop.
func (s *MemoryStore) Reflect(ctx context.Context, query ReflectQuery) (ReflectOutcome, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return ReflectOutcome{}, err
	}
	if _, ok := s.banks[query.BankID]; !ok {
		return ReflectOutcome{}, ErrBankNotFound
	}
	if s.readErr != nil {
		return ReflectOutcome{}, s.readErr
	}

	if query.MentalModels == nil {
		query.MentalModels = append([]ReflectMentalModel(nil), s.mentalModels[query.BankID]...)
	}
	if query.Directives == nil {
		query.Directives = append([]ReflectDirective(nil), s.directives[query.BankID]...)
	}
	// A supplied runner takes precedence: it is the model-driven loop, and the
	// deterministic in-process loop is the fallback for a deployment with no
	// model configured.
	if query.Reflection != nil {
		directives := make([]string, 0, len(query.Directives))
		for _, directive := range query.Directives {
			if directive.Content != "" {
				directives = append(directives, directive.Content)
			}
		}
		return query.Reflection.Run(ctx, AgenticReflectRequest{
			BankID:        query.BankID,
			Question:      query.Query,
			MaxTokens:     query.MaxTokens,
			MaxIterations: query.MaxIterations,
			Retrieval:     s,
			Directives:    directives,
		})
	}
	loop := newReflectLoop(query, s.rawFacts(query.BankID))
	return loop.run(ctx)
}

// SeedMentalModel injects a synthesized perspective for tests.
func (s *MemoryStore) SeedMentalModel(bankID string, model ReflectMentalModel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mentalModels[bankID] = append(s.mentalModels[bankID], model)
}

// SeedDirective injects a directive for tests.
func (s *MemoryStore) SeedDirective(bankID string, directive ReflectDirective) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directives[bankID] = append(s.directives[bankID], directive)
}

// MentalModels returns a bank's seeded perspectives.
func (s *MemoryStore) MentalModels(bankID string) []ReflectMentalModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ReflectMentalModel(nil), s.mentalModels[bankID]...)
}

// Directives returns a bank's seeded directives.
func (s *MemoryStore) Directives(bankID string) []ReflectDirective {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ReflectDirective(nil), s.directives[bankID]...)
}

// rawFacts returns a copy of a bank's stored matches as reflection citations.
func (s *MemoryStore) rawFacts(bankID string) []ReflectCitation {
	matches := s.memories[bankID]
	out := make([]ReflectCitation, 0, len(matches))
	for _, m := range matches {
		out = append(out, ReflectCitation{
			ID:            m.ID,
			Text:          m.Text,
			Type:          m.Type,
			Context:       m.Context,
			OccurredStart: m.OccurredStart,
			OccurredEnd:   m.OccurredEnd,
			MentionedAt:   m.MentionedAt,
			DocumentID:    m.DocumentID,
			ChunkID:       m.ChunkID,
			Metadata:      m.Metadata,
			Tags:          m.Tags,
			Attachments:   m.Attachments,
		})
	}
	return out
}

// reflectLoop carries the working state of one reflection.
type reflectLoop struct {
	query        ReflectQuery
	facts        []ReflectCitation
	models       []ReflectMentalModel
	directives   []ReflectDirectiveCitation
	available    []string
	toolCalls    []ReflectToolCall
	modelCalls   []ReflectModelCall
	callIDs      map[string]int
	iteration    int
	evidenceCost int
}

// newReflectLoop builds the loop state, applying the tool removals a filter or
// exclusion imposes and resolving the directives that apply.
func newReflectLoop(query ReflectQuery, facts []ReflectCitation) *reflectLoop {
	loop := &reflectLoop{query: query, facts: facts}
	loop.available = availableReflectTools(query)
	loop.models = selectMentalModels(query)
	loop.directives = selectDirectives(query)
	return loop
}

// availableReflectTools returns the retrieval tools the loop may invoke. A
// fact-type filter that selects only observations, or an observation-only
// request, removes the raw-fact tool; a mental-model exclusion removes the
// perspective tool. A removed tool is never invoked.
func availableReflectTools(query ReflectQuery) []string {
	removed := map[string]bool{}
	if observationsOnly(query.Types) {
		removed["search_facts"] = true
	}
	if query.ExcludeMentalModels || len(query.ExcludeMentalModelIDs) > 0 {
		removed["search_mental_models"] = true
	}
	out := make([]string, 0, len(reflectAllToolNames))
	for _, name := range reflectAllToolNames {
		if !removed[name] {
			out = append(out, name)
		}
	}
	return out
}

// observationsOnly reports whether a fact-type selection admits only
// observations, which removes the raw-fact retrieval tool.
func observationsOnly(types []string) bool {
	if len(types) == 0 {
		return false
	}
	for _, t := range types {
		if t != string(TypeObservation) {
			return false
		}
	}
	return true
}

// selectMentalModels returns the perspectives the loop may use, honoring the
// blanket and identifier exclusions.
func selectMentalModels(query ReflectQuery) []ReflectMentalModel {
	if query.ExcludeMentalModels {
		return nil
	}
	excluded := map[string]bool{}
	for _, id := range query.ExcludeMentalModelIDs {
		excluded[id] = true
	}
	out := make([]ReflectMentalModel, 0, len(query.MentalModels))
	for _, m := range query.MentalModels {
		if excluded[m.ID] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// selectDirectives returns the directives that apply to this reflection. An
// untagged directive always applies; a tagged directive applies only on a tag
// match; the apply-all flag applies every active directive regardless of tag
// scope. A directive whose scope is non-empty applies only when the request
// tags carry that scope; an empty scope is global.
func selectDirectives(query ReflectQuery) []ReflectDirectiveCitation {
	requestTags := map[string]bool{}
	for _, t := range query.Tags {
		requestTags[t] = true
	}
	out := make([]ReflectDirectiveCitation, 0)
	for _, d := range query.Directives {
		if !d.Active {
			continue
		}
		if !directiveApplies(d, requestTags, query.ApplyAllDirectives) {
			continue
		}
		out = append(out, ReflectDirectiveCitation{ID: d.ID, Name: d.Name, Content: d.Content})
	}
	return out
}

// directiveApplies reports whether one directive applies under the request's
// tag scope, honoring the apply-all override and the execution scope.
func directiveApplies(d ReflectDirective, requestTags map[string]bool, applyAll bool) bool {
	if d.Scope != "" && !requestTags[d.Scope] {
		return false
	}
	if applyAll || len(d.Tags) == 0 {
		return true
	}
	for _, tag := range d.Tags {
		if requestTags[tag] {
			return true
		}
	}
	return false
}

// run executes the bounded loop and returns the outcome.
func (l *reflectLoop) run(ctx context.Context) (ReflectOutcome, error) {
	for l.iteration = 1; l.iteration <= reflectMaxIterations; l.iteration++ {
		if err := ctx.Err(); err != nil {
			return ReflectOutcome{}, err
		}
		if l.evidenceCost >= reflectContextBudget {
			break
		}
		l.runTurn()
	}
	answer := l.synthesize()
	if strings.TrimSpace(answer) == "" {
		return ReflectOutcome{}, ErrReflectNoAnswer
	}
	return l.outcome(answer), nil
}

// runTurn performs one retrieval turn: a fresh covering perspective satisfies
// the loop without descending to raw facts, while a stale or non-covering
// perspective forces deeper evidence.
func (l *reflectLoop) runTurn() {
	iteration := l.iteration
	if model, ok := l.freshMentalModel(); ok {
		l.recordModelCall("mental_model")
		l.recordToolCall("search_mental_models", map[string]any{"query": l.query.Query},
			map[string]any{"id": model.ID, "text": model.Text}, true, iteration)
		l.evidenceCost += reflectTokenCount(model.Text)
		return
	}
	// Descend to raw facts.
	if containsString(l.available, "search_facts") {
		l.recordModelCall("facts")
		l.recordToolCall("search_facts", map[string]any{"query": l.query.Query},
			map[string]any{"count": len(l.facts)}, len(l.facts) > 0, iteration)
		for _, fact := range l.facts {
			l.evidenceCost += reflectTokenCount(fact.Text)
		}
	}
	if containsString(l.available, "search_observations") {
		l.recordModelCall("observations")
		l.recordToolCall("search_observations",
			map[string]any{"query": l.query.Query, "max_tokens": l.query.SearchObservationsMaxTokens},
			map[string]any{"include_entities": l.query.SearchObservationsIncludeEntities}, true, iteration)
	}
}

// recordToolCall appends one trace tool call, normalizing the tool name before
// dispatch so an equivalent provider-prefixed name resolves to the same tool.
// Call identifiers are made unique within the loop so each call keeps its own
// result; the recorded order follows the model's call order.
func (l *reflectLoop) recordToolCall(name string, input, output map[string]any, hasOutput bool, iteration int) {
	if l.callIDs == nil {
		l.callIDs = map[string]int{}
	}
	normalized := normalizeToolName(name)
	id := uniqueCallID(normalized+":"+itoaSmall(iteration), l.callIDs)
	l.toolCalls = append(l.toolCalls, ReflectToolCall{
		Tool:      normalized,
		Input:     input,
		Output:    output,
		HasOutput: hasOutput,
		Iteration: iteration,
		ID:        id,
	})
}

// freshMentalModel returns a non-stale perspective that covers the question, if
// one exists.
func (l *reflectLoop) freshMentalModel() (ReflectMentalModel, bool) {
	for _, m := range l.models {
		if m.Stale {
			continue
		}
		if coversQuestion(m.Text, l.query.Query) {
			return m, true
		}
	}
	return ReflectMentalModel{}, false
}

// coversQuestion reports whether a perspective's text shares a word with the
// question; an empty question is covered by any non-empty perspective.
func coversQuestion(text, query string) bool {
	if strings.TrimSpace(query) == "" {
		return strings.TrimSpace(text) != ""
	}
	want := strings.Fields(strings.ToLower(query))
	have := strings.ToLower(text)
	for _, w := range want {
		if strings.Contains(have, w) {
			return true
		}
	}
	return false
}

// recordModelCall appends one trace model call.
func (l *reflectLoop) recordModelCall(scope string) {
	l.modelCalls = append(l.modelCalls, ReflectModelCall{Scope: scope, DurationMs: 0})
}

// synthesize composes the markdown answer from the evidence the loop gathered.
// A covering perspective is answered from directly; otherwise the answer cites
// the retrieved facts.
func (l *reflectLoop) synthesize() string {
	if len(l.models) > 0 {
		for _, m := range l.models {
			if !m.Stale {
				return renderAnswer(l.query.Query, m.Text, l.facts)
			}
		}
	}
	if len(l.facts) == 0 {
		return "No stored memories matched the question."
	}
	return renderAnswer(l.query.Query, "", l.facts)
}

// renderAnswer renders the answer markdown from a perspective and the raw
// facts, resolving citation identifiers to their real values.
func renderAnswer(query, perspective string, facts []ReflectCitation) string {
	var b strings.Builder
	if strings.TrimSpace(perspective) != "" {
		b.WriteString(perspective)
	}
	if len(facts) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Based on: ")
		ids := make([]string, 0, len(facts))
		for _, f := range facts {
			ids = append(ids, f.ID)
		}
		sort.Strings(ids)
		b.WriteString(strings.Join(ids, ", "))
	}
	if b.Len() == 0 {
		b.WriteString("No stored memories matched the question.")
	}
	return shorten(b.String(), 0)
}

// shorten applies a post-generation length rewrite. The budget is a soft
// directive: an over-length answer is truncated; a zero budget leaves it whole.
func shorten(answer string, maxTokens int) string {
	if maxTokens <= 0 {
		return answer
	}
	if reflectTokenCount(answer) <= maxTokens {
		return answer
	}
	words := strings.Fields(answer)
	if maxTokens >= len(words) {
		return answer
	}
	return strings.Join(words[:maxTokens], " ")
}

// outcome assembles the storage-level result, applying the visible-length
// rewrite when a length budget was requested. A rewrite that yields nothing
// leaves the original answer standing.
func (l *reflectLoop) outcome(answer string) ReflectOutcome {
	out := ReflectOutcome{
		Answer:         applyLengthRewrite(answer, l.query.MaxTokens),
		RawAnswer:      answer,
		Usage:          l.usage(answer),
		AvailableTools: append([]string(nil), l.available...),
		Directives:     l.directives,
	}
	if l.query.Include.Facts != nil {
		out.Facts = l.citedFacts()
		out.MentalModels = l.citedMentalModels()
		out.Directives = l.directives
	}
	if l.query.Include.ToolCalls {
		out.Trace = &ReflectTrace{
			ToolCalls: append([]ReflectToolCall(nil), l.toolCalls...),
			LLMCalls:  append([]ReflectModelCall(nil), l.modelCalls...),
		}
	}
	return out
}

// citedFacts returns the facts the answer used, carrying their attachments.
func (l *reflectLoop) citedFacts() []ReflectCitation {
	out := make([]ReflectCitation, 0, len(l.facts))
	for _, f := range l.facts {
		if f.Attachments == nil {
			f.Attachments = []RecallAttachment{}
		}
		out = append(out, f)
	}
	return out
}

// citedMentalModels returns the perspectives the answer used.
func (l *reflectLoop) citedMentalModels() []ReflectMentalModelCitation {
	out := make([]ReflectMentalModelCitation, 0, len(l.models))
	for _, m := range l.models {
		out = append(out, ReflectMentalModelCitation{ID: m.ID, Text: m.Text, Context: m.Context})
	}
	return out
}

// usage reports token accounting where visible output excludes reasoning
// tokens and cached is a subset of input.
func (l *reflectLoop) usage(answer string) ReflectUsage {
	input := reflectTokenCount(l.query.Query)
	output := reflectTokenCount(answer)
	return ReflectUsage{
		InputTokens:    input,
		OutputTokens:   output,
		TotalTokens:    input + output,
		CachedTokens:   0,
		ThoughtsTokens: 0,
	}
}

// reflectTokenCount approximates the token cost of a text as its whitespace
// fields; an empty text costs one token.
func reflectTokenCount(text string) int {
	n := 0
	inWord := false
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inWord = false
			continue
		}
		if !inWord {
			n++
			inWord = true
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

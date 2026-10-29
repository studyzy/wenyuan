package reflect

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// The prompt shows short aliases instead of the full identifiers, and an alias
// the model echoes in its answer is resolved back to the identifier.
func TestAliasesShortenPromptAndResolveInAnswer(t *testing.T) {
	longID := "9f8e7d6c-5b4a-3928-1716-0f1e2d3c4b5a"
	retrieval := &stubRetrieval{observations: []Evidence{{
		Kind: "observation", ID: longID, Text: "Alice prefers tea.",
	}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "tea"}),
			toolCall(ToolDone, nil),
		),
		closingDone(map[string]any{"answer": "According to o1, Alice prefers tea."}),
	)}
	outcome, err := NewLoop(p, retrieval, Config{Question: "what does Alice prefer?"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The evidence message carries the alias, never the raw identifier.
	var sawAlias, sawRaw bool
	for _, req := range p.requests {
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "[o1]") {
				sawAlias = true
			}
			if strings.Contains(message.Content, longID) {
				sawRaw = true
			}
		}
	}
	if !sawAlias {
		t.Fatal("the prompt must present the evidence under its alias")
	}
	if sawRaw {
		t.Fatal("a full identifier must not reach the prompt")
	}
	if !strings.Contains(outcome.Answer, longID) {
		t.Fatalf("alias not resolved in the answer: %q", outcome.Answer)
	}
	if strings.Contains(outcome.Answer, "o1") {
		t.Fatalf("an alias must not reach the caller: %q", outcome.Answer)
	}
}

// An identifier the model was never shown is scrubbed from the answer, while a
// retrieved one passes.
func TestUnverifiedIDsAreScrubbed(t *testing.T) {
	known := "11111111-2222-3333-4444-555555555555"
	fabricated := "99999999-8888-7777-6666-555555555555"
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: known, Text: "Alice prefers tea."}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "tea"}),
			toolCall(ToolDone, nil),
		),
		closingDone(map[string]any{"answer": "See " + known + " and also " + fabricated + "."}),
	)}
	outcome, err := NewLoop(p, retrieval, Config{Question: "what does Alice prefer?"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outcome.Answer, known) {
		t.Fatalf("a retrieved identifier must survive: %q", outcome.Answer)
	}
	if strings.Contains(outcome.Answer, fabricated) {
		t.Fatalf("a fabricated identifier must be scrubbed: %q", outcome.Answer)
	}
	if !strings.Contains(outcome.Answer, unverifiedID) {
		t.Fatalf("the scrub marker is missing: %q", outcome.Answer)
	}
}

// An alias in a tool call's arguments resolves to the real identifier, so a
// model citing "o2" still retrieves the right record.
func TestAliasResolvesInToolArguments(t *testing.T) {
	first := "aaaaaaaa-1111-2222-3333-444444444444"
	second := "bbbbbbbb-1111-2222-3333-444444444444"
	retrieval := &stubRetrieval{
		observations: []Evidence{
			{Kind: "observation", ID: first, Text: "one"},
			{Kind: "observation", ID: second, Text: "two"},
		},
		expanded: []Evidence{{Kind: "chunk", ID: "chunk-1", Text: "more"}},
	}
	p := &scriptedProvider{name: "stub", script: turnScript(
		toolCall(ToolSearchObservations, map[string]any{"query": "anything"}),
		toolCall(ToolExpand, map[string]any{"ids": []string{"o2"}}),
		toolCall(ToolDone, nil),
	)}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(retrieval.askedIDs) != 1 || retrieval.askedIDs[0] != second {
		t.Fatalf("expand asked for %v, want the resolved second identifier", retrieval.askedIDs)
	}
}

// The aliaser assigns each kind its own prefix and does not repeat one.
func TestAliaserPrefixesAndReuse(t *testing.T) {
	a := newAliaser()
	if got := a.aliasFor("observation", "id-1"); got != "o1" {
		t.Fatalf("first observation alias = %q", got)
	}
	if got := a.aliasFor("observation", "id-1"); got != "o1" {
		t.Fatalf("alias must be reused: %q", got)
	}
	if got := a.aliasFor("memory", "id-2"); got != "f1" {
		t.Fatalf("fact alias = %q", got)
	}
	if got := a.aliasFor("chunk", "chunk-1"); got != "c1" {
		t.Fatalf("chunk alias = %q", got)
	}
	if got := a.aliasFor("mental_model", "mm-1"); got != "p1" {
		t.Fatalf("perspective alias = %q", got)
	}
}

// Directives appear as a mandatory section that opens each system prompt and as
// a numbered reminder inside the same prompt, so a later turn cannot out-rank
// them. The rule's name is bolded ahead of its content.
func TestDirectivesAppearAsSectionAndReminder(t *testing.T) {
	retrieval := &stubRetrieval{
		observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "weekly deployments"}},
		directives: []Directive{
			{Name: "Language", Content: "Always answer in French."},
			{Content: "Never mention salaries."},
		},
	}
	p := &scriptedProvider{name: "stub", script: turnScript(
		toolCall(ToolSearchObservations, map[string]any{"query": "deploy"}),
		toolCall(ToolDone, nil),
	)}
	if _, err := NewLoop(p, retrieval, Config{Question: "when do we deploy?"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The section rides in both the reasoning loop's system prompt and the
	// answering call's, which is a separate call with its own prompt. The loop
	// re-sends its system prompt each turn, so the two calls are the first and
	// last requests.
	first := p.requests[0].Messages[0].Content
	last := p.requests[len(p.requests)-1].Messages[0].Content
	if first == last {
		t.Fatal("the reasoning and answering prompts must differ")
	}
	for _, content := range []string{first, last} {
		if !strings.Contains(content, "## DIRECTIVES (MANDATORY)") {
			t.Fatalf("the section must open the system prompt: %q", content)
		}
		if !strings.Contains(content, "- **Language**: Always answer in French.") {
			t.Fatalf("a named rule must be bolded: %q", content)
		}
		if !strings.Contains(content, "## REMINDER: MANDATORY DIRECTIVES") {
			t.Fatalf("the reminder must ride inside the same system prompt: %q", content)
		}
		if !strings.Contains(content, "1. **Language**: Always answer in French.") ||
			!strings.Contains(content, "2. Never mention salaries.") {
			t.Fatalf("the reminder must number the rules: %q", content)
		}
	}
	// The reasoning prompt closes with the reminder, after every instruction the
	// model reads.
	if !strings.HasSuffix(
		first, "Do NOT include any commentary about how you handled directives - just follow them.") {
		t.Fatalf("the reasoning prompt must close with the reminder: %q", first)
	}
}

// With no directives configured neither block is emitted, so a bank without
// them keeps the prompt it had.
func TestNoDirectivesNoBlocks(t *testing.T) {
	if got := directivesSection(nil); got != "" {
		t.Fatalf("section = %q, want empty", got)
	}
	if got := directivesReminder([]Directive{{Content: "  "}}); got != "" {
		t.Fatalf("reminder = %q, want empty", got)
	}
	section := directivesSection([]Directive{{Content: "b rule"}, {Content: "a rule"}})
	if strings.Index(section, "a rule") > strings.Index(section, "b rule") {
		t.Fatalf("rules must render in a stable order: %q", section)
	}
}

// An answer over the visible-length budget is rewritten, and the rewrite's usage
// is billed alongside the answer's.
func TestAnswerOverBudgetIsRewritten(t *testing.T) {
	longAnswer := strings.Repeat("this is a long answer. ", 200)
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, nil),
		),
		func() provider.CompletionResult {
			closing := closingDone(map[string]any{"answer": longAnswer})
			closing.Usage = provider.Usage{InputTokens: 10, OutputTokens: 10}
			return closing
		}(),
		provider.CompletionResult{Content: "short answer", Usage: provider.Usage{InputTokens: 5, OutputTokens: 5}},
	)}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 20}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "short answer" {
		t.Fatalf("answer = %q, want the rewrite", outcome.Answer)
	}
	if len(p.requests) != 4 {
		t.Fatalf("provider saw %d calls, want the extra rewrite call", len(p.requests))
	}
	if outcome.Usage.InputTokens != 15 || outcome.Usage.OutputTokens != 15 {
		t.Fatalf("usage = %+v, want both calls billed", outcome.Usage)
	}
}

// An answer inside the budget is sent as-is, with no extra call.
func TestAnswerWithinBudgetIsNotRewritten(t *testing.T) {
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, nil),
		),
		closingDone(map[string]any{"answer": "short"}),
	)}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 1000}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "short" {
		t.Fatalf("answer = %q", outcome.Answer)
	}
	if len(p.requests) != 3 {
		t.Fatalf("provider saw %d calls, want no rewrite call", len(p.requests))
	}
}

// An empty rewrite never empties the answer: a complete synthesis survives a
// model hiccup past the answer guard.
func TestEmptyRewriteKeepsOriginalAnswer(t *testing.T) {
	longAnswer := strings.Repeat("this is a long answer. ", 200)
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, nil),
		),
		closingDone(map[string]any{"answer": longAnswer}),
		provider.CompletionResult{Content: "   "},
	)}
	outcome, err := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 20}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != strings.TrimSpace(longAnswer) {
		t.Fatalf("an empty rewrite must keep the original answer, got %q", outcome.Answer)
	}
}

// concurrentRetrieval blocks each lookup until the expected number are in
// flight, so a sequential loop deadlocks instead of passing.
type concurrentRetrieval struct {
	expected int
	mu       sync.Mutex
	started  int
	release  chan struct{}
	perCall  map[string]string
}

func newConcurrentRetrieval(expected int) *concurrentRetrieval {
	return &concurrentRetrieval{expected: expected, release: make(chan struct{}), perCall: map[string]string{}}
}

func (r *concurrentRetrieval) arrive(query string) []Evidence {
	r.mu.Lock()
	r.started++
	if r.started >= r.expected {
		close(r.release)
	}
	release := r.release
	r.mu.Unlock()
	<-release
	return []Evidence{r.evidenceFor(query)}
}

func (r *concurrentRetrieval) evidenceFor(query string) Evidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	if text, ok := r.perCall[query]; ok {
		return Evidence{Kind: "observation", ID: text, Text: query}
	}
	return Evidence{Kind: "observation", ID: query, Text: query}
}

func (r *concurrentRetrieval) ReflectMentalModels(context.Context, string, int) ([]Evidence, error) {
	return nil, nil
}
func (r *concurrentRetrieval) ReflectObservations(_ context.Context, query string, _ int) ([]Evidence, error) {
	return r.arrive(query), nil
}
func (r *concurrentRetrieval) ReflectMemories(_ context.Context, query string, _ int) ([]Evidence, error) {
	return r.arrive(query), nil
}
func (r *concurrentRetrieval) ReflectReadModels(context.Context, []string) ([]Evidence, error) {
	return nil, nil
}
func (r *concurrentRetrieval) ReflectExpand(context.Context, []string, string) ([]Evidence, error) {
	return nil, nil
}
func (r *concurrentRetrieval) Directives(context.Context) []Directive { return nil }

// The lookups one turn asked for run together, and their results are still
// recorded in the order the model asked for them.
func TestToolCallsRunInParallelAndKeepOrder(t *testing.T) {
	retrieval := newConcurrentRetrieval(2)
	p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{{
		ToolCalls: []provider.ToolCall{
			toolCall(ToolSearchObservations, map[string]any{"query": "first"}),
			toolCall(ToolRecall, map[string]any{"query": "second"}),
		},
	}}}
	// A sequential loop would block on the first call's barrier forever, so the
	// run is bounded by the test itself.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outcome, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(ctx)
	if err != nil {
		t.Fatalf("parallel lookups must complete: %v", err)
	}
	if len(outcome.Evidence) < 2 {
		t.Fatalf("evidence = %+v, want both lookups", outcome.Evidence)
	}
	if outcome.Evidence[0].ID != "first" || outcome.Evidence[1].ID != "second" {
		t.Fatalf("results out of order: %+v", outcome.Evidence)
	}
}

// A failed lookup still fails the run even when its sibling succeeded.
func TestParallelLookupFailureFailsRun(t *testing.T) {
	retrieval := &failingRetrieval{}
	p := &scriptedProvider{name: "stub", script: []provider.CompletionResult{{
		ToolCalls: []provider.ToolCall{
			toolCall(ToolSearchObservations, map[string]any{"query": "ok"}),
			toolCall(ToolRecall, map[string]any{"query": "boom"}),
		},
	}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err == nil {
		t.Fatal("a failing lookup must fail the run")
	}
}

// failingRetrieval succeeds for one query and fails for another.
type failingRetrieval struct{}

func (r *failingRetrieval) ReflectMentalModels(context.Context, string, int) ([]Evidence, error) {
	return nil, nil
}
func (r *failingRetrieval) ReflectObservations(context.Context, string, int) ([]Evidence, error) {
	return []Evidence{{Kind: "observation", ID: "ok", Text: "ok"}}, nil
}
func (r *failingRetrieval) ReflectMemories(context.Context, string, int) ([]Evidence, error) {
	return nil, errors.New("lookup failed")
}
func (r *failingRetrieval) ReflectReadModels(context.Context, []string) ([]Evidence, error) {
	return nil, nil
}
func (r *failingRetrieval) ReflectExpand(context.Context, []string, string) ([]Evidence, error) {
	return nil, nil
}
func (r *failingRetrieval) Directives(context.Context) []Directive { return nil }

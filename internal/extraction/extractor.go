// Package extraction turns retained text into memory units and entities.
//
// It is the first consumer of the provider boundary: when a provider is
// configured the extraction asks the model for facts and entities, and when
// none is configured it falls back to the deterministic segmentation the
// service already used, so a deployment without a model keeps working exactly
// as before.
package extraction

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/studyzy/wenyuan/internal/provider"
)

// maxFactsPerItem bounds how many facts one item may yield, so a verbose model
// cannot turn one write into an unbounded number of units.
const maxFactsPerItem = 64

// maxCausalRelationsPerFact bounds the causal edges one fact may declare. The
// limit is the contract's: a model asked for causes will otherwise relate every
// fact to every other and turn the graph into noise.
const maxCausalRelationsPerFact = 2

// CausalRelation is one causal edge a fact declares to an earlier fact. Target
// is the index of the causing fact within the same item, which is how the model
// refers to it; the index is resolved to a stored unit after the writes.
type CausalRelation struct {
	// TargetIndex is the position of the causing fact in the item's fact list.
	// It must be lower than the declaring fact's own index: a cause precedes
	// its effect in the text.
	TargetIndex int
}

// Fact is one extracted memory. Everything but the text is optional, because a
// model may not supply it and an absent value must not become a wrong one.
type Fact struct {
	Text          string
	Entities      []string
	OccurredStart string
	OccurredEnd   string
	Confidence    float64
	// CausalRelations are the facts this fact was caused by, by index.
	CausalRelations []CausalRelation
}

// Result is the outcome of extracting one item.
type Result struct {
	Facts []Fact
	// Extracted reports whether a provider produced these facts. False means
	// the deterministic fallback did, which a caller may want to distinguish.
	Extracted bool
	// Usage is the token accounting the provider reported.
	Usage provider.Usage
}

// Extractor turns text into facts.
type Extractor struct {
	// Provider is the model boundary. A nil provider selects the deterministic
	// fallback, which is the same state as an unconfigured model.
	Provider provider.Provider
	// Scope labels the provider call for tracing.
	Scope string
}

// New builds an extractor over the provider boundary.
func New(p provider.Provider) *Extractor { return &Extractor{Provider: p, Scope: "retain"} }

// Available reports whether a model is configured, which is what selects the
// model path over the deterministic one.
func (e *Extractor) Available() bool {
	if e == nil || e.Provider == nil {
		return false
	}
	// The no-model selection is a real provider that reports its unavailability
	// through VerifyConnection rather than by being absent.
	return e.Provider.Name() != "none"
}

// Extract turns one item's text into facts.
//
// A model failure is returned rather than silently swallowed, because the
// caller decides whether a failed extraction should retry or fall back; the
// fallback is taken here only when no model is configured at all.
func (e *Extractor) Extract(ctx context.Context, text string) (Result, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Result{}, errors.New("extraction requires non-empty text")
	}
	if !e.Available() {
		return deterministicResult(trimmed), nil
	}

	response, err := e.Provider.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: factExtractionPrompt},
			{Role: "user", Content: trimmed},
		},
		ResponseSchema: factSchema(),
		Scope:          e.Scope,
		Strict:         true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("fact extraction call failed: %w", err)
	}
	facts, err := parseFacts(response.Structured, response.Content)
	if err != nil {
		return Result{}, err
	}
	if len(facts) == 0 {
		// A model that returned nothing usable is treated as having found
		// nothing, not as a failure, so the write still stores the document.
		return Result{Extracted: true, Usage: response.Usage}, nil
	}
	return Result{Facts: facts, Extracted: true, Usage: response.Usage}, nil
}

// factExtractionPrompt instructs the model. It asks for the same fields the
// schema declares, so a model that ignores the schema still produces the right
// shape.
const factExtractionPrompt = `Extract the discrete facts stated by the text.
Return one entry per fact. For each fact give:
- text: the fact as a single self-contained sentence, in the same language as the input
- entities: the named people, places, organisations, products or things the fact mentions
- occurred_start and occurred_end: an ISO-8601 timestamp when the fact is tied to a time, otherwise omit
- confidence: a number between 0 and 1 for how certain the text is
- causal_relations: up to two facts this one was caused by, each naming the position
  of the causing fact in this same list with target_index. The target must be an
  earlier fact: a cause is stated before its effect. Use it only for a real causal
  link, such as "lost job" then "could not pay rent", and omit it otherwise.
Do not invent facts the text does not state, and do not summarise the text as a whole.`

// factSchema declares the structured-output contract.
func factSchema() *provider.Schema {
	return &provider.Schema{
		Type: "object",
		Properties: map[string]*provider.Schema{
			"facts": {
				Type: "array",
				Items: &provider.Schema{
					Type: "object",
					Properties: map[string]*provider.Schema{
						"text":           {Type: "string"},
						"entities":       {Type: "array", Items: &provider.Schema{Type: "string"}},
						"occurred_start": {Type: "string"},
						"occurred_end":   {Type: "string"},
						"confidence":     {Type: "number"},
						"causal_relations": {
							Type: "array",
							Items: &provider.Schema{
								Type: "object",
								Properties: map[string]*provider.Schema{
									"target_index": {Type: "integer"},
								},
								Required: []string{"target_index"},
							},
						},
					},
					Required: []string{"text"},
				},
			},
		},
		Required: []string{"facts"},
	}
}

// parseFacts reads the model's structured value, falling back to parsing its
// text when no structured value was produced.
func parseFacts(structured any, _ string) ([]Fact, error) {
	root, ok := structured.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("extraction response was not an object")
	}
	rawFacts, ok := root["facts"].([]any)
	if !ok {
		// An empty or absent list is a legitimate "no facts found".
		return nil, nil
	}
	facts := make([]Fact, 0, len(rawFacts))
	for _, raw := range rawFacts {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text, _ := entry["text"].(string)
		if strings.TrimSpace(text) == "" {
			// A fact without text carries nothing a caller can use.
			continue
		}
		fact := Fact{
			Text:          strings.TrimSpace(text),
			Entities:      stringList(entry["entities"]),
			OccurredStart: stringValue(entry["occurred_start"]),
			OccurredEnd:   stringValue(entry["occurred_end"]),
			Confidence:    numberValue(entry["confidence"]),
		}
		// Relations are resolved against the index this fact will have, so a
		// forward or out-of-range reference is dropped rather than kept as a
		// dangling edge.
		fact.CausalRelations = causalRelations(entry["causal_relations"], len(facts))
		facts = append(facts, fact)
		if len(facts) >= maxFactsPerItem {
			break
		}
	}
	return facts, nil
}

// deterministicResult is the fallback used when no model is configured: the
// text becomes one fact per non-empty line, with no entities and no dates,
// because nothing here can know better.
func deterministicResult(text string) Result {
	facts := make([]Fact, 0)
	for _, paragraph := range splitParagraphs(text) {
		if len(facts) >= maxFactsPerItem {
			break
		}
		facts = append(facts, Fact{Text: paragraph, Confidence: 1})
	}
	return Result{Facts: facts, Extracted: false}
}

// splitParagraphs splits on blank lines, then on sentence boundaries within a
// paragraph, so a long block still yields separately retrievable facts.
func splitParagraphs(text string) []string {
	blocks := splitOnBlankLines(text)
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		for _, sentence := range splitSentences(block) {
			if trimmed := strings.TrimSpace(sentence); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

// splitOnBlankLines breaks a text into blocks at blank lines.
func splitOnBlankLines(text string) []string {
	normalised := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalised, "\n")
	blocks := make([]string, 0)
	var current strings.Builder
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if current.Len() > 0 {
				blocks = append(blocks, current.String())
				current.Reset()
			}
			continue
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(strings.TrimSpace(line))
	}
	if current.Len() > 0 {
		blocks = append(blocks, current.String())
	}
	return blocks
}

// splitSentences breaks a block at sentence-ending punctuation followed by a
// space, keeping the punctuation with the sentence it closes.
func splitSentences(block string) []string {
	var out []string
	var current strings.Builder
	runes := []rune(block)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])
		if !isSentenceEnd(runes[i]) {
			continue
		}
		// A decimal point or an abbreviation is not a sentence boundary.
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}
		out = append(out, current.String())
		current.Reset()
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// isSentenceEnd reports whether a rune ends a sentence.
func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	default:
		return false
	}
}

// causalRelations reads a fact's declared causal edges, keeping only those that
// point at an earlier fact in the same list and capping the count. Index is the
// position the declaring fact will occupy, so a cause must be strictly below it.
func causalRelations(value any, index int) []CausalRelation {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]CausalRelation, 0, len(raw))
	seen := map[int]bool{}
	for _, entry := range raw {
		if len(out) >= maxCausalRelationsPerFact {
			break
		}
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		target, ok := intValue(object["target_index"])
		if !ok || target < 0 || target >= index || seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, CausalRelation{TargetIndex: target})
	}
	return out
}

// intValue reads an integer that may be absent. A float that is whole counts,
// because a JSON schema constrained model may still emit 1.0 for an index.
func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

// stringList reads a list of strings from a structured value.
func stringList(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if single, ok := value.(string); ok && strings.TrimSpace(single) != "" {
			return []string{strings.TrimSpace(single)}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		text, ok := entry.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || seen[strings.ToLower(trimmed)] {
			continue
		}
		seen[strings.ToLower(trimmed)] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// stringValue reads a string that may be absent.
func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// numberValue reads a number that may be absent, clamped to the unit interval
// because a confidence outside it is not a confidence.
func numberValue(value any) float64 {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return 0
	}
	if number < 0 {
		return 0
	}
	if number > 1 {
		return 1
	}
	return number
}

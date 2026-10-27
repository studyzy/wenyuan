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
	"strconv"
	"strings"
	"unicode"

	"github.com/studyzy/wenyuan/internal/entitylabels"
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
	// Where is the place the fact is tied to, when the text names one.
	Where string
	// Kind is the reference's fact_kind classification (for example a
	// preference or an event), free-form and optional.
	Kind string
	// Type is the model's perspective: world, assistant or experience. The
	// storage layer maps it to the stored unit type; empty means world.
	Type string
	// FromAttachments names the attachments the fact came from, by their short
	// id, when the fact was read out of an attachment rather than the text.
	FromAttachments []string
	// Labels are the canonical key:value classifications the model selected
	// from the bank's controlled vocabulary, one or more per group. Empty when
	// no vocabulary was configured.
	Labels []string
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
	// MaxCorrectionRetries bounds how many times a response whose facts are in a
	// foreign shape is re-asked with the drift correction. Zero (the default)
	// leaves such a response as "no facts found".
	MaxCorrectionRetries int
	// MaxCompletionTokens caps the extraction call's generated tokens. Zero
	// leaves the parameter unset, which is what a deployment that configured no
	// cap asks for.
	MaxCompletionTokens int
	// Vision is the retain model's image-reading capability: non-nil true means
	// it reads images, non-nil false means it cannot, and nil means the provider
	// could not say. It is consulted only when a call carries inline
	// attachments, which is the one input that requires the model to read an
	// image rather than its caption.
	Vision *bool
	// VisionModel names the model configured to read attachments, so the
	// refusal below can point the caller at the setting that fixes it. It is
	// only used in the refusal's message.
	VisionModel string
}

// VisionNotSupportedError reports an attachment-bearing extraction the model
// configured to read attachments cannot serve. It is a distinct type so the
// reporting layer can map it to the reference's 422 rather than the 500 the
// generic error path would produce.
type VisionNotSupportedError struct {
	// Detail is the client-safe sentence, matching the reference's own refusal.
	Detail string
}

func (e *VisionNotSupportedError) Error() string { return e.Detail }

// VisionRefusal reports why a call carrying inline attachments must be refused,
// or nil when the configured model reads images. A model that reads images is
// the only state that accepts an attachment: an explicit "cannot" and a
// provider that could not say are both refused, because extracting from the
// caption and dropping the image would store a document that looks retained
// while the information the caller cared about is gone.
func (e *Extractor) VisionRefusal() error {
	if e == nil || (e.Vision != nil && *e.Vision) {
		return nil
	}
	model := e.VisionModel
	if model == "" {
		model = "the configured model"
	}
	if e.Vision != nil {
		return &VisionNotSupportedError{Detail: "The model configured to read attachments (" + model +
			") cannot read images, but this request carries inline image content. Point " +
			"WENYUAN_API_VLM_MODEL at a vision-capable model, or send the content as text only."}
	}
	return &VisionNotSupportedError{Detail: "Hindsight cannot tell whether the model configured to read " +
		"attachments (" + model + ") can read images, and this request carries inline image content. If " +
		"that model is vision-capable, set WENYUAN_API_LLM_VISION=true; otherwise send the content as " +
		"text only."}
}

// New builds an extractor over the provider boundary. The scope labels the
// model call "retain_extract_facts", matching the reference's fact-extraction
// scope so the trace surface reports the same call names.
func New(p provider.Provider) *Extractor {
	return &Extractor{Provider: p, Scope: "retain_extract_facts"}
}

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

// Extract turns one item's text into facts with no bank settings. It is the
// call for a caller that has none to supply; a caller with a retain mission,
// custom instructions, a strategy or a reference time uses ExtractWith.
//
// A model failure is returned rather than silently swallowed, because the
// caller decides whether a failed extraction should retry or fall back; the
// fallback is taken here only when no model is configured at all.
func (e *Extractor) Extract(ctx context.Context, text string) (Result, error) {
	return e.ExtractWith(ctx, text, Options{})
}

// ExtractWith turns one item's text into facts, rendering the bank's mission,
// custom instructions and strategy into the prompt and resolving relative
// dates against opts.ReferenceTime.
func (e *Extractor) ExtractWith(ctx context.Context, text string, opts Options) (Result, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Result{}, errors.New("extraction requires non-empty text")
	}
	// An attachment-bearing call needs a model that can read images. Refusing
	// here, before any model call, matches the reference's pre-extraction gate
	// and keeps a caption-only extraction from silently dropping the picture.
	if len(opts.Attachments) > 0 {
		if refusal := e.VisionRefusal(); refusal != nil {
			return Result{}, refusal
		}
	}
	if resolveMode(opts.Mode) == ModeChunks {
		// Chunks mode asks the model nothing: each segment of the item becomes
		// its own memory. The segmentation is the caller's, so no fact is
		// emitted here and the caller stores the segments verbatim.
		return Result{}, nil
	}
	if !e.Available() {
		return deterministicResult(trimmed, opts), nil
	}
	return e.extractModel(ctx, trimmed, opts, 0)
}

// repairAsk is the self-correcting instruction appended when the first response
// could not be parsed.
const repairAsk = `Your previous response could not be read as the required
JSON object. Return exactly one JSON object of the form {"facts":[{"text":"...",
"entities":[...],"occurred_start":"...","occurred_end":"...","confidence":0.0}]}
with no other text.`

// repair asks the model once more for a well-shaped response.
func (e *Extractor) repair(ctx context.Context, text, previous string, opts Options) ([]Fact, error) {
	response, err := e.complete(ctx, text, opts, previous, repairAsk)
	if err != nil {
		return nil, fmt.Errorf("fact extraction repair call failed: %w", err)
	}
	return parseForMode(resolveMode(opts.Mode), response.Structured, opts)
}

// driftCorrection is the user turn appended when the model answered with facts
// in a shape the extractor cannot read. A bare retry converges to nothing — the
// prompt is unchanged, so the model repeats the same drifted answer — which is
// why the correction names the keys the extractor expects.
const driftCorrection = `Valid keys for each fact are: "what" (required, the fact
itself), "when", "where", "who", "why", "fact_type", "entities" (a list of names),
"occurred_start", "occurred_end" and "confidence". Return exactly one JSON object
of the form {"facts":[{"what":"...","entities":["..."]}]} using those keys and no
others.`

// driftWarranted reports whether a response carried fact entries that parsed to
// nothing usable, which is what a drifted shape looks like: the model answered,
// but with keys this extractor does not read.
func driftWarranted(structured any) bool {
	root, ok := structured.(map[string]any)
	if !ok {
		return false
	}
	facts, ok := root["facts"].([]any)
	return ok && len(facts) > 0
}

// correctDrift re-asks the model with the drift correction appended, up to the
// configured retry budget. It returns the facts the corrected ask produced and
// the response they came from, so usage reflects the call that actually
// answered. A budget of zero leaves the drifted response alone.
func (e *Extractor) correctDrift(ctx context.Context, text string, response provider.CompletionResult, opts Options) ([]Fact, provider.CompletionResult, error) {
	facts, parseErr := parseForMode(resolveMode(opts.Mode), response.Structured, opts)
	for range e.MaxCorrectionRetries {
		if parseErr == nil && (len(facts) > 0 || !driftWarranted(response.Structured)) {
			break
		}
		previous := response.Content
		corrected, err := e.complete(ctx, text, opts, previous, driftCorrection)
		if err != nil {
			return nil, response, fmt.Errorf("fact extraction correction call failed: %w", err)
		}
		response = corrected
		facts, parseErr = parseForMode(resolveMode(opts.Mode), response.Structured, opts)
	}
	if parseErr != nil {
		return nil, response, parseErr
	}
	return facts, response, nil
}

// parseForMode reads the model's structured value with the parser the mode
// needs: verbatim entries carry no text and take the metadata-only reader,
// every other mode reads the dimensions (or the concise "text") and collapses
// them.
func parseForMode(mode string, structured any, opts Options) ([]Fact, error) {
	if mode == ModeVerbatim {
		return parseVerbatimFacts(structured, opts.Vocabulary)
	}
	return parseFacts(structured, opts)
}

// factExtractionPrompt and the mode schemas live in modes.go; this file owns
// the extractor's runtime behaviour.

// parseFacts reads the model's structured value, falling back to parsing its
// text when no structured value was produced. The vocabulary, when present,
// turns each fact's 'labels' object into canonical key:value selections.
func parseFacts(structured any, opts Options) ([]Fact, error) {
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
		// The stored text is the collapse of the model's dimensions, the way
		// the reference builds it. The core fact falls back through the
		// reference's key order (what, factual_core, text), so a response that
		// carries only a "text" — the concise default's own shape — collapses
		// to exactly that text.
		what := firstNonEmpty(
			dimensionValue(entry["what"]),
			dimensionValue(entry["factual_core"]),
			dimensionValue(entry["text"]),
		)
		if what == "" {
			// A fact without a core fact carries nothing a caller can use.
			continue
		}
		text := collapseFactText(what, dimensionValue(entry["when"]), dimensionValue(entry["who"]), dimensionValue(entry["why"]))
		if isDegenerateFact(text) {
			// A punctuation-only or otherwise contentless fact is a model
			// artefact, not a memory.
			continue
		}
		// The model's perspective is kept as it answered it. The storage layer
		// owns the mapping ("assistant" and "experience" mean the agent acted,
		// "world" is objective, and an unrecognised value falls back through
		// the fact kind), so collapsing it here would discard a distinction the
		// schema asks for. Lowercasing is the only normalisation the extractor
		// needs; an unrecognised value is left for that mapping to resolve.
		factType := strings.ToLower(stringValue(entry["fact_type"]))
		fact := Fact{
			Text:            strings.TrimSpace(text),
			Entities:        stringList(entry["entities"]),
			OccurredStart:   dimensionValue(entry["occurred_start"]),
			OccurredEnd:     dimensionValue(entry["occurred_end"]),
			Confidence:      numberValue(entry["confidence"]),
			Where:           stringValue(entry["where"]),
			Kind:            stringValue(entry["fact_kind"]),
			Type:            factType,
			FromAttachments: attachmentNumbers(entry["from_attachments"]),
			Labels:          extractLabels(entry, opts.Vocabulary),
		}
		// Relations are resolved against the index this fact will have, so a
		// forward or out-of-range reference is dropped rather than kept as a
		// dangling edge. A disabled causal switch never reads the field, so a
		// model that answers with relations anyway yields none.
		if causalLinksEnabled(opts) {
			fact.CausalRelations = causalRelations(entry["causal_relations"], len(facts))
		}
		facts = append(facts, fact)
		if len(facts) >= maxFactsPerItem {
			break
		}
	}
	return facts, nil
}

// parseVerbatimFacts reads the model's metadata-only answer for verbatim mode.
// A fact carries no text — the chunk itself becomes the stored text — so the
// degenerate-text filter does not apply here; the entries are kept for the
// entities, dates and perspective they report, and the caller collapses them
// into one fact per chunk.
func parseVerbatimFacts(structured any, vocab *entitylabels.Vocabulary) ([]Fact, error) {
	root, ok := structured.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("extraction response was not an object")
	}
	rawFacts, ok := root["facts"].([]any)
	if !ok {
		return nil, nil
	}
	facts := make([]Fact, 0, len(rawFacts))
	for _, raw := range rawFacts {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		facts = append(facts, Fact{
			Entities:        stringList(entry["entities"]),
			OccurredStart:   dimensionValue(entry["occurred_start"]),
			OccurredEnd:     dimensionValue(entry["occurred_end"]),
			Confidence:      numberValue(entry["confidence"]),
			Where:           stringValue(entry["where"]),
			Kind:            stringValue(entry["fact_kind"]),
			Type:            strings.ToLower(stringValue(entry["fact_type"])),
			FromAttachments: attachmentNumbers(entry["from_attachments"]),
			Labels:          extractLabels(entry, vocab),
		})
		if len(facts) >= maxFactsPerItem {
			break
		}
	}
	return facts, nil
}

// collapseVerbatim reduces the model's metadata entries to the one fact verbatim
// mode stores per chunk: the chunk's own text, carrying the first entry's
// metadata and the entities merged from every entry. An answer with no entries
// still yields the chunk, so a verbatim write never loses its text to a model
// that returned nothing — which is the whole point of the mode.
func collapseVerbatim(facts []Fact, chunk string) []Fact {
	representative := Fact{}
	if len(facts) > 0 {
		representative = facts[0]
		seen := map[string]bool{}
		for _, existing := range representative.Entities {
			seen[strings.ToLower(existing)] = true
		}
		for _, extra := range facts[1:] {
			for _, entity := range extra.Entities {
				if entity == "" || seen[strings.ToLower(entity)] {
					continue
				}
				seen[strings.ToLower(entity)] = true
				representative.Entities = append(representative.Entities, entity)
			}
		}
	}
	// Verbatim facts carry no causal edges: the entry is the whole chunk, so a
	// relation to another chunk's fact has no fact text to hang on.
	representative.Text = chunk
	representative.CausalRelations = nil
	return []Fact{representative}
}

// isDegenerateFact reports whether a fact text carries no real content: it has
// fewer than two letters or digits, so it is punctuation, whitespace or a bare
// symbol rather than a memory.
func isDegenerateFact(text string) bool {
	alnum := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			alnum++
			if alnum >= 2 {
				return false
			}
		}
	}
	return true
}

// deterministicResult is the fallback used when no model is configured: the
// text becomes one fact per non-empty line, with no entities. Relative dates
// the text names are inferred from opts.ReferenceTime, which is the only date
// knowledge available without a model.
func deterministicResult(text string, opts Options) Result {
	facts := make([]Fact, 0)
	for _, paragraph := range splitParagraphs(text) {
		if len(facts) >= maxFactsPerItem {
			break
		}
		facts = append(facts, Fact{Text: paragraph, Confidence: 1})
	}
	applyDateInference(facts, opts.ReferenceTime)
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

// attachmentNumbers reads a fact's from_attachments preserving order. The model
// answers with the one-based positions of the attachments it read the fact
// from, which arrive as JSON numbers; an already-stringified id is accepted too
// so a model that echoed an identifier still links. Order is preserved because
// the positions index the item's attachments in the order they appeared.
func attachmentNumbers(value any) []string {
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if text := attachmentNumber(entry); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		if text := attachmentNumber(value); text != "" {
			return []string{text}
		}
		return nil
	}
}

// attachmentNumber renders one from_attachments entry as a string: a JSON number
// becomes its integer form, and a string is trimmed.
func attachmentNumber(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if typed == float64(int(typed)) {
			return strconv.Itoa(int(typed))
		}
	case int:
		return strconv.Itoa(typed)
	}
	return ""
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

// PreviewMessages returns the messages and response schema an unconfigured
// fact-extraction run would send, so the prompt-preview route reports what is
// actually sent rather than a second copy of it that can drift.
//
// The runtime text is a bracketed stand-in: a preview answers "what does this
// bank send", and letting a caller supply the sample would make the answer
// depend on something the bank does not hold.
func PreviewMessages() ([]provider.Message, *provider.Schema) {
	return PreviewMessagesFor(Options{})
}

// PreviewMessagesFor renders the preview for a bank's retain configuration, so
// a custom-mode bank's preview shows its instructions and a verbose bank's
// preview shows the verbose prompt — the same builder the real run uses.
func PreviewMessagesFor(opts Options) ([]provider.Message, *provider.Schema) {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: composeExtractionPrompt(opts)},
		{Role: provider.RoleUser, Content: buildUserMessage(PlaceholderContent, opts)},
	}, schemaFor(opts)
}

// PlaceholderContent stands in for the text being retained.
const PlaceholderContent = "«the text being retained»"

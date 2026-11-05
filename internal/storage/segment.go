package storage

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ChunkSize is the maximum size of one chunk in characters. It bounds a chunk
// so a single paragraph cannot become an unbounded unit, and it matches the
// limit the retain pipeline documents for the content it hands to extraction.
const ChunkSize = 3000

// DefaultStructuredChunkSize is the structured-input chunk budget used when the
// caller configures none. It matches ChunkSize so an unconfigured deployment
// chunks exactly as it always did.
const DefaultStructuredChunkSize = ChunkSize

// defaultMaxAttachmentsPerChunk caps the number of inline references a single
// chunk may carry when the caller enables attachment-aware segmentation. It is
// the resolved process default; a caller that omits the cap applies no image
// handling at all.
const defaultMaxAttachmentsPerChunk = 8

// SegmentOptions carries the configured chunk budgets for one segmentation.
// The two size fields are optional: zero or less means the default, so the zero
// value reproduces the historical split. MaxAttachmentsPerChunk is optional
// too: zero or less disables the attachment-aware path.
type SegmentOptions struct {
	// ChunkSize bounds a prose chunk. Zero or less means ChunkSize.
	ChunkSize int
	// StructuredChunkSize bounds a structured chunk — a JSON conversation or a
	// JSONL body. Zero or less means DefaultStructuredChunkSize.
	StructuredChunkSize int
	// MaxAttachmentsPerChunk caps inline references in one chunk. Zero or less
	// leaves text carrying reference tokens on the ordinary text path.
	MaxAttachmentsPerChunk int
}

// normalized fills the defaults, so an unset budget keeps the historical size.
// It never defaults MaxAttachmentsPerChunk: an unset cap is a deliberate
// request for no image handling.
func (o SegmentOptions) normalized() SegmentOptions {
	if o.ChunkSize <= 0 {
		o.ChunkSize = ChunkSize
	}
	if o.StructuredChunkSize <= 0 {
		o.StructuredChunkSize = DefaultStructuredChunkSize
	}
	return o
}

// segmentsPerContent cuts one content part into its own chunks, numbered from 0
// the way the reference's retain numbers every content block it is handed: it
// cuts each content separately (engine/retain/orchestrator.py:1968-1987) and
// restarts chunk_index at 0 for each (engine/retain/fact_extraction.py:2985).
//
// Chunk rows are written per item, not from the whole document body. Segmenting
// the body instead makes a second content block land on index 0, which is the
// collision that once left a document with a single chunk (the shape
// engine/retain/orchestrator.py:1455-1470 warns about, #1888).
func segmentsPerContent(content string, opts SegmentOptions) []string {
	return SegmentWith(content, opts)
}

// chunkSeparators are the split points, ordered most to least preferred. The
// final empty separator lets the splitter break mid-word as a last resort, so a
// chunk can never exceed the budget.
var chunkSeparators = []string{
	"\n\n", // Paragraph breaks
	"\n",   // Line breaks
	". ",   // Sentence endings
	"! ",   // Exclamations
	"? ",   // Questions
	"; ",   // Semicolons
	", ",   // Commas
	" ",    // Words
	"",     // Characters (last resort)
}

// Segment splits content into ordered chunks of at most ChunkSize characters.
// A body within the budget is returned verbatim; longer bodies are split on
// paragraph, line, then sentence boundaries in that order, with structured
// inputs (a JSON conversation, a JSONL body, a lone JSON object) cut at their
// own record boundaries instead.
func Segment(content string) []string {
	return SegmentWith(content, SegmentOptions{})
}

// SegmentWith is Segment with the budgets supplied by the caller. The tighter
// of ChunkSize and StructuredChunkSize bounds every emitted chunk, so both a
// prose budget and a structured budget set by a deployment are honoured.
//
// Ordering is deliberate. Text small enough for one chunk is returned byte for
// byte, with no parsing: a short structured body must keep its exact spelling
// because its text is the chunk hash and the extraction input. Longer bodies
// are then classified — a JSON conversation array, a lone JSON object, a JSONL
// body, or plain prose — and each branch packs whole records. Every emitted
// chunk is itself within the budgets, so re-chunking any chunk returns it
// unchanged, which keeps a chunk id stable across re-ingests.
func SegmentWith(content string, opts SegmentOptions) []string {
	opts = opts.normalized()
	if strings.TrimSpace(content) == "" {
		return []string{content}
	}
	maxChars := opts.ChunkSize
	if opts.StructuredChunkSize < maxChars {
		maxChars = opts.StructuredChunkSize
	}

	// Reference-bearing text is budgeted by how many references one chunk may
	// carry, so it takes its own path before the short-text check: even a short
	// body with references must be grouped to respect the cap.
	if opts.MaxAttachmentsPerChunk > 0 && containsAttachmentPlaceholder(content) {
		return attachmentAwareChunks(content, maxChars, opts.MaxAttachmentsPerChunk)
	}

	if len(content) <= maxChars {
		return []string{content}
	}
	structuredLimit := maxChars

	if turns, ok := parseConversation(content); ok {
		return conversationChunks(turns, maxChars, structuredLimit)
	}

	var single any
	if json.Unmarshal([]byte(content), &single) == nil {
		if _, ok := single.(map[string]any); ok {
			// One object is one structured unit: keep it whole up to the
			// structured limit, else split its text within the chunk budget.
			if len(content) <= structuredLimit {
				return []string{content}
			}
			return splitRecursive(content, maxChars, chunkSeparators)
		}
	}

	if looksLikeJSONL(content) {
		return jsonlChunks(content, maxChars, structuredLimit)
	}
	return splitRecursive(content, maxChars, chunkSeparators)
}

// parseConversation reports whether content is a non-empty JSON array whose
// every element is an object, returning each element's verbatim text. Keeping
// the raw element bytes (rather than re-encoding a decoded value) preserves
// key order, numeric literals and escaping exactly, so a re-chunk is stable.
func parseConversation(content string) ([]string, bool) {
	var raw []json.RawMessage
	if json.Unmarshal([]byte(content), &raw) != nil || len(raw) == 0 {
		return nil, false
	}
	turns := make([]string, len(raw))
	for i, element := range raw {
		text := strings.TrimSpace(string(element))
		if !strings.HasPrefix(text, "{") {
			return nil, false
		}
		turns[i] = text
	}
	return turns, true
}

// conversationChunks packs consecutive whole turns into JSON-array chunks. The
// running size starts at the two brackets and grows by each turn's length plus
// one joining comma. A single turn larger than the limit is split as text
// against min(structuredLimit, maxChars) so no fragment exceeds the budget.
func conversationChunks(turns []string, maxChars, structuredLimit int) []string {
	out := make([]string, 0)
	current := make([]string, 0, len(turns))
	currentSize := 2 // the "[]" wrapper
	flush := func() {
		if len(current) == 0 {
			return
		}
		out = append(out, "["+strings.Join(current, ",")+"]")
		current = current[:0]
		currentSize = 2
	}
	fragmentBudget := structuredLimit
	if maxChars < fragmentBudget {
		fragmentBudget = maxChars
	}
	for _, turn := range turns {
		turnSize := len(turn) + 1 // +1 for the joining comma
		if len(turn) > structuredLimit {
			flush()
			out = append(out, splitRecursive(turn, fragmentBudget, chunkSeparators)...)
			continue
		}
		if currentSize+turnSize > maxChars && len(current) > 0 {
			flush()
		}
		current = append(current, turn)
		currentSize += turnSize
	}
	flush()
	return out
}

// nonblankLines returns the body's non-blank lines in order, split on a
// newline with a trailing carriage return removed. Lines are otherwise kept
// verbatim; only blank ones are dropped.
func nonblankLines(content string) []string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// looksLikeJSONL reports whether content is newline-delimited JSON objects:
// two or more non-blank lines, every one of them a JSON object. One line that
// is not an object disqualifies the whole body.
func looksLikeJSONL(content string) bool {
	seen := 0
	for _, line := range nonblankLines(content) {
		var value any
		if json.Unmarshal([]byte(line), &value) != nil {
			return false
		}
		if _, ok := value.(map[string]any); !ok {
			return false
		}
		seen++
	}
	return seen >= 2
}

// jsonlChunks packs whole JSONL lines into chunks joined by a newline. A line
// larger than the limit is split as text against min(structuredLimit, maxChars)
// so no fragment exceeds the budget.
func jsonlChunks(content string, maxChars, structuredLimit int) []string {
	out := make([]string, 0)
	current := make([]string, 0)
	currentSize := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		out = append(out, strings.Join(current, "\n"))
		current = current[:0]
		currentSize = 0
	}
	fragmentBudget := structuredLimit
	if maxChars < fragmentBudget {
		fragmentBudget = maxChars
	}
	for _, line := range nonblankLines(content) {
		if len(line) > structuredLimit {
			flush()
			out = append(out, splitRecursive(line, fragmentBudget, chunkSeparators)...)
			continue
		}
		packed := len(line) + 1 // +1 for the joining newline
		if currentSize+packed > maxChars && len(current) > 0 {
			flush()
		}
		current = append(current, line)
		currentSize += packed
	}
	flush()
	return out
}

// segmentAttachmentPlaceholderRE matches a well-formed inline reference token.
var segmentAttachmentPlaceholderRE = regexp.MustCompile(`\x{27e6}hs-att:[0-9a-fA-F]+\x{27e7}`)

func containsAttachmentPlaceholder(text string) bool {
	return segmentAttachmentPlaceholderRE.MatchString(text)
}

// segmentRun is one prose run or one reference token from a body, in order.
type segmentRun struct {
	text    string
	isImage bool
}

// placeholderRuns splits a body into prose runs and reference tokens, keeping
// every byte of the body in its original order.
func placeholderRuns(text string) []segmentRun {
	matches := segmentAttachmentPlaceholderRE.FindAllStringIndex(text, -1)
	runs := make([]segmentRun, 0, len(matches)*2+1)
	cursor := 0
	for _, match := range matches {
		if match[0] > cursor {
			runs = append(runs, segmentRun{text: text[cursor:match[0]]})
		}
		runs = append(runs, segmentRun{text: text[match[0]:match[1]], isImage: true})
		cursor = match[1]
	}
	if cursor < len(text) {
		runs = append(runs, segmentRun{text: text[cursor:]})
	}
	return runs
}

// attachmentAwareChunks packs a reference-bearing body so each chunk holds at
// most maxAttachments tokens and each token stays with the prose next to it. A
// token costs only the characters its spelling occupies against the text
// budget; the real constraint is how many references one chunk may carry.
//
// An over-long prose run is split against the room left in the open chunk, so
// its first piece can join whatever is already buffered (prose-then-image and
// image-then-prose both work). When too little room remains to be worth
// joining, the chunk is flushed and the run is split against the full budget.
// Every emitted chunk is within both budgets, so re-chunking one returns it
// unchanged.
func attachmentAwareChunks(text string, maxChars, maxAttachments int) []string {
	minimumUsefulRoom := maxChars / 4
	if minimumUsefulRoom < 1 {
		minimumUsefulRoom = 1
	}
	out := make([]string, 0)
	buffered := make([]string, 0)
	used := 0
	images := 0
	flush := func() {
		if len(buffered) == 0 {
			return
		}
		packed := strings.TrimSpace(strings.Join(buffered, ""))
		buffered = buffered[:0]
		used = 0
		images = 0
		if packed != "" {
			out = append(out, packed)
		}
	}
	appendPiece := func(piece string, isImage bool) {
		buffered = append(buffered, piece)
		used += len(piece)
		if isImage {
			images++
		}
	}
	for _, run := range placeholderRuns(text) {
		if run.isImage {
			if len(buffered) > 0 && (used+len(run.text) > maxChars || images+1 > maxAttachments) {
				flush()
			}
			appendPiece(run.text, true)
			continue
		}
		if used+len(run.text) <= maxChars {
			appendPiece(run.text, false)
			continue
		}
		room := maxChars - used
		keepOpen := len(buffered) > 0 && room >= minimumUsefulRoom
		if !keepOpen {
			flush()
		}
		budget := maxChars
		if keepOpen {
			budget = room
		}
		for _, piece := range splitRecursive(run.text, budget, chunkSeparators) {
			if len(buffered) > 0 && used+len(piece) > maxChars {
				flush()
			}
			appendPiece(piece, false)
		}
	}
	flush()
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

// splitRecursive splits text into pieces of at most maxChars, cutting on the
// most-preferred separator the text contains and recursing into any piece that
// is still over budget.
func splitRecursive(text string, maxChars int, separators []string) []string {
	separator := separators[len(separators)-1]
	remaining := []string(nil)
	for index, candidate := range separators {
		if candidate == "" {
			separator = candidate
			break
		}
		if strings.Contains(text, candidate) {
			separator = candidate
			remaining = separators[index+1:]
			break
		}
	}

	out := make([]string, 0)
	buffered := make([]string, 0)
	bufferedLen := 0
	flush := func() {
		if len(buffered) == 0 {
			return
		}
		packed := strings.TrimSpace(strings.Join(buffered, ""))
		buffered = buffered[:0]
		bufferedLen = 0
		if packed != "" {
			out = append(out, packed)
		}
	}

	for _, piece := range splitOnSeparator(text, separator) {
		if len(piece) < maxChars {
			if len(buffered) > 0 && bufferedLen+len(piece) > maxChars {
				flush()
			}
			buffered = append(buffered, piece)
			bufferedLen += len(piece)
			continue
		}
		// Over budget even alone: close the run so ordering is preserved, then
		// split this piece further, or emit it whole when no separator is left.
		flush()
		if len(remaining) > 0 {
			out = append(out, splitRecursive(piece, maxChars, remaining)...)
		} else {
			out = append(out, piece)
		}
	}
	flush()
	return out
}

// splitOnSeparator cuts text at every occurrence of separator, keeping each
// separator at the start of the piece that follows it — the reference's
// RecursiveCharacterTextSplitter convention (re.split with keep_separator=True):
// the piece before the first match comes first, then one piece per match
// running from that match to the next. An empty separator degrades to one piece
// per character, which is the documented last resort.
func splitOnSeparator(text, separator string) []string {
	if separator == "" {
		runes := []rune(text)
		out := make([]string, 0, len(runes))
		for _, r := range runes {
			out = append(out, string(r))
		}
		return out
	}
	out := make([]string, 0)
	previous := 0
	scan := 0
	for {
		index := strings.Index(text[scan:], separator)
		if index < 0 {
			break
		}
		match := scan + index
		if piece := text[previous:match]; piece != "" {
			out = append(out, piece)
		}
		previous = match
		scan = match + len(separator)
	}
	if tail := text[previous:]; tail != "" {
		out = append(out, tail)
	}
	return out
}

// segmentOptionsFor resolves the boundaries a request asked for, leaving a
// request that named none on the package defaults. The reference cap is the
// resolved process default; a deployment that configures a different value
// must carry it into RetainRequest for this function to use.
func segmentOptionsFor(req RetainRequest) SegmentOptions {
	cap := req.MaxAttachmentsPerChunk
	if cap <= 0 {
		cap = defaultMaxAttachmentsPerChunk
	}
	return SegmentOptions{
		ChunkSize:              req.ChunkSize,
		StructuredChunkSize:    req.StructuredChunkSize,
		MaxAttachmentsPerChunk: cap,
	}
}

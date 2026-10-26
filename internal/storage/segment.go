package storage

import (
	"encoding/json"
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

// SegmentOptions carries the configured chunk budgets for one segmentation.
// Both fields are optional: zero or less means the default, so the zero value
// reproduces the historical split.
type SegmentOptions struct {
	// ChunkSize bounds a prose chunk. Zero or less means ChunkSize.
	ChunkSize int
	// StructuredChunkSize bounds a structured chunk — a JSON conversation or a
	// JSONL body. Zero or less means DefaultStructuredChunkSize.
	StructuredChunkSize int
}

// normalized fills the defaults, so an unset budget keeps the historical size.
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
// restarts `chunk_index` at 0 for each (engine/retain/fact_extraction.py:2985).
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

// Segment splits content into ordered chunks of at most ChunkSize characters,
// preferring the earliest available separator so boundaries fall on paragraph,
// line, then sentence boundaries in that order. Structured inputs are chunked
// whole: a JSON conversation becomes one chunk per turn and a JSONL body one
// chunk per line group, so a chunk never splits a turn or a record.
//
// A chunk is only ever produced from content that was present: the pieces are
// packed greedily and each emitted chunk is whitespace-trimmed, so an empty
// input still yields the single empty chunk a caller expects rather than
// nothing at all.
func Segment(content string) []string {
	return SegmentWith(content, SegmentOptions{})
}

// SegmentWith is Segment with the chunk budgets supplied by the caller: the
// structured branch is bounded by opts.StructuredChunkSize and the prose branch
// by opts.ChunkSize, so a bank can size the two independently.
func SegmentWith(content string, opts SegmentOptions) []string {
	opts = opts.normalized()
	if strings.TrimSpace(content) == "" {
		return []string{content}
	}
	if chunks, ok := segmentJSONConversation(content, opts.StructuredChunkSize); ok {
		return chunks
	}
	if chunks, ok := segmentJSONLines(content, opts.StructuredChunkSize); ok {
		return chunks
	}
	chunks := splitRecursive(content, opts.ChunkSize, chunkSeparators)
	if len(chunks) == 0 {
		return []string{content}
	}
	return chunks
}

// segmentJSONConversation chunks a JSON array of messages one turn at a time,
// packing consecutive turns until the character budget is reached. It reports
// false when the content is not such an array.
func segmentJSONConversation(content string, maxChars int) ([]string, bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var turns []any
	if err := json.Unmarshal([]byte(trimmed), &turns); err != nil || len(turns) == 0 {
		return nil, false
	}
	rendered := make([]string, 0, len(turns))
	for _, turn := range turns {
		rendered = append(rendered, renderTurn(turn))
	}
	return packPieces(rendered, maxChars), true
}

// segmentJSONLines chunks a JSONL body one record at a time, packing
// consecutive records until the budget is reached. It reports false unless
// every non-empty line is a JSON object.
func segmentJSONLines(content string, maxChars int) ([]string, bool) {
	lines := strings.Split(content, "\n")
	pieces := make([]string, 0, len(lines))
	seenObject := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "{") {
			return nil, false
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
			return nil, false
		}
		seenObject = true
		pieces = append(pieces, trimmed)
	}
	if !seenObject {
		return nil, false
	}
	return packPieces(pieces, maxChars), true
}

// renderTurn renders one conversation turn as "role: content" text.
func renderTurn(turn any) string {
	entry, ok := turn.(map[string]any)
	if !ok {
		return toJSONString(turn)
	}
	role, _ := entry["role"].(string)
	body := toJSONString(entry["content"])
	if role == "" {
		return body
	}
	return role + ": " + body
}

// toJSONString renders a value as text, using the raw string for a string.
func toJSONString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// packPieces greedily packs whole pieces into chunks of at most maxChars
// characters; a single piece larger than the budget is split by the recursive
// splitter so it can never exceed the bound.
func packPieces(pieces []string, maxChars int) []string {
	out := make([]string, 0)
	buffer := make([]string, 0, len(pieces))
	bufferLen := 0
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		out = append(out, strings.TrimSpace(strings.Join(buffer, "\n")))
		buffer = buffer[:0]
		bufferLen = 0
	}
	for _, piece := range pieces {
		if len(piece) > maxChars {
			flush()
			out = append(out, splitRecursive(piece, maxChars, chunkSeparators)...)
			continue
		}
		if bufferLen > 0 && bufferLen+1+len(piece) > maxChars {
			flush()
		}
		buffer = append(buffer, piece)
		bufferLen += len(piece) + 1
	}
	flush()
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
// request that named none on the package defaults.
func segmentOptionsFor(req RetainRequest) SegmentOptions {
	return SegmentOptions{ChunkSize: req.ChunkSize, StructuredChunkSize: req.StructuredChunkSize}
}

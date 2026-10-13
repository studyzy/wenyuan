package storage

import "strings"

// ChunkSize is the maximum size of one chunk in characters. It bounds a chunk
// so a single paragraph cannot become an unbounded unit, and it matches the
// limit the retain pipeline documents for the content it hands to extraction.
const ChunkSize = 3000

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
// line, then sentence boundaries in that order.
//
// A chunk is only ever produced from content that was present: the pieces are
// packed greedily and each emitted chunk is whitespace-trimmed, so an empty
// input still yields the single empty chunk a caller expects rather than
// nothing at all.
func Segment(content string) []string {
	if strings.TrimSpace(content) == "" {
		return []string{content}
	}
	chunks := splitRecursive(content, ChunkSize, chunkSeparators)
	if len(chunks) == 0 {
		return []string{content}
	}
	return chunks
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

// splitOnSeparator cuts text at every occurrence of separator, keeping the
// separator at the start of the piece that follows it. An empty separator
// degrades to one piece per character, which is the documented last resort.
func splitOnSeparator(text, separator string) []string {
	if separator == "" {
		runes := []rune(text)
		out := make([]string, 0, len(runes))
		for _, r := range runes {
			out = append(out, string(r))
		}
		return out
	}
	parts := strings.SplitAfter(text, separator)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

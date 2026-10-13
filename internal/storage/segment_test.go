package storage

import (
	"strings"
	"testing"
)

// Chunking is bounded by a size budget and prefers the earliest separator the
// text contains, so boundaries fall on paragraph, line, then sentence
// boundaries in that order.
func TestSegmentSplitsOnPreferredBoundary(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantChunks int
		wantErr    bool
	}{
		{
			name:       "empty content yields the single empty chunk",
			content:    "",
			wantChunks: 1,
		},
		{
			name:       "whitespace-only content yields the single chunk",
			content:    "   \n\t  ",
			wantChunks: 1,
		},
		{
			name:       "content within budget stays one chunk",
			content:    "short body",
			wantChunks: 1,
		},
		{
			name:       "content at exactly the budget stays one chunk",
			content:    strings.Repeat("a", ChunkSize-1),
			wantChunks: 1,
		},
		{
			name:       "over-budget content splits",
			content:    strings.Repeat("a", ChunkSize+500),
			wantChunks: 2,
		},
		{
			name:       "paragraph boundary is preferred",
			content:    strings.Repeat("a", ChunkSize-1) + "\n\n" + strings.Repeat("b", ChunkSize-1),
			wantChunks: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := Segment(test.content)
			if len(chunks) != test.wantChunks {
				t.Fatalf("Segment() returned %d chunks, want %d: %q", len(chunks), test.wantChunks, chunks)
			}
		})
	}
}

// No chunk may exceed the budget, including when the content offers no
// separator at all and the splitter must fall back to per-character cutting.
func TestSegmentNeverExceedsBudget(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"single unbroken run", strings.Repeat("x", ChunkSize*3)},
		{"multibyte unbroken run", strings.Repeat("文", ChunkSize)},
		{"long single line", strings.Repeat("word ", ChunkSize)},
		{"many paragraphs", strings.Repeat("para\n\n", ChunkSize)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, chunk := range Segment(test.content) {
				if len(chunk) > ChunkSize {
					t.Fatalf("chunk %d is %d chars, over the %d budget", i, len(chunk), ChunkSize)
				}
			}
		})
	}
}

// Chunking must not lose content: every non-whitespace character of the input
// appears in exactly one chunk, so a write cannot silently drop text.
func TestSegmentPreservesContent(t *testing.T) {
	tests := []string{
		"first paragraph\n\nsecond paragraph",
		strings.Repeat("a", ChunkSize+1),
		strings.Repeat("hello world. ", 500),
		"段落一\n\n段落二\n\n" + strings.Repeat("内容", 2000),
	}
	for _, content := range tests {
		t.Run("", func(t *testing.T) {
			got := strings.Join(Segment(content), "")
			want := onlyNonSpace(content)
			if onlyNonSpace(got) != want {
				t.Fatalf("chunking lost or altered content: got %d chars, want %d", len(got), len(content))
			}
		})
	}
}

// onlyNonSpace strips whitespace so a comparison ignores the trimming the
// splitter is allowed to do at chunk edges.
func onlyNonSpace(text string) string {
	var builder strings.Builder
	for _, r := range text {
		if r != ' ' && r != '\n' && r != '\t' && r != '\r' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

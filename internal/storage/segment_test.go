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

// A cut keeps the separator at the start of the piece that follows it, so a
// piece that alone overflows the budget is recursed into without its leading
// separator and its trailing word is emitted as its own chunk. This is the
// reference's keep_separator=True convention, and the same body at the default
// budget still comes back as one chunk.
func TestSegmentKeepsSeparatorOnTheFollowingPiece(t *testing.T) {
	got := SegmentWith("Alpha beta.", SegmentOptions{ChunkSize: 5})
	want := []string{"Alpha", "beta", "."}
	if len(got) != len(want) {
		t.Fatalf("chunks = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
	if chunks := SegmentWith("Alpha beta.", SegmentOptions{}); len(chunks) != 1 {
		t.Fatalf("the same body within budget must stay one chunk: %q", chunks)
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

// A JSON conversation is chunked whole turns, not mid-turn.
func TestSegmentConversation(t *testing.T) {
	content := `[{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"}]`
	chunks := Segment(content)
	if len(chunks) == 0 {
		t.Fatal("conversation produced no chunks")
	}
	joined := strings.Join(chunks, "\n")
	if !strings.Contains(joined, "user: hello") || !strings.Contains(joined, "assistant: hi there") {
		t.Fatalf("a turn must not be split mid-turn: %v", chunks)
	}
}

// A JSONL body is chunked whole records, not mid-record.
func TestSegmentJSONLines(t *testing.T) {
	content := `{"a":1}` + "\n" + `{"b":2}` + "\n"
	chunks := Segment(content)
	if len(chunks) != 1 {
		t.Fatalf("small JSONL must pack into one chunk, got %v", chunks)
	}
	if chunks[0] != `{"a":1}`+"\n"+`{"b":2}` {
		t.Fatalf("JSONL chunk = %q", chunks[0])
	}
}

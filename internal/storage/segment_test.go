package storage

import (
	"encoding/json"
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

// A short body is returned byte for byte, with no structural parsing at all.
// Keeping the exact spelling matters because a chunk's text is both its content
// hash and the extraction input; a short JSON array or JSONL body must not be
// re-rendered into prose or have its trailing newline trimmed away.
func TestSegmentShortStructuredPassesThroughVerbatim(t *testing.T) {
	cases := []string{
		"  a short prose line with edges  ",
		"[{\"role\":\"user\",\"content\":\"hello\"},{\"role\":\"assistant\",\"content\":\"hi\"}]",
		"{\"a\":1,\"b\":\"two\"}",
		"{\"a\":1}\n{\"b\":2}\n",
		"first\n\nsecond\n",
	}
	for _, content := range cases {
		chunks := Segment(content)
		if len(chunks) != 1 || chunks[0] != content {
			t.Fatalf("short body must be returned verbatim: got %q, want %q", chunks, content)
		}
	}
}

// A long JSON conversation is cut at whole-turn boundaries and every chunk is
// a JSON array of complete turns, not a flattened "role: content" rendering.
// Re-chunking a chunk is a fixed point, so a chunk id stays stable.
func TestSegmentConversation(t *testing.T) {
	content := conversationJSON(t, 12, 160)
	opts := SegmentOptions{ChunkSize: 400, StructuredChunkSize: 400}
	chunks := SegmentWith(content, opts)
	if len(chunks) < 2 {
		t.Fatalf("a long conversation must span several chunks, got %d", len(chunks))
	}
	gotTurns := 0
	for _, chunk := range chunks {
		var turns []map[string]any
		if err := json.Unmarshal([]byte(chunk), &turns); err != nil {
			t.Fatalf("chunk must be a JSON array: %v: %q", err, chunk)
		}
		if len(turns) == 0 {
			t.Fatalf("chunk must carry at least one complete turn: %q", chunk)
		}
		gotTurns += len(turns)
	}
	if gotTurns != 12 {
		t.Fatalf("turns = %d, want 12 (no turn may be dropped or split)", gotTurns)
	}
	for _, chunk := range chunks {
		if again := SegmentWith(chunk, opts); len(again) != 1 || again[0] != chunk {
			t.Fatalf("re-chunking a chunk must return it unchanged: %q -> %q", chunk, again)
		}
	}
}

// A JSONL body needs at least two non-blank object lines and every line must
// be an object. A lone object takes its own branch instead of being treated as
// a line group, and a body with a non-object line falls back to prose.
func TestSegmentStructuredThresholdAndSingleObject(t *testing.T) {
	single := "{\"k\":\"" + strings.Repeat("x", 20) + "\"}\n"
	if chunks := Segment(single); len(chunks) != 1 || chunks[0] != single {
		t.Fatalf("a lone object must be kept verbatim, not line-packed: %q", chunks)
	}

	opts := SegmentOptions{ChunkSize: 200, StructuredChunkSize: 200}
	big, err := json.Marshal(map[string]any{"blob": strings.Repeat("w", 800)})
	if err != nil {
		t.Fatal(err)
	}
	split := SegmentWith(string(big), opts)
	if len(split) < 2 {
		t.Fatalf("a long lone object must be split, got %d chunks", len(split))
	}
	if got, want := onlyNonSpace(strings.Join(split, "")), onlyNonSpace(string(big)); got != want {
		t.Fatalf("splitting a lone object lost content: got %d chars, want %d", len(got), len(want))
	}

	mixed := "{\"a\":1}\n" + strings.Repeat("prose ", 60) + "\n"
	prose := SegmentWith(mixed, opts)
	if !strings.Contains(onlyNonSpace(strings.Join(prose, "")), "prose") {
		t.Fatalf("a non-object line must survive prose splitting: %q", prose)
	}

	var lines []string
	for i := 0; i < 4; i++ {
		record, mErr := json.Marshal(map[string]any{"k": i, "pad": strings.Repeat("y", 90)})
		if mErr != nil {
			t.Fatal(mErr)
		}
		lines = append(lines, string(record))
	}
	jsonl := SegmentWith(strings.Join(lines, "\n"), opts)
	if len(jsonl) < 2 {
		t.Fatalf("a multi-line JSONL body must span chunks, got %d", len(jsonl))
	}
	for _, chunk := range jsonl {
		for _, line := range strings.Split(chunk, "\n") {
			var obj map[string]any
			if uErr := json.Unmarshal([]byte(line), &obj); uErr != nil {
				t.Fatalf("JSONL chunk must hold whole records: %v: %q", uErr, line)
			}
		}
	}
}

// A turn larger than the budget is split as text within min(limit, budget),
// keeping each turn body's bytes (the array wrapper is not re-emitted). The
// old flattened rendering lost the JSON keys, so this catches that regression.
func TestSegmentOversizedTurnSplitsWithinBudget(t *testing.T) {
	turn, err := json.Marshal(map[string]any{"role": "user", "content": strings.Repeat("z", 900)})
	if err != nil {
		t.Fatal(err)
	}
	content := "[" + string(turn) + "," + string(turn) + "]"
	opts := SegmentOptions{ChunkSize: 300, StructuredChunkSize: 300}
	chunks := SegmentWith(content, opts)
	if len(chunks) < 2 {
		t.Fatalf("oversized turns must be split, got %d chunks", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > 300 {
			t.Fatalf("chunk %d is %d chars, over the 300 budget", i, len(chunk))
		}
	}
	// Oversized turns are emitted as raw text fragments: the array wrapper is
	// not re-serialized around them, so the two turn bodies alone survive.
	if got, want := onlyNonSpace(strings.Join(chunks, "")), onlyNonSpace(string(turn)+string(turn)); got != want {
		t.Fatalf("splitting oversized turns lost content: got %d chars, want %d", len(got), len(want))
	}
}

// A short JSONL body passes through verbatim; a longer one is cut at whole
// record boundaries so no object is split across chunks.
func TestSegmentJSONLines(t *testing.T) {
	short := "{\"a\":1}" + "\n" + "{\"b\":2}" + "\n"
	if chunks := Segment(short); len(chunks) != 1 || chunks[0] != short {
		t.Fatalf("a short JSONL body must be returned verbatim: %q", chunks)
	}

	var lines []string
	for i := 0; i < 8; i++ {
		record, err := json.Marshal(map[string]any{"k": i, "pad": strings.Repeat("x", 120)})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(record))
	}
	content := strings.Join(lines, "\n") + "\n"
	opts := SegmentOptions{ChunkSize: 300, StructuredChunkSize: 300}
	chunks := SegmentWith(content, opts)
	if len(chunks) < 2 {
		t.Fatalf("a long JSONL body must span several chunks, got %d", len(chunks))
	}
	var gotLines []string
	for _, chunk := range chunks {
		for _, line := range strings.Split(chunk, "\n") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Fatalf("a chunk must contain only whole JSONL records: %v: %q", err, line)
			}
			gotLines = append(gotLines, line)
		}
	}
	if len(gotLines) != len(lines) {
		t.Fatalf("records = %d, want %d", len(gotLines), len(lines))
	}
	for i := range lines {
		if gotLines[i] != lines[i] {
			t.Fatalf("record %d = %q, want %q", i, gotLines[i], lines[i])
		}
	}
}

// attachmentToken renders one inline reference token.
func attachmentToken(id string) string {
	return "\u27e6hs-att:" + id + "\u27e7"
}

// A reference-bearing body is grouped so no chunk exceeds the image cap, each
// chunk stays within the text budget, and re-chunking is a fixed point.
func TestSegmentAttachmentCapGroupsImages(t *testing.T) {
	opts := SegmentOptions{ChunkSize: 120, StructuredChunkSize: 120, MaxAttachmentsPerChunk: 2}
	var b strings.Builder
	ids := []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb", "cccccccccccc", "dddddddddddd", "eeeeeeeeeeee", "ffffffffffff"}
	for i, id := range ids {
		b.WriteString("caption" + string(rune('A'+i)) + " " + attachmentToken(id) + " ")
	}
	chunks := SegmentWith(b.String(), opts)
	if len(chunks) < 3 {
		t.Fatalf("a cap of 2 images must split 6 images across chunks, got %d", len(chunks))
	}
	total := 0
	for _, chunk := range chunks {
		n := strings.Count(chunk, "hs-att:")
		total += n
		if n > 2 {
			t.Fatalf("chunk carries %d images, over the cap of 2: %q", n, chunk)
		}
		if len(chunk) > 120 {
			t.Fatalf("chunk is %d chars, over the 120 budget: %q", len(chunk), chunk)
		}
		if again := SegmentWith(chunk, opts); len(again) != 1 || again[0] != chunk {
			t.Fatalf("re-chunking a reference chunk must return it unchanged: %q -> %q", chunk, again)
		}
	}
	if total != len(ids) {
		t.Fatalf("images across chunks = %d, want %d", total, len(ids))
	}
}

// Without a configured cap, reference-bearing text stays on the ordinary text
// path — a short body is simply returned verbatim.
func TestSegmentAttachmentCapRequiresConfig(t *testing.T) {
	text := "intro " + attachmentToken("aaaaaaaaaaaa") + " tail"
	chunks := SegmentWith(text, SegmentOptions{ChunkSize: 120, StructuredChunkSize: 120})
	if len(chunks) != 1 || chunks[0] != text {
		t.Fatalf("without a cap a short body must pass verbatim: %q", chunks)
	}
}

// When an image is followed by a long prose run, the run is split against the
// room left in the open chunk so its first piece stays with the image.
func TestSegmentAttachmentProseUsesRemainingRoom(t *testing.T) {
	opts := SegmentOptions{ChunkSize: 100, StructuredChunkSize: 100, MaxAttachmentsPerChunk: 2}
	text := attachmentToken("aaaaaaaaaaaa") + strings.Repeat("word ", 60)
	chunks := SegmentWith(text, opts)
	if len(chunks) < 2 {
		t.Fatalf("a long prose run after an image must be split, got %d chunks", len(chunks))
	}
	first := chunks[0]
	if !strings.Contains(first, "hs-att:") || !strings.Contains(first, "word") {
		t.Fatalf("the first piece of prose must stay with the image: %q", first)
	}
}

package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

// conversationJSON builds a JSON conversation whose turns each carry a body of
// the given length.
func conversationJSON(t *testing.T, turns, bodyLen int) string {
	t.Helper()
	body := strings.Repeat("a", bodyLen)
	list := make([]map[string]any, 0, turns)
	for range turns {
		list = append(list, map[string]any{"role": "user", "content": body})
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// The structured (JSON conversation) branch honours StructuredChunkSize: a
// smaller budget yields more chunks, each within the budget.
func TestStructuredChunkHonoursConfiguredSize(t *testing.T) {
	content := conversationJSON(t, 4, 120)

	chunks := SegmentWith(content, SegmentOptions{StructuredChunkSize: 300})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2 at a 300-char structured budget", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > 300 {
			t.Fatalf("chunk is %d chars, over the configured 300", len(chunk))
		}
	}

	// A budget large enough for every turn keeps the whole conversation as one
	// chunk.
	whole := SegmentWith(content, SegmentOptions{StructuredChunkSize: 1000})
	if len(whole) != 1 {
		t.Fatalf("chunks = %d, want 1 at a 1000-char budget", len(whole))
	}
}

// The prose branch honours ChunkSize.
func TestProseChunkHonoursConfiguredSize(t *testing.T) {
	content := strings.Repeat("word ", 200) // 1000 chars
	chunks := SegmentWith(content, SegmentOptions{ChunkSize: 150})
	if len(chunks) < 2 {
		t.Fatalf("a small budget must produce several chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > 150 {
			t.Fatalf("chunk %d is %d chars, over the configured 150", i, len(chunk))
		}
	}
}

// An unconfigured call behaves exactly as the historical Segment, so existing
// callers keep their output.
func TestSegmentDefaultsUnchanged(t *testing.T) {
	cases := []string{
		"",
		"one paragraph",
		strings.Repeat("word ", 2000),
		conversationJSON(t, 4, 120),
		"{\"a\":1}\n{\"b\":2}\n",
	}
	for _, content := range cases {
		want := Segment(content)
		if got := SegmentWith(content, SegmentOptions{}); !equalChunks(got, want) {
			t.Fatalf("SegmentWith{} drifted from Segment for %q: %v vs %v", content, got, want)
		}
		if got := SegmentWith(content, SegmentOptions{ChunkSize: 0, StructuredChunkSize: 0}); !equalChunks(got, want) {
			t.Fatalf("explicit-zero options drifted from Segment for %q", content)
		}
	}
}

// equalChunks reports whether two chunk lists are identical.
func equalChunks(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

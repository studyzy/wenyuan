package storage

import (
	"context"
	"testing"
)

// chunkRecordingExtractor answers one fact per call and remembers the text it
// was handed, so a test can see which chunks extraction actually ran on.
type chunkRecordingExtractor struct {
	texts []string
	facts []ExtractedFact
}

func (s *chunkRecordingExtractor) Extract(_ context.Context, text string) ([]ExtractedFact, error) {
	s.texts = append(s.texts, text)
	out := make([]ExtractedFact, 0, len(s.facts))
	for _, fact := range s.facts {
		if fact.Text == "" {
			fact.Text = text
		}
		out = append(out, fact)
	}
	return out, nil
}

// A folded item's content blocks are stored as their own chunks, not one chunk
// of the joined body. The reference's retain cuts every content dict on its own
// boundaries (engine/retain/orchestrator.py:1968-1987) and numbers each from 0
// (engine/retain/fact_extraction.py:2985), so three one-sentence items of a
// synchronous retain become three chunks of one sentence each — which is what a
// recall's `chunks` channel returns, chunk_index 0, 1, 2. Segmenting the joined
// body instead handed back the whole 192-character body as chunk 0.
//
// The parts continue the numbering rather than restarting it, because they share
// one document and a per-document chunk_id must stay unique; each stored fact
// still points at the chunk it came from, which is what makes the shape agree
// with the reference's per-content chunking.
func TestRetainStoresEachContentPartAsItsOwnChunk(t *testing.T) {
	store := NewMemoryStore("public")
	parts := []string{
		"Alice works at Google on the AI team.",
		"Bob likes hiking in the Swiss Alps.",
		"The deploy pipeline uses Kubernetes.",
	}
	extractor := &chunkRecordingExtractor{facts: []ExtractedFact{{}, {}, {}}}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items: []RetainItem{{
			Content:    joinDocumentParts(parts[0], joinDocumentParts(parts[1], parts[2])),
			Parts:      parts,
			DocumentID: "doc-1",
		}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
	if len(extractor.texts) != len(parts) {
		t.Fatalf("extraction ran on %d chunks, want %d: %q", len(extractor.texts), len(parts), extractor.texts)
	}
	if len(store.chunks) != len(parts) {
		t.Fatalf("expected %d chunks, got %d: %+v", len(parts), len(store.chunks), store.chunks)
	}
	for i, chunk := range store.chunks {
		if chunk.ChunkIndex != i {
			t.Fatalf("chunk %d index = %d", i, chunk.ChunkIndex)
		}
		if chunk.ChunkText != parts[i] {
			t.Fatalf("chunk %d = %q, want %q", i, chunk.ChunkText, parts[i])
		}
	}
	// Each fact points at the chunk it came from, so the units name all three
	// chunks — a fact stored against one chunk id for every block would mean the
	// blocks were not chunked separately after all.
	chunkByID := map[string]int{}
	for _, chunk := range store.chunks {
		chunkByID[chunk.ChunkID] = chunk.ChunkIndex
	}
	seen := map[int]bool{}
	for _, unit := range store.units {
		index, ok := chunkByID[unit.ChunkID]
		if !ok {
			t.Fatalf("unit %q names chunk %q, which was not stored", unit.Text, unit.ChunkID)
		}
		seen[index] = true
	}
	for i := range parts {
		if !seen[i] {
			t.Fatalf("no fact was stored against chunk %d", i)
		}
	}
}

// A one-block item keeps the single-block behavior: its Content is chunked as
// before, so an ordinary write is unaffected.
func TestRetainWithoutPartsChunksTheContent(t *testing.T) {
	store := NewMemoryStore("public")
	extractor := &chunkRecordingExtractor{facts: []ExtractedFact{{}}}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "only one block", DocumentID: "doc-1"}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
	if len(extractor.texts) != 1 || extractor.texts[0] != "only one block" {
		t.Fatalf("extraction ran on %q, want the item's one block", extractor.texts)
	}
	if len(store.chunks) != 1 || store.chunks[0].ChunkText != "only one block" {
		t.Fatalf("expected one chunk of the item text, got %+v", store.chunks)
	}
}

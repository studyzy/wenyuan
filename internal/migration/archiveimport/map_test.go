package archiveimport

import (
	"testing"

	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// The fact→unit mapping itself carries no chunk reference: the identifier only
// exists once the document's chunks have been written, so the writer sets it
// from that write's index→identifier map. A mapped unit must therefore be
// written with the reference the caller resolved, never one of its own.
func TestUnitForFactLeavesTheChunkReferenceToTheWriter(t *testing.T) {
	chunkIndex := 3
	fact := transferarchive.Fact{
		Text:       "The deployment runs on Tuesdays.",
		FactType:   "world",
		ChunkIndex: &chunkIndex,
	}

	unit := unitFromFact("bank-a", "doc-1", 0, fact)

	if unit.ChunkID != "" {
		t.Fatalf("unit chunk id = %q, want none: the writer sets it from the chunk rows it wrote", unit.ChunkID)
	}
	if unit.DocumentID != "doc-1" {
		t.Fatalf("unit document id = %q, want the fact's document", unit.DocumentID)
	}
}

// A fact without a chunk reference is unaffected: the unit still carries its
// text and its document, which is what recall reads.
func TestUnitForFactWithoutChunkIndex(t *testing.T) {
	fact := transferarchive.Fact{Text: "Alice leads the team.", FactType: "world"}

	unit := unitFromFact("bank-a", "doc-2", 1, fact)

	if unit.ChunkID != "" {
		t.Fatalf("unit chunk id = %q, want none", unit.ChunkID)
	}
	if unit.Text != fact.Text {
		t.Fatalf("unit text = %q, want %q", unit.Text, fact.Text)
	}
}

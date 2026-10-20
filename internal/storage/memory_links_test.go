package storage

import (
	"math"
	"testing"
)

// Contract reference: memories/pg/links.py:85-146 (`_within_batch_temporal_links`)
// and :594-660 (`compute_semantic_links_ann`). A retain's temporal pass links
// every ordered pair inside a fact-type group within the window — each unit to
// its successors AND the reverse edge — and its semantic pass records a unit's
// nearest neighbours plus the unit's own self-link at weight 1, because the ANN
// probe returns the seed itself.
func TestSemanticLinksIncludeTheSelfLink(t *testing.T) {
	unit := MemoryUnit{ID: "a", BankID: "b1", Embedding: []float32{1, 0}}
	neighbour := MemoryUnit{ID: "b", BankID: "b1", Embedding: []float32{0.9, 0.1}}
	links := semanticLinksFor("b1", []MemoryUnit{unit, neighbour}, 0.7)
	// Every unit in the batch fans out, so each one records its own self-link and
	// the pair is recorded both ways — the same symmetric shape the reference's
	// per-seed ANN probe produces.
	if len(links) != 4 {
		t.Fatalf("links = %d, want two self-links and the pair both ways: %+v", len(links), links)
	}
	self := map[string]float64{}
	pair := map[string]bool{}
	for _, link := range links {
		if link.FromUnitID == link.ToUnitID {
			self[link.FromUnitID] = link.Weight
			continue
		}
		pair[link.FromUnitID+"->"+link.ToUnitID] = true
	}
	for _, id := range []string{"a", "b"} {
		weight, ok := self[id]
		if !ok || weight != 1 {
			t.Fatalf("%s self-link = (%v, ok=%v), want weight 1", id, weight, ok)
		}
	}
	if !pair["a->b"] || !pair["b->a"] {
		t.Fatalf("pairs = %v, want both directions", pair)
	}
}

// The temporal pass links a batch's same-day units in both directions.
func TestTemporalLinksLinkSuccessorsAndTheirReverse(t *testing.T) {
	store := NewMemoryStore("public")
	date := *parseTime(t, "2026-10-20T00:00:00Z")
	for _, id := range []string{"u1", "u2", "u3"} {
		store.units[id] = MemoryUnit{
			ID: id, BankID: "b1", FactType: TypeWorld,
			EventDate: &date,
		}
	}
	links := store.temporalLinksLocked("b1", []string{"u1", "u2", "u3"})
	seen := map[string]bool{}
	for _, link := range links {
		if link.LinkType != LinkTemporal {
			t.Fatalf("link type = %q, want temporal", link.LinkType)
		}
		// Same-day units clamp to the reference's 0.3 weight floor... except a
		// zero gap, which weighs 1.
		if math.Abs(link.Weight-1) > 1e-9 {
			t.Fatalf("link %s->%s weight = %v, want 1 for a zero gap", link.FromUnitID, link.ToUnitID, link.Weight)
		}
		seen[link.FromUnitID+"->"+link.ToUnitID] = true
	}
	for _, pair := range [][2]string{
		{"u1", "u2"}, {"u2", "u1"}, {"u2", "u3"}, {"u3", "u2"}, {"u1", "u3"}, {"u3", "u1"},
	} {
		if !seen[pair[0]+"->"+pair[1]] {
			t.Fatalf("missing temporal link %s->%s; got %v", pair[0], pair[1], seen)
		}
	}
	// No self-links: the reference's temporal pass never writes one.
	if seen["u1->u1"] {
		t.Fatal("temporal pass must not link a unit to itself")
	}
}

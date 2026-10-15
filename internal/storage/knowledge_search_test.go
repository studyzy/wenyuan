package storage

import (
	"context"
	"testing"
)

// knowledgeHitIDs projects a result set to its ordered identifiers.
func knowledgeHitIDs(hits []KnowledgeHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.Node.ID)
	}
	return ids
}

// putKnowledge stores one node or fails the test.
func putKnowledge(t *testing.T, store Store, node KnowledgeNode) {
	t.Helper()
	if _, err := store.PutKnowledgeNode(context.Background(), node); err != nil {
		t.Fatalf("put knowledge node %q: %v", node.ID, err)
	}
}

// The knowledge search ranks by how often the query's terms occur, matching the
// reference's ts_rank_cd arm, so a body that repeats the term outranks a
// name-exact page that carries it once. The name is not a special case: under
// the old substring scoring the name hit was pinned to 1.0 and always won.
// The reported score is a normalised rank in (0,1], the top hit landing on 1.
func TestMemoryKnowledgeSearchRanksByTermFrequency(t *testing.T) {
	store, bank := newContractStore(t)
	putKnowledge(t, store, KnowledgeNode{ID: "p-name", BankID: bank, Kind: KnowledgeKindPage, Title: "pipeline"})
	putKnowledge(t, store, KnowledgeNode{ID: "p-body", BankID: bank, Kind: KnowledgeKindPage, Title: "Deployment", Text: "run the pipeline and the pipeline again"})

	hits, err := store.SearchKnowledgeNodes(context.Background(), KnowledgeSearchQuery{BankID: bank, Query: "pipeline", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Node.ID != "p-body" {
		t.Fatalf("top hit = %q, want p-body", hits[0].Node.ID)
	}
	if hits[0].Score != 1 {
		t.Fatalf("top score = %v, want 1", hits[0].Score)
	}
	for _, hit := range hits {
		if hit.Score <= 0 || hit.Score > 1 {
			t.Fatalf("score %v out of (0,1] for %q", hit.Score, hit.Node.ID)
		}
	}
}

// The memory backend keeps no page embeddings, so it serves the text arm only:
// a query sharing no term returns nothing even when a query embedding is
// supplied, because the vector arm it cannot serve is refused rather than
// faked. A blank query is answered with no results rather than every page.
func TestMemoryKnowledgeSearchServesTextArmOnly(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	putKnowledge(t, store, KnowledgeNode{ID: "p1", BankID: bank, Kind: KnowledgeKindPage, Title: "Deployment", Text: "run the pipeline"})

	hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{
		BankID: bank, Query: "quantum", Limit: 10, Embedding: []float32{1, 0, 0},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %d, want 0", len(hits))
	}

	blank, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: bank, Query: "   ", Limit: 10})
	if err != nil {
		t.Fatalf("blank search: %v", err)
	}
	if len(blank) != 0 {
		t.Fatalf("blank hits = %d, want 0", len(blank))
	}
}

// A multi-word query matches on any of its terms: the text arm ORs its tokens
// the way the reference's BM25 arm does, so a natural-language question does
// not have to appear in a page word for word. Under the old substring scoring
// the whole query had to appear verbatim, so this returned nothing.
func TestMemoryKnowledgeSearchMatchesAnyTerm(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	putKnowledge(t, store, KnowledgeNode{ID: "p-refund", BankID: bank, Kind: KnowledgeKindPage, Title: "Refund", Text: "The refund window is thirty days."})

	hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: bank, Query: "what is the refund window?", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Node.ID != "p-refund" {
		t.Fatalf("hits = %v, want p-refund", knowledgeHitIDs(hits))
	}
}

// The ordering is stable: pages of equal term count break the tie by
// identifier, so the same query over the same corpus returns the same ranking
// regardless of the underlying map's iteration order, and the fused rank curve
// is strictly descending.
func TestMemoryKnowledgeSearchOrderIsStable(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	for _, id := range []string{"c-page", "a-page", "b-page"} {
		putKnowledge(t, store, KnowledgeNode{ID: id, BankID: bank, Kind: KnowledgeKindPage, Title: id, Text: "pipeline"})
	}
	hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: bank, Query: "pipeline", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	want := []string{"a-page", "b-page", "c-page"}
	if len(hits) != len(want) {
		t.Fatalf("hits = %d, want %d", len(hits), len(want))
	}
	for i, hit := range hits {
		if hit.Node.ID != want[i] {
			t.Fatalf("hit %d = %q, want %q", i, hit.Node.ID, want[i])
		}
		if i > 0 && hits[i-1].Score <= hit.Score {
			t.Fatalf("scores not strictly descending: %v then %v", hits[i-1].Score, hit.Score)
		}
	}
}

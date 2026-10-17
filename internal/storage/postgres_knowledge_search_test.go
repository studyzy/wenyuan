//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
)

// The PostgreSQL knowledge search is a real hybrid: it ranks a dense, short
// match above a sparse one on the text arm, reports a normalised rank score in
// (0,1], and still finds a page whose body shares no term with the query when
// that page's embedding is the query's neighbour.
func TestPostgresKnowledgeSearchHybrid(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	putKnowledge(t, store, KnowledgeNode{ID: "p-name", BankID: "vec-bank", Kind: KnowledgeKindPage, Title: "pipeline"})
	putKnowledge(t, store, KnowledgeNode{ID: "p-body", BankID: "vec-bank", Kind: KnowledgeKindPage, Title: "Deployment", Text: "run the pipeline and the pipeline again"})

	hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: "vec-bank", Query: "pipeline", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	// The reference ranks by ts_rank_cd, which scales with how often the term
	// occurs, so the body that repeats it beats the name-exact page that has it
	// once — the opposite of the old substring scoring, which pinned the name
	// hit to 1.0. A text-only search of one arm puts the top hit at 1.0.
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

	// A page whose body shares no term with the query, but whose backing mental
	// model carries the query's embedding, is found by the vector arm alone.
	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "mm-vec", BankID: "vec-bank", Subtype: SubtypePinned,
		Name: "runbook", Content: "unrelated content", Embedding: unitVector(3),
	})
	if err != nil {
		t.Fatalf("put mental model: %v", err)
	}
	putKnowledge(t, store, KnowledgeNode{
		ID: "p-vec", BankID: "vec-bank", Kind: KnowledgeKindPage,
		Title: "runbook", Text: "unrelated content", MentalModelID: model.ID,
	})
	vecHits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{
		BankID: "vec-bank", Query: "quantum", Limit: 10, Embedding: unitVector(3),
	})
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(vecHits) == 0 || vecHits[0].Node.ID != "p-vec" {
		t.Fatalf("vector hits = %v, want p-vec first", vecHits)
	}
	// A hit only one arm found tops out at 0.5: the other arm contributes
	// nothing to the fused score.
	if score := vecHits[0].Score; score <= 0 || score > 0.5+1e-9 {
		t.Fatalf("vector top score = %v, want in (0,0.5]", score)
	}

	// A multi-word question ORs its terms, so it matches a page that carries
	// only one of them; a conjunctive parse would return nothing.
	putKnowledge(t, store, KnowledgeNode{
		ID: "p-refund", BankID: "vec-bank", Kind: KnowledgeKindPage,
		Title: "Refund", Text: "The refund window is thirty days.",
	})
	questionHits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{
		BankID: "vec-bank", Query: "what is the refund window", Limit: 10,
	})
	if err != nil {
		t.Fatalf("question search: %v", err)
	}
	if len(questionHits) == 0 || questionHits[0].Node.ID != "p-refund" {
		t.Fatalf("question hits = %v, want p-refund first", knowledgeHitIDs(questionHits))
	}
}

// The two backends agree on the ordering of a text-only corpus, so a bank that
// runs on either returns the same ranking for the same query.
func TestKnowledgeSearchBackendsAgreeOnOrder(t *testing.T) {
	pg := postgresVectorStore(t)
	mem, bank := newContractStore(t)
	ctx := context.Background()
	corpus := []KnowledgeNode{
		{ID: "k1", Kind: KnowledgeKindPage, Title: "pipeline"},
		{ID: "k2", Kind: KnowledgeKindPage, Title: "Deployment", Text: "run the pipeline and the pipeline again"},
		{ID: "k3", Kind: KnowledgeKindPage, Title: "Unrelated", Text: "nothing relevant to see in this document at all here"},
	}
	for _, node := range corpus {
		pgNode := node
		pgNode.BankID = "vec-bank"
		putKnowledge(t, pg, pgNode)
		memNode := node
		memNode.BankID = bank
		putKnowledge(t, mem, memNode)
	}
	pgHits, err := pg.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: "vec-bank", Query: "pipeline", Limit: 10})
	if err != nil {
		t.Fatalf("postgres search: %v", err)
	}
	memHits, err := mem.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: bank, Query: "pipeline", Limit: 10})
	if err != nil {
		t.Fatalf("memory search: %v", err)
	}
	pgOrder := knowledgeHitIDs(pgHits)
	memOrder := knowledgeHitIDs(memHits)
	if len(pgOrder) != len(memOrder) {
		t.Fatalf("postgres order %v != memory order %v", pgOrder, memOrder)
	}
	for i := range pgOrder {
		if pgOrder[i] != memOrder[i] {
			t.Fatalf("postgres order %v != memory order %v", pgOrder, memOrder)
		}
	}
}

// With `enable_text_search` off the keyword arm is dropped: the search ranks on
// the vector arm alone, and with no query embedding either it returns nothing
// rather than a ranking off an index the bank switched off.
func TestPostgresKnowledgeSearchTextArmDisabled(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	putKnowledge(t, store, KnowledgeNode{ID: "p-body", BankID: "vec-bank", Kind: KnowledgeKindPage, Title: "Deployment", Text: "run the pipeline"})

	hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{
		BankID: "vec-bank", Query: "pipeline", Limit: 10, DisableTextArm: true,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %d, want none without an embedding and the text arm off", len(hits))
	}
}

// The tag predicate is bound into each arm's WHERE, so a tagged page that ranks
// below untagged pages is still returned when the limit is 1 — the filter is
// part of the search, not a post-ranking drop that a short window can starve.
func TestPostgresKnowledgeSearchTagFilterPreRanking(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	putKnowledge(t, store, KnowledgeNode{ID: "p-high", BankID: "vec-bank", Kind: KnowledgeKindPage,
		Title: "Deployment", Text: "pipeline pipeline pipeline"})
	putKnowledge(t, store, KnowledgeNode{ID: "p-tagged", BankID: "vec-bank", Kind: KnowledgeKindPage,
		Title: "Tagged", Text: "pipeline", Tags: []string{"keep"}})

	hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{
		BankID: "vec-bank", Query: "pipeline", Limit: 1,
		Tags: []string{"keep"}, TagsMatch: TagsMatchAllStrict,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Node.ID != "p-tagged" {
		t.Fatalf("filtered hits = %v, want [p-tagged]", knowledgeHitIDs(hits))
	}
}

// A corrupted parent cycle (a tree written before the walk was guarded, or
// restored from such an export) must not spin the recursive subtree walk: the
// delete terminates on the first repeat and removes the reachable rows.
func TestPostgresKnowledgeDeleteSubtreeCycleSafe(t *testing.T) {
	store := postgresVectorStore(t)
	pg := store.(*PostgresStore)
	ctx := context.Background()
	putKnowledge(t, store, KnowledgeNode{ID: "cyc-a", BankID: "vec-bank", Kind: KnowledgeKindFolder, Title: "a"})
	putKnowledge(t, store, KnowledgeNode{ID: "cyc-b", BankID: "vec-bank", Kind: KnowledgeKindFolder, Title: "b", ParentID: "cyc-a"})
	if _, err := pg.db.ExecContext(ctx,
		`UPDATE `+quoteIdent(pg.schema)+`.knowledge_pages SET parent_id = $1 WHERE bank_id = $2 AND id = $3`,
		"cyc-b", "vec-bank", "cyc-a"); err != nil {
		t.Fatalf("introduce cycle: %v", err)
	}
	if err := store.DeleteKnowledgeNode(ctx, "vec-bank", "cyc-a"); err != nil {
		t.Fatalf("delete cyclic subtree: %v", err)
	}
	if _, err := store.GetKnowledgeNode(ctx, "vec-bank", "cyc-b"); !errors.Is(err, ErrKnowledgeNodeNotFound) {
		t.Fatalf("cyc-b still present after cascade: %v", err)
	}
}

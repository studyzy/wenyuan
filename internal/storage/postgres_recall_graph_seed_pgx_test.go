//go:build pgx

package storage

import (
	"context"
	"testing"
)

// A lexical hit does not seed the graph walk. With no stored embeddings the
// dense arm surfaces nothing, so the graph arm has no entry points and a unit
// that merely shares an entity with the lexical hit is not pulled into the
// results — the reference derives its entry points from the semantic arm alone
// (engine/memories/pg/recall.py's `graph_seeds`).
func TestPostgresRecallGraphArmSeedsOnlyFromDenseHits(t *testing.T) {
	store := postgresVectorStore(t)
	pg := store.(*PostgresStore)
	ctx := context.Background()
	const bank = "w15-graph-seeds"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	const entity = "33333333-3333-3333-3333-333333333333"
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(pg.schema)+`.entities (id, canonical_name, bank_id) VALUES ($1::uuid,$2,$3)`,
		entity, "Lisken", bank); err != nil {
		t.Fatalf("insert entity: %v", err)
	}
	// Neither unit carries an embedding, so the dense arm has nothing to seed
	// from and only the keyword arm can surface the lexical hit.
	hit := MemoryUnit{ID: newID(), BankID: bank, Text: "Zulmar faxed the Lisken report.", FactType: TypeWorld}
	linked := MemoryUnit{ID: newID(), BankID: bank, Text: "The Kelbrun annex houses archive boxes.", FactType: TypeWorld}
	for _, unit := range []MemoryUnit{hit, linked} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatalf("upsert unit: %v", err)
		}
		if err := store.UpsertUnitEntity(ctx, unit.ID, entity); err != nil {
			t.Fatalf("post entity: %v", err)
		}
	}
	result, err := store.Recall(ctx, RecallQuery{BankID: bank, Query: "Zulmar Lisken report", Limit: 10})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	recalled := map[string]bool{}
	for _, match := range result.Matches {
		recalled[match.ID] = true
	}
	if !recalled[hit.ID] {
		t.Fatalf("the lexical hit must be recalled: %v", recalled)
	}
	if recalled[linked.ID] {
		t.Fatalf("a unit sharing an entity with a lexical hit must not be pulled in without a dense seed: %v", recalled)
	}
}

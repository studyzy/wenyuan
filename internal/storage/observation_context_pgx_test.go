//go:build pgx

package storage

import (
	"context"
	"testing"
)

// An observation's context column is NULL, and its recall result says so.
//
// The reference's observation insert never names the context column
// (engine/memories/pg/consolidation.py:268-320), so the row carries NULL and a
// recall result reports `context: null`, while a fact with no context reports
// "". Folding NULL to "" on the way in — or on the way out — makes the two
// indistinguishable on the wire.
func TestPostgresObservationContextIsNull(t *testing.T) {
	store := postgresVectorStore(t).(*PostgresStore)
	ctx := context.Background()
	const bank = "observation-context-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "Context"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}

	fact, err := store.UpsertUnit(ctx, MemoryUnit{
		BankID: bank, Text: "the deployment runs on Tuesdays", FactType: TypeWorld,
	})
	if err != nil {
		t.Fatalf("write the source fact: %v", err)
	}
	observationID, err := store.CreateObservation(ctx, bank, "deployments happen weekly", []string{fact.ID})
	if err != nil {
		t.Fatalf("create the observation: %v", err)
	}

	var isNull bool
	if err := store.db.QueryRowContext(ctx,
		`SELECT context IS NULL FROM `+quoteIdent(store.schema)+`.memory_units WHERE id = $1::uuid`,
		observationID).Scan(&isNull); err != nil {
		t.Fatalf("read the stored context: %v", err)
	}
	if !isNull {
		t.Fatal("the observation's context column is not NULL")
	}

	result, err := store.Recall(ctx, RecallQuery{BankID: bank, Types: []string{string(TypeObservation)}, Limit: 10})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	found := false
	for _, match := range result.Matches {
		if match.ID != observationID {
			continue
		}
		found = true
		if !match.ContextAbsent {
			t.Fatalf("the recalled observation reports context %q as present", match.Context)
		}
	}
	if !found {
		t.Fatalf("recall did not return the observation (%d matches)", len(result.Matches))
	}
}

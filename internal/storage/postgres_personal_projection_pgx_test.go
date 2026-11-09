//go:build pgx

package storage

import (
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// TestProjectionCarriesRetrievalColumns pins what an aggregate row is written
// with. The aggregate bank is a materialized projection and calls no model, so
// a vector it needs can only come from the source row it mirrors: a projection
// that dropped the embedding and the search_vector left every world fact
// invisible to the semantic arm and to a keyword query, which is what an
// aggregate read as empty results for.
func TestProjectionCarriesRetrievalColumns(t *testing.T) {
	f := newPersonalFixture(t)
	ctx := f.ctx(t)
	source := f.bank(t, "acme")
	// The source fact carries both a vector and a context, so the projection has
	// something of each to carry.
	vector := make([]float32, EmbeddingDimensions)
	vector[0] = 1
	var unitID string
	if err := f.db.QueryRowContext(ctx, `
		INSERT INTO `+quoteIdent(f.persona)+`.memory_units (bank_id, text, fact_type, context, embedding)
		VALUES ($1,'the deploy runs on kubernetes','world','ops runbook',$2::vector)
		RETURNING id::text`, source, EncodeEmbedding(vector)).Scan(&unitID); err != nil {
		t.Fatalf("insert source fact: %v", err)
	}

	input, ran, stamp := f.round(t, 10)
	if !ran {
		t.Fatal("the round did not run")
	}
	if len(input.Facts) != 1 {
		t.Fatalf("round read %d facts, want 1", len(input.Facts))
	}
	sourceFact := input.Facts[0]
	if len(sourceFact.Embedding) != EmbeddingDimensions || sourceFact.Context == nil {
		t.Fatalf("round read embedding of %d dims and context %v; want both carried",
			len(sourceFact.Embedding), sourceFact.Context)
	}

	f.finish(t, FinishPersonalRequest{
		TenantID: input.TenantID, Schema: f.persona, BankID: input.BankID, Stamp: stamp,
		Insert: []PersonalPreviousFact{{
			Text: sourceFact.Text, MergeKey: "k1", FactType: sourceFact.FactType,
			SourceUnitID: sourceFact.UnitID, Context: sourceFact.Context,
			Embedding: sourceFact.Embedding,
		}},
		Sources: []PersonalSourceRow{{UnitID: sourceFact.UnitID, MergeKey: "k1", BankID: source}},
		Cursors: map[string]MergeCursor{source: {UpdatedAt: sourceFact.UpdatedAt, UnitID: unitID}},
	})

	var embedded bool
	var contextText *string
	var matchesKeyword bool
	if err := f.db.QueryRowContext(ctx, `
		SELECT embedding IS NOT NULL, context,
		       search_vector @@ to_tsquery('english', 'kubernetes')
		  FROM `+quoteIdent(f.persona)+`.memory_units
		 WHERE bank_id = $1 AND fact_type = 'world'`,
		tenant.PersonalBankID(f.persona)).Scan(&embedded, &contextText, &matchesKeyword); err != nil {
		t.Fatalf("read projected row: %v", err)
	}
	if !embedded {
		t.Error("projected row has no embedding")
	}
	if contextText == nil || *contextText != "ops runbook" {
		t.Errorf("projected context = %v, want the source's", contextText)
	}
	if !matchesKeyword {
		t.Error("projected row's search_vector does not answer a keyword query on its text")
	}

	// The round's claim is what the second call contends with; releasing it is
	// the store's own job, so this only proves the first round left no claim
	// behind that would block the next tick.
	if _, err := f.store.BeginPersonalRound(ctx, f.persona, 10, time.Minute,
		func(PersonalRoundInput, time.Time) error { return nil }); err != nil {
		t.Fatalf("second begin: %v", err)
	}
}

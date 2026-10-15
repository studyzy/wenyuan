//go:build pgx

package storage

import (
	"context"
	"reflect"
	"testing"
)

// The PostgreSQL reflection retrieval must answer each layer from the store's
// own tables. This exercises the SQL against a live database; it is skipped
// unless WENYUAN_TEST_DATABASE_URL is set. The offline contract assertion
// (dispatch to a supplied runner) lives in postgres_reflect_test.go.
func TestPostgresReflectRetrievalQueries(t *testing.T) {
	store := postgresVectorStore(t)
	pg := store.(*PostgresStore)
	ctx := context.Background()
	const bank = "vec-bank"

	rawID := pgUUID("reflect-raw")
	obsID := pgUUID("reflect-obs")
	docID := pgUUID("reflect-doc")
	factID := pgUUID("reflect-fact")
	mmID := pgUUID("reflect-mm")

	if _, err := store.PutDocument(ctx, bank, docID, Document{OriginalText: "the whole source document"}); err != nil {
		t.Fatalf("put document: %v", err)
	}
	// memory_units.chunk_id is a foreign key onto chunks in the reference
	// schema, so the provenance the fact carries has to exist as a chunk row
	// before the unit can name it.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(pg.schema)+`.chunks (chunk_id, bank_id, document_id, chunk_index, chunk_text)
		 VALUES ('chunk-1', $1, $2, 0, 'the whole source document')`, bank, docID); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	units := []MemoryUnit{
		{ID: rawID, BankID: bank, Text: "alpha raw memory", FactType: TypeWorld},
		{ID: obsID, BankID: bank, Text: "alpha consolidated observation", FactType: TypeObservation},
		{ID: factID, BankID: bank, Text: "fact lacking inline context", FactType: TypeWorld, DocumentID: docID, ChunkID: "chunk-1"},
	}
	for _, unit := range units {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatalf("upsert %s: %v", unit.Text, err)
		}
	}
	if _, err := store.PutMentalModel(ctx, MentalModel{
		ID: mmID, BankID: bank, Subtype: SubtypePinned, Name: "Page", Content: "alpha perspective page",
	}); err != nil {
		t.Fatalf("put mental model: %v", err)
	}

	observations, err := pg.ReflectObservations(ctx, bank, "alpha", 0)
	if err != nil {
		t.Fatalf("observations: %v", err)
	}
	if ids := citationIDs(observations); !reflect.DeepEqual(ids, []string{obsID}) {
		t.Errorf("observations = %v, want only the observation", ids)
	}

	memories, err := pg.ReflectMemories(ctx, bank, "alpha", 0)
	if err != nil {
		t.Fatalf("memories: %v", err)
	}
	memIDs := citationIDs(memories)
	if !containsString(memIDs, rawID) {
		t.Errorf("memories = %v, want the raw memory", memIDs)
	}
	if containsString(memIDs, obsID) {
		t.Errorf("memories = %v, must exclude observations", memIDs)
	}

	models, err := pg.ReflectMentalModels(ctx, bank, "alpha", 0)
	if err != nil {
		t.Fatalf("mental models: %v", err)
	}
	if len(models) != 1 || models[0].ID != mmID || models[0].Text != "alpha perspective page" {
		t.Errorf("mental models = %+v, want the seeded page", models)
	}

	read, err := pg.ReflectReadModels(ctx, bank, []string{mmID, pgUUID("reflect-missing")})
	if err != nil {
		t.Fatalf("read models: %v", err)
	}
	if len(read) != 1 || read[0].ID != mmID || read[0].Text != "alpha perspective page" {
		t.Errorf("read models = %+v, want only the known page", read)
	}

	expanded, err := pg.ReflectExpand(ctx, ReflectExpandRequest{BankID: bank, IDs: []string{factID}, Depth: "document"})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(expanded) != 1 {
		t.Fatalf("expand returned %d citations, want 1", len(expanded))
	}
	if expanded[0].Text != "the whole source document" {
		t.Errorf("expanded text = %q, want the source document", expanded[0].Text)
	}
	if expanded[0].DocumentID != docID || expanded[0].ChunkID != "chunk-1" {
		t.Errorf("expanded provenance = (%q,%q), want (%q,chunk-1)", expanded[0].DocumentID, expanded[0].ChunkID, docID)
	}
}

// With no catalogue wired, the agentic dispatch reads the bank's stored
// directives itself so the loop still follows them.
func TestPostgresReflectLoadsBankDirectives(t *testing.T) {
	store := postgresVectorStore(t)
	pg := store.(*PostgresStore)
	ctx := context.Background()

	if _, err := store.PutDirective(ctx, Directive{
		ID: pgUUID("reflect-directive"), BankID: "vec-bank", Name: "Terse", Content: "Always be terse.", IsActive: true,
	}); err != nil {
		t.Fatalf("put directive: %v", err)
	}
	runner := &capturingReflectRunner{}
	if _, err := pg.Reflect(ctx, ReflectQuery{BankID: "vec-bank", Query: "q", Reflection: runner}); err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if !reflect.DeepEqual(runner.req.Directives, []string{"Always be terse."}) {
		t.Errorf("directives = %q, want the stored directive", runner.req.Directives)
	}
}

// citationIDs lists a citation set's identifiers in order.
func citationIDs(citations []ReflectCitation) []string {
	ids := make([]string, 0, len(citations))
	for _, citation := range citations {
		ids = append(ids, citation.ID)
	}
	return ids
}

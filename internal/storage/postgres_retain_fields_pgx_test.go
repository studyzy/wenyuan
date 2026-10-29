//go:build pgx

package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The PostgreSQL retain write must populate every durable unit column, not just
// the text and occurred_start. This asserts the fixed INSERT against a live
// database; it is skipped unless WENYUAN_TEST_DATABASE_URL is set, which is why
// the offline contract assertion lives in postgres_retain_fields_test.go.
func TestPostgresRetainUnitPersistsDurableFields(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	mentioned := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	eventDate := time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC)
	occurredEnd := time.Date(2026, 2, 27, 18, 30, 0, 0, time.UTC)
	unit := MemoryUnit{
		ID:          pgUUID("retain-fields-unit"),
		BankID:      "vec-bank",
		Text:        "Alice hiked the ridge on Friday.",
		FactType:    TypeExperience,
		Context:     "a planning conversation about the weekend",
		MentionedAt: &mentioned,
		EventDate:   &eventDate,
		OccurredEnd: &occurredEnd,
		Metadata:    map[string]any{"where": "the ridge"},
	}
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatalf("upsert unit: %v", err)
	}

	var (
		context     *string
		metadataRaw []byte
		gotMention  *time.Time
		gotEvent    *time.Time
		gotEnd      *time.Time
	)
	query := `SELECT context, metadata, mentioned_at, event_date, occurred_end
	          FROM ` + quoteIdent(store.(*PostgresStore).defaultSchema) + `.memory_units WHERE id = $1`
	if err := store.(*PostgresStore).db.QueryRowContext(ctx, query, pgUUID("retain-fields-unit")).
		Scan(&context, &metadataRaw, &gotMention, &gotEvent, &gotEnd); err != nil {
		t.Fatalf("read back unit: %v", err)
	}
	if context == nil || *context != unit.Context {
		t.Errorf("context = %v, want %q", context, unit.Context)
	}
	if gotMention == nil || !gotMention.Equal(mentioned) {
		t.Errorf("mentioned_at = %v, want %v", gotMention, mentioned)
	}
	if gotEvent == nil || !gotEvent.Equal(eventDate) {
		t.Errorf("event_date = %v, want %v", gotEvent, eventDate)
	}
	if gotEnd == nil || !gotEnd.Equal(occurredEnd) {
		t.Errorf("occurred_end = %v, want %v", gotEnd, occurredEnd)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataRaw, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["where"] != "the ridge" {
		t.Errorf("metadata = %#v, want the caller's map", metadata)
	}
}

// The store-text opt-out reaches the SQL write: the document's raw text and
// every chunk's text are kept out of the columns, while the content hashes are
// still computed from the real text so delta dedup is unaffected. This is the
// PostgreSQL counterpart of TestDiscardDocumentTextKeepsHashes.
func TestPostgresDiscardDocumentText(t *testing.T) {
	pg, schema := newClearMemoriesStore(t, "store_text")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	content := "first paragraph\n\n" + strings.Repeat("x", ChunkSize) + "\n\nthird paragraph"
	if _, err := pg.RetainWrite(ctx, RetainRequest{
		BankID:              "b1",
		Items:               []RetainItem{{Content: content, DocumentID: "d1"}},
		DiscardDocumentText: true,
	}); err != nil {
		t.Fatalf("retain: %v", err)
	}

	doc, err := pg.GetDocument(ctx, "b1", "d1")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if doc.OriginalText != "" {
		t.Fatalf("raw text must not be stored, got %q", doc.OriginalText)
	}
	if doc.ContentHash != ContentHash(content) {
		t.Fatalf("content hash must come from the real text")
	}

	// The column itself is NULL — the reference's representation for a withheld
	// body, which the read surfaces coalesce into a null original_text.
	var stored *string
	if err := pg.db.QueryRowContext(ctx,
		`SELECT original_text FROM `+quoteIdent(schema)+
			`.documents WHERE bank_id = 'b1' AND id = 'd1'`).Scan(&stored); err != nil {
		t.Fatalf("read stored text: %v", err)
	}
	if stored != nil {
		t.Fatalf("original_text must be stored as NULL, got %q", *stored)
	}

	page, err := pg.ListChunks(ctx, ChunkQuery{BankID: "b1", DocumentID: "d1", Limit: 50})
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(page.Chunks) < 2 {
		t.Fatalf("the write must still chunk the document, got %d chunks", len(page.Chunks))
	}
	real := SegmentWith(content, SegmentOptions{})
	for i, chunk := range page.Chunks {
		if chunk.ChunkText != "" {
			t.Fatalf("chunk %d text must not be stored, got %q", i, chunk.ChunkText)
		}
		if i < len(real) && chunk.ContentHash != ContentHash(real[i]) {
			t.Fatalf("chunk %d hash must come from the real text", i)
		}
	}
}

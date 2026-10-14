//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// referenceSchema is the shape the reference database actually has, narrowed
// to the columns the direct reader touches. It is a fixture for the reader's
// SQL: the columns, names and defaults are the reference's own, so a query that
// drifts from the reference fails here rather than only in a real migration.
const referenceSchema = `
CREATE TABLE documents (
    id            TEXT NOT NULL,
    bank_id       TEXT NOT NULL,
    original_text TEXT,
    retain_params JSONB DEFAULT '{}'::jsonb,
    tags          VARCHAR(255)[] DEFAULT '{}'::character varying[],
    created_at    TIMESTAMPTZ DEFAULT now(),
    updated_at    TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (id, bank_id)
);
CREATE TABLE chunks (
    chunk_id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id TEXT NOT NULL,
    bank_id     TEXT NOT NULL,
    chunk_index INT NOT NULL,
    chunk_text  TEXT NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now(),
    content_hash TEXT
);
CREATE TABLE memory_units (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bank_id      TEXT NOT NULL,
    document_id  TEXT,
    text         TEXT NOT NULL,
    context      TEXT,
    event_date   TIMESTAMPTZ,
    occurred_start TIMESTAMPTZ,
    occurred_end TIMESTAMPTZ,
    mentioned_at TIMESTAMPTZ,
    fact_type    TEXT NOT NULL DEFAULT 'world',
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now(),
    chunk_id     TEXT
);
`

// test-direct-read: a bank is read straight out of a reference-shaped database
// and rendered as an archive whose documents, chunks and facts carry the
// referenced values, with the reference's column names translated.
func TestDirectReadRendersArchive(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the direct-read test was not run")
	}
	db, err := OpenDatabase(url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("ref_e2e_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// The schema is dropped on a context of its own, and the pool closed after
	// it, because a deferred cancel and close run before a registered cleanup:
	// dropping on the test's context would execute against a cancelled context
	// and a closed pool, and the discarded error would hide it.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = db.Close()
	})
	if _, err := db.ExecContext(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	for _, statement := range splitStatements(referenceSchema) {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create reference schema: %v", err)
		}
	}
	seed := []string{
		`INSERT INTO documents (id, bank_id, original_text, created_at) VALUES ('d1','src-bank','about Alice', now())`,
		`INSERT INTO chunks (chunk_id, bank_id, document_id, chunk_index, chunk_text)
		   VALUES (gen_random_uuid(),'src-bank','d1',0,'about Alice')`,
		`INSERT INTO memory_units (id, bank_id, document_id, text, fact_type, metadata)
		   VALUES (gen_random_uuid(),'src-bank','d1','Alice works at Google','world','{"k":"v"}')`,
		`INSERT INTO memory_units (id, bank_id, document_id, text, fact_type)
		   VALUES (gen_random_uuid(),'src-bank','d1','Alice prefers tea','opinion')`,
	}
	for _, statement := range seed {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	archive, err := NewDirectSource(db, "src-bank").Read(ctx)
	if err != nil {
		t.Fatalf("direct read: %v", err)
	}
	if len(archive.Documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(archive.Documents))
	}
	doc := archive.Documents[0]
	if doc.ID != "d1" || doc.OriginalText != "about Alice" {
		t.Fatalf("document = %+v", doc)
	}
	if len(doc.Chunks) != 1 || doc.Chunks[0].ChunkText != "about Alice" {
		t.Fatalf("chunks = %+v", doc.Chunks)
	}
	if len(doc.Facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(doc.Facts))
	}
	types := map[string]string{}
	for _, fact := range doc.Facts {
		types[fact.Text] = fact.FactType
	}
	if types["Alice works at Google"] != "world" || types["Alice prefers tea"] != "opinion" {
		t.Fatalf("fact types = %+v", types)
	}
	for _, fact := range doc.Facts {
		if fact.Text == "Alice works at Google" && fact.Metadata["k"] != "v" {
			t.Fatalf("fact metadata = %+v", fact.Metadata)
		}
	}
}

// splitStatements splits the fixture DDL into separate statements.
func splitStatements(ddl string) []string {
	var statements []string
	for _, part := range strings.Split(ddl, ";") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			statements = append(statements, trimmed)
		}
	}
	return statements
}

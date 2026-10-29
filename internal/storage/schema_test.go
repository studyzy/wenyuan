package storage

import (
	"regexp"
	"strings"
	"testing"
)

// The bank and memory-unit core columns exist with the reference names.
func TestBankAndMemoryUnitColumnContracts(t *testing.T) {
	for _, name := range []string{
		"bank_id", "name", "disposition", "mission",
	} {
		if !containsString(BanksColumns, name) {
			t.Fatalf("banks column contract is missing %q", name)
		}
	}
	for _, name := range []string{
		"text", "fact_type", "proof_count",
		"occurred_start", "occurred_end", "mentioned_at", "metadata",
		"context", "event_date", "source_memory_ids",
	} {
		if !containsString(MemoryUnitsColumns, name) {
			t.Fatalf("memory_units column contract is missing %q", name)
		}
	}
}

// The memory-domain table set is exactly the documented set and is owned by
// the storage area.
func TestMemoryDomainTableInventory(t *testing.T) {
	expected := []string{
		"banks", "bank_aliases", "documents", "chunks", "entities", "memory_units",
		"invalidated_memory_units", "unit_entities", "memory_links", "entity_cooccurrences",
		"entity_maintenance_queue", "graph_maintenance_queue", "async_operations",
		"file_storage", "mental_models", "mental_model_history", "observation_history",
		"directives", "audit_log", "llm_requests", "knowledge_pages",
		"webhooks", "attachments", "bank_stats_cache",
	}
	if len(MemoryDomainTables) != len(expected) {
		t.Fatalf("table count = %d, want %d", len(MemoryDomainTables), len(expected))
	}
	for _, name := range expected {
		if !OwnsTable(name) {
			t.Fatalf("storage area must own table %q", name)
		}
	}
	if OwnsTable("not_a_table") {
		t.Fatal("OwnsTable must reject unknown tables")
	}
}

func TestSortedTablesAreStable(t *testing.T) {
	sorted := SortedMemoryDomainTables()
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1] > sorted[i] {
			t.Fatalf("tables are not sorted: %v", sorted)
		}
	}
}

// ts-0008 / ts-0021 / cmp-0004: the documents, chunks, entities and
// async_operations column contracts carry the documented columns.
func TestRetainColumnContracts(t *testing.T) {
	cases := map[string]struct {
		columns []string
		names   []string
	}{
		"documents":        {DocumentsColumns, []string{"original_text", "content_hash", "retain_params", "tags"}},
		"chunks":           {ChunksColumns, []string{"chunk_id", "bank_id", "document_id", "chunk_index", "chunk_text", "content_hash"}},
		"entities":         {EntitiesColumns, []string{"canonical_name", "bank_id", "metadata", "first_seen", "last_seen", "mention_count", "entity_kind"}},
		"async_operations": {AsyncOperationsColumns, []string{"operation_id", "bank_id", "operation_type", "status", "created_at", "updated_at", "completed_at", "error_message", "result_metadata"}},
	}
	for table, tc := range cases {
		for _, name := range tc.names {
			if !containsString(tc.columns, name) {
				t.Fatalf("%s column contract is missing %q", table, name)
			}
		}
	}
}

// migrationSeriesSQL is the whole series as one lowercased string, which is what
// a "the series defines X" assertion reads. Comments are kept: a statement the
// series deliberately does not make (an illegal index it must not create) is
// stated in the statement's own comment.
func migrationSeriesSQL(t *testing.T) string {
	t.Helper()
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	var sql strings.Builder
	for _, migration := range series {
		sql.WriteString(strings.ToLower(migration.SQL))
		sql.WriteByte('\n')
	}
	return sql.String()
}

// The scheduled-full-refresh schema of the reference's d4f7a2c9e1b5: the
// column the schedule reads its own clock from, the partial index over it, and
// the discovery routine that now also returns full-refresh-only models.
func TestMigrationDefinesScheduledFullRefresh(t *testing.T) {
	if !containsString(MentalModelsColumns, "last_full_refreshed_at") {
		t.Fatal("mental_models column contract is missing last_full_refreshed_at")
	}
	sql := migrationSeriesSQL(t)
	for _, needle := range []string{
		"last_full_refreshed_at timestamp with time zone",
		"create index " + MentalModelsFullRefreshIndex,
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("the migration series must define %q", needle)
		}
	}
	// The routine's result type gained the two full-refresh columns, which
	// CREATE OR REPLACE cannot do — so it is dropped and recreated. A series
	// that kept the replace would fail at run time.
	if !strings.Contains(sql, "drop function if exists mental_models_with_cron()") {
		t.Fatal("the series must drop mental_models_with_cron() before recreating its wider result type")
	}
	if !strings.Contains(sql, "full_refresh_cron text, last_full_refreshed_at timestamp with time zone") {
		t.Fatal("the series' mental_models_with_cron() must return the full-refresh columns")
	}
}

// The reference's e8c1a4f7b2d9: a per-bank backend serves every memory_units
// vector search from the idx_mu_emb_* partial indexes, so the schema carries no
// global vector index on memory_units. The plan the sweep applies is built on
// exactly that assumption.
func TestMigrationDoesNotBuildAGlobalMemoryUnitsVectorIndex(t *testing.T) {
	sql := migrationSeriesSQL(t)
	if regexp.MustCompile(`\bcreate index\b[^;]*\bon memory_units using hnsw`).MatchString(sql) {
		t.Fatalf("the series must not build a global vector index on memory_units (%s)", MemoryUnitsGlobalVectorIndexIllegal)
	}
	// The mental-model page search has no per-bank index to fall back on, so its
	// global index stays.
	if !regexp.MustCompile(`\bcreate index idx_mental_models_embedding\b[^;]*using hnsw`).MatchString(sql) {
		t.Fatal("the series must keep the global hnsw index on mental_models.embedding")
	}
}

// The reference's f2a9c4e7b1d3: the two columns pointing at mental_models.id
// are TEXT, because a longer id could be created but never refreshed.
func TestMentalModelIDReferencesAreText(t *testing.T) {
	sql := migrationSeriesSQL(t)
	for table, column := range MentalModelIDReferenceColumns {
		if regexp.MustCompile(`\b` + column + ` character varying\(64\)`).MatchString(sql) {
			t.Fatalf("%s.%s is declared character varying(64); the reference widens it to text", table, column)
		}
	}
	if !containsString(KnowledgePagesColumns, "mental_model_id") {
		t.Fatal("knowledge_pages column contract is missing mental_model_id")
	}
}

// cmp-0004 / cmp-0005: the migration series defines the retain pipeline columns
// and narrows the fact-type constraint to the final set.
func TestMigrationDefinesRetainPipeline(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, migration := range series {
		sql += migration.SQL + "\n"
	}
	for _, needle := range []string{
		"retain_params", "content_hash", "original_text", "mention_count",
		"operation_type", "error_message", "result_metadata", "retry_count",
		"next_retry_at", "completed_at",
	} {
		if !containsFold(sql, needle) {
			t.Fatalf("the migration series must define %q", needle)
		}
	}
}

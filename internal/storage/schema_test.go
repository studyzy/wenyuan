package storage

import "testing"

// ts-0029: the bank and memory-unit core columns exist with the expected names.
func TestBankAndMemoryUnitColumnContracts(t *testing.T) {
	for _, name := range []string{
		"id", "name",
		"disposition_skepticism", "disposition_literalism", "disposition_empathy",
		"reflect_mission",
	} {
		if !containsString(BanksColumns, name) {
			t.Fatalf("banks column contract is missing %q", name)
		}
	}
	for _, name := range []string{
		"text", "type",
		"occurred_start", "occurred_end", "mentioned_at", "metadata",
		"context", "event_date", "confidence_score", "access_count",
	} {
		if !containsString(MemoryUnitsColumns, name) {
			t.Fatalf("memory_units column contract is missing %q", name)
		}
	}
}

// ts-0019 / neg-0004 / inv-0005: the memory-domain table set is exactly the
// documented set and is owned by the storage area.
func TestMemoryDomainTableInventory(t *testing.T) {
	expected := []string{
		"banks", "bank_aliases", "documents", "chunks", "entities", "memory_units",
		"unit_entities", "memory_links", "entity_cooccurrences", "async_operations",
		"file_storage", "mental_models", "directives",
		"audit_log", "llm_requests", "observation_sources", "knowledge_nodes",
		"webhooks", "webhook_deliveries", "attachments", "bank_stats_cache",
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
		"documents":        {DocumentsColumns, []string{"original_text", "content_hash", "metadata", "retain_params"}},
		"chunks":           {ChunksColumns, []string{"id", "bank_id", "document_id", "index", "text", "content_hash"}},
		"entities":         {EntitiesColumns, []string{"name", "bank_id", "metadata", "first_seen", "last_seen", "mention_count", "kind"}},
		"async_operations": {AsyncOperationsColumns, []string{"id", "bank_id", "operation_type", "status", "created_at", "updated_at", "completed_at", "error_message", "result_metadata"}},
	}
	for table, tc := range cases {
		for _, name := range tc.names {
			if !containsString(tc.columns, name) {
				t.Fatalf("%s column contract is missing %q", table, name)
			}
		}
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
		"next_retry_at", "completed_at", "progress",
	} {
		if !containsFold(sql, needle) {
			t.Fatalf("the migration series must define %q", needle)
		}
	}
}

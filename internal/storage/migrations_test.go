package storage

import (
	"strings"
	"testing"
)

// TestMigrationsSplitIntoValidStatements guards the migration series against a
// defect that only surfaces when the migrations actually run.
//
// The splitter breaks the series on semicolons, so a semicolon inside a comment
// that sits in the middle of a statement splits that statement in half and the
// database rejects the fragment. A compile-only check cannot see this, which is
// why the guard is here rather than left to review.
func TestMigrationsSplitIntoValidStatements(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatalf("read the migration series: %v", err)
	}
	if len(series) == 0 {
		t.Fatal("the migration series is empty")
	}
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			trimmed := strings.TrimSpace(statement)
			if trimmed == "" {
				t.Fatalf("migration %04d produced an empty statement", migration.Version)
			}
			// Every statement the series carries must be a complete statement
			// starting with a keyword the database accepts. A fragment that
			// starts mid-sentence is the signature of a comment semicolon.
			upper := strings.ToUpper(trimmed)
			if !startsWithKeyword(upper) {
				t.Fatalf("migration %04d produced a statement fragment that would be rejected: %q",
					migration.Version, firstLine(trimmed))
			}
		}
	}
}

// startsWithKeyword reports whether a statement begins with a SQL keyword the
// series uses.
func startsWithKeyword(statement string) bool {
	for _, keyword := range []string{
		"CREATE", "ALTER", "DROP", "INSERT", "UPDATE", "DELETE", "SET", "GRANT", "DO", "COMMENT",
	} {
		if strings.HasPrefix(statement, keyword) {
			return true
		}
	}
	return false
}

// firstLine renders a statement's first line for a failure message.
func firstLine(statement string) string {
	if index := strings.IndexByte(statement, '\n'); index >= 0 {
		return statement[:index]
	}
	if len(statement) > 120 {
		return statement[:120]
	}
	return statement
}

// TestDeclaredColumnsExistInTheSchemaInventory cross-checks the column
// declarations against the migrations that create them.
//
// A column declared in the inventory but created by no migration is a phantom:
// a read that selects it fails on a fresh database, and the defect is invisible
// until the query runs. This guard is a coarse check rather than a full parse —
// it asserts that every declared column is mentioned somewhere in the series —
// which is enough to catch a column added to the inventory and nowhere else.
func TestDeclaredColumnsExistInTheSchemaInventory(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatalf("read the migration series: %v", err)
	}
	var ddl strings.Builder
	for _, migration := range series {
		ddl.WriteString(strings.ToLower(migration.SQL))
		ddl.WriteByte('\n')
	}
	combined := ddl.String()

	declarations := map[string][]string{
		"banks":              BanksColumns,
		"bank_aliases":       BankAliasesColumns,
		"documents":          DocumentsColumns,
		"chunks":             ChunksColumns,
		"entities":           EntitiesColumns,
		"memory_units":       MemoryUnitsColumns,
		"async_operations":   AsyncOperationsColumns,
		"file_storage":       FileStorageColumns,
		"mental_models":      MentalModelsColumns,
		"directives":         DirectivesColumns,
		"knowledge_nodes":    KnowledgeNodesColumns,
		"audit_log":          AuditLogColumns,
		"llm_requests":       LLMRequestsColumns,
		"webhooks":           WebhooksColumns,
		"webhook_deliveries": WebhookDeliveriesColumns,
	}
	for table, columns := range declarations {
		for _, column := range columns {
			if !strings.Contains(combined, strings.ToLower(column)) {
				t.Errorf("%s.%s is declared in the schema inventory but no migration creates it",
					table, column)
			}
		}
	}
}

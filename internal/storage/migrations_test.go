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
		"CREATE", "ALTER", "DROP", "INSERT", "UPDATE", "DELETE", "SELECT", "SET", "GRANT", "DO", "COMMENT",
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
		"banks":            BanksColumns,
		"bank_aliases":     BankAliasesColumns,
		"documents":        DocumentsColumns,
		"chunks":           ChunksColumns,
		"entities":         EntitiesColumns,
		"memory_units":     MemoryUnitsColumns,
		"memory_links":     MemoryLinksColumns,
		"async_operations": AsyncOperationsColumns,
		"file_storage":     FileStorageColumns,
		"mental_models":    MentalModelsColumns,
		"directives":       DirectivesColumns,
		"knowledge_pages":  KnowledgePagesColumns,
		"audit_log":        AuditLogColumns,
		"llm_requests":     LLMRequestsColumns,
		"webhooks":         WebhooksColumns,
		"attachments":      AttachmentsColumns,
		"bank_stats_cache": BankStatsCacheColumns,
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

// The column names the reference does not have. They were the destination's
// own names before the schema was aligned. The series now builds the reference
// schema directly, so none of them may appear anywhere in it: a reintroduced
// name would either fail at run time or silently diverge the schema again.
//
// Whole identifiers are matched, so `id` does not match `bank_id`.
func TestRetiredColumnNamesDoNotReturn(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatalf("read the migration series: %v", err)
	}
	var ddl strings.Builder
	for _, migration := range series {
		ddl.WriteString(strings.ToLower(stripSQLComments(migration.SQL)))
		ddl.WriteByte('\n')
	}
	combined := ddl.String()

	// Each entry is a retired name plus the reference name that replaced it, so
	// a failure report says what to use instead.
	retired := map[string]string{
		"disposition_skepticism": "disposition (a jsonb object)",
		"disposition_literalism": "disposition (a jsonb object)",
		"disposition_empathy":    "disposition (a jsonb object)",
		"reflect_mission":        "mission",
		"source_fact_ids":        "source_memory_ids",
		"confidence_score":       "no column; the value is not stored",
		"access_count":           "proof_count",
		"entity_a":               "entity_id_1",
		"entity_b":               "entity_id_2",
	}
	for name, replacement := range retired {
		if containsIdentifier(combined, name) {
			t.Errorf("the retired column %q is used by a migration at or after the alignment; use %s", name, replacement)
		}
	}
}

// stripSQLComments removes line comments from DDL, so a comment that explains
// what a name used to be is not mistaken for a use of it.
func stripSQLComments(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if index := strings.Index(line, "--"); index >= 0 {
			lines[i] = line[:index]
		}
	}
	return strings.Join(lines, "\n")
}

// containsIdentifier reports whether text contains name as a whole SQL
// identifier rather than as part of a longer one, so `id` does not match
// `bank_id` and a mention inside a comment is still counted (a comment naming a
// retired column is a hazard: the next edit copies it).
func containsIdentifier(text, name string) bool {
	for index := 0; ; {
		found := strings.Index(text[index:], name)
		if found < 0 {
			return false
		}
		start := index + found
		end := start + len(name)
		if !isIdentifierByte(text, start-1) && !isIdentifierByte(text, end) {
			return true
		}
		index = start + 1
	}
}

// isIdentifierByte reports whether the byte at an offset is part of a SQL
// identifier. An out-of-range offset is not.
func isIdentifierByte(text string, offset int) bool {
	if offset < 0 || offset >= len(text) {
		return false
	}
	c := text[offset]
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// Every CREATE EXTENSION in the series must be preceded by the extension
// advisory lock in the same migration.
//
// CREATE EXTENSION IF NOT EXISTS is not concurrency-safe: two sessions both
// pass the existence check, both issue the create, and one fails on the
// catalogue's unique index on the extension name. A migration runs from every
// process that starts, so two deployments coming up together collide. Verified
// against a live server: twelve concurrent creates of one extension produced
// nine to ten duplicate-key failures without the lock and zero with it.
//
// This is a static check rather than a concurrency test because the race is
// intermittent — it does not reproduce on every run — so a test that raced
// would pass whether or not the lock was there, and only the invariant is
// dependable.
func TestExtensionCreationIsSerialized(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatalf("read the migration series: %v", err)
	}
	const lock = "pg_advisory_xact_lock"
	creations := 0
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			if !strings.Contains(strings.ToUpper(statement), "CREATE EXTENSION") {
				continue
			}
			creations++
			// The lock must appear in a statement of the same migration, taken
			// before this one. A lock taken afterwards does not help.
			locked := false
			for _, earlier := range migration.Statements() {
				if earlier == statement {
					break
				}
				if strings.Contains(earlier, lock) {
					locked = true
					break
				}
			}
			if !locked {
				t.Fatalf("migration %04d creates an extension without taking %s first: %q",
					migration.Version, lock, firstLine(statement))
			}
		}
	}
	if creations == 0 {
		t.Fatal("no extension creation found; the guard is not covering anything")
	}
}

package storage

import (
	"testing"
)

// TestExpectedSchemaReflectsTheDeclaredTableInventory ties the parsed inventory
// to the hand-declared list of owned tables.
//
// The two are written independently — one is read out of the SQL, the other is
// declared in schema.go — so a table added to the series and forgotten in the
// declaration, or one declared and never created, shows up here. The mismatch
// matters because the parsed inventory is what decides whether a target
// database is adopted, and a table missing from it would be created on top of
// an existing one.
func TestExpectedSchemaReflectsTheDeclaredTableInventory(t *testing.T) {
	expected, err := ExpectedSchemaOf()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range MemoryDomainTables {
		if !expected.HasTable(table) {
			t.Errorf("table %q is declared but the migration series does not create it", table)
		}
	}
	for _, object := range expected.Objects() {
		if object.Kind != ObjectTable {
			continue
		}
		if !OwnsTable(object.Name) {
			t.Errorf("the series creates table %q, which the storage area does not declare as its own", object.Name)
		}
	}
}

// TestExpectedSchemaReadsWrappedColumnDefinitions guards the parse against the
// shape of the emitted file: a column definition longer than the file's width
// is wrapped, and its continuation must not be read as another column.
func TestExpectedSchemaReadsWrappedColumnDefinitions(t *testing.T) {
	expected, err := ExpectedSchemaOf()
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		table  string
		column string
	}{
		{"bank_aliases", "is_primary"},
		{"bank_stats_cache", "computed_at"},
		{"unit_entities", "entity_id"},
		{"webhooks", "http_config"},
		{"banks", "disposition"},
	} {
		columns := expected.columns[testCase.table]
		if columns == nil {
			t.Fatalf("table %q was not parsed", testCase.table)
		}
		if !columns[testCase.column] {
			t.Errorf("column %q of table %q was not parsed", testCase.column, testCase.table)
		}
		// A continuation line reads as a one-word column; nothing in the
		// series declares one, so a bare "NOT" or "NULL" is the defect.
		for _, keyword := range []string{"NOT", "NULL", "VALUES"} {
			if columns[keyword] {
				t.Errorf("table %q parsed a continuation line as column %q", testCase.table, keyword)
			}
		}
	}
}

// TestExpectedSchemaColumnsMatchDeclaredColumns compares the parsed columns of
// a table against the declared list for it. The declarations are what the
// hand-written queries are built on, so a column the parse loses would be a
// column adoption would create again.
//
// mental_models is left out: its declared list is a union of the columns the
// memory area touches rather than the table's declared order, and the parse is
// not expected to reproduce that.
func TestExpectedSchemaColumnsMatchDeclaredColumns(t *testing.T) {
	expected, err := ExpectedSchemaOf()
	if err != nil {
		t.Fatal(err)
	}
	// The parsed columns in declared order, from the object inventory.
	columnsOf := func(table string) []string {
		var columns []string
		for _, object := range expected.Objects() {
			if object.Kind == ObjectColumn && object.Table == table {
				columns = append(columns, object.Name)
			}
		}
		return columns
	}
	for _, testCase := range []struct {
		table    string
		declared []string
	}{
		{"async_operations", AsyncOperationsColumns},
		{"attachments", AttachmentsColumns},
		{"audit_log", AuditLogColumns},
		{"bank_aliases", BankAliasesColumns},
		{"bank_stats_cache", BankStatsCacheColumns},
		{"banks", BanksColumns},
		{"chunks", ChunksColumns},
		{"documents", DocumentsColumns},
		{"entities", EntitiesColumns},
		{"file_storage", FileStorageColumns},
		{"knowledge_pages", KnowledgePagesColumns},
		{"memory_links", MemoryLinksColumns},
		{"memory_units", MemoryUnitsColumns},
		{"webhooks", WebhooksColumns},
	} {
		parsed := columnsOf(testCase.table)
		if !sameStringSet(parsed, testCase.declared) {
			t.Errorf("columns of %q:\n parsed   %v\n declared %v", testCase.table, parsed, testCase.declared)
		}
	}
}

// TestExpectedSchemaReadsEveryObjectClass asserts that each class the adoption
// check compares is actually populated, so a parse that silently reads nothing
// of one class cannot make adoption look strictly safer than it is.
func TestExpectedSchemaReadsEveryObjectClass(t *testing.T) {
	expected, err := ExpectedSchemaOf()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[ObjectKind]int{}
	for _, object := range expected.Objects() {
		counts[object.Kind]++
	}
	for _, kind := range []ObjectKind{ObjectTable, ObjectColumn, ObjectConstraint, ObjectIndex, ObjectFunction} {
		if counts[kind] == 0 {
			t.Errorf("the inventory holds no %s", kind)
		}
	}
	if !expected.constraints["banks\x00pk_banks"] {
		t.Error("the primary key of banks was not parsed")
	}
	if !expected.indexes["idx_bank_aliases_bank"] {
		t.Error("idx_bank_aliases_bank was not parsed")
	}
}

// TestStructuralInventoryExcludesIndexes pins the rule the adoption decision
// rests on: an index is tracked but does not decide whether a database is
// adopted, because the reference creates its vector indexes outside its
// migrations and two databases of the same recorded revision disagree about
// their names.
func TestStructuralInventoryExcludesIndexes(t *testing.T) {
	expected, err := ExpectedSchemaOf()
	if err != nil {
		t.Fatal(err)
	}
	indexes := 0
	for _, object := range expected.Objects() {
		if object.Kind == ObjectIndex {
			indexes++
		}
	}
	if indexes == 0 {
		t.Fatal("the inventory tracks no index, so the exclusion proves nothing")
	}
	if len(expected.Structural()) != len(expected.Objects())-indexes {
		t.Fatalf("structural inventory holds %d objects, want %d", len(expected.Structural()), len(expected.Objects())-indexes)
	}
	for _, object := range expected.Structural() {
		if object.Kind == ObjectIndex {
			t.Errorf("%s is an index and must not decide adoption", object)
		}
	}
	// The two vector indexes are still tracked, so their absence is reported.
	for _, name := range []string{"idx_memory_units_embedding_hnsw", "idx_mental_models_embedding_hnsw"} {
		if !expected.indexes[name] {
			t.Errorf("index %q is not tracked", name)
		}
	}
}

// TestSplitTopLevelCommas checks the division of a definition list on its
// top-level commas, which is what keeps a default holding a comma from being
// read as two definitions.
func TestSplitTopLevelCommas(t *testing.T) {
	for _, testCase := range []struct {
		input string
		want  int
	}{
		{`a integer, b text`, 2},
		{`disposition jsonb DEFAULT '{"empathy": 3, "literalism": 3}'::jsonb CONSTRAINT banks_personality_not_null NOT NULL`, 1},
		{`tags character varying[] DEFAULT ARRAY[]::character varying[], other text`, 2},
		{`a numeric(10, 2), b text`, 2},
		{`a text DEFAULT E'a, b', b text`, 2},
	} {
		if got := len(splitTopLevelCommas(testCase.input)); got != testCase.want {
			t.Errorf("splitTopLevelCommas(%q) = %d pieces, want %d", testCase.input, got, testCase.want)
		}
	}
}

// sameStringSet reports whether two string slices hold the same values,
// regardless of order. The comparison is a set rather than a sequence because
// the declared lists are hand-written reading orders, not table declarations.
func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := map[string]int{}
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

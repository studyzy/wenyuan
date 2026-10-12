package storage

import (
	"context"
	"strings"
	"testing"
)

// ts-0012: the full migration series runs before traffic and is idempotent
// across a fresh and an existing database.
func TestMigrationSeriesIsOrderedAndGapless(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(series) < 3 {
		t.Fatalf("expected at least three migrations, got %d", len(series))
	}
	for i, migration := range series {
		if migration.Version != i+1 {
			t.Fatalf("migration %d has version %d", i, migration.Version)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			t.Fatalf("migration %d has empty SQL", migration.Version)
		}
	}
}

func TestMigrationsAreIdempotentByConstruction(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	indexStatements := 0
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			upper := strings.ToUpper(statement)
			switch {
			case strings.Contains(upper, "CREATE TABLE"):
				indexStatements++
				if !strings.Contains(upper, "IF NOT EXISTS") {
					t.Fatalf("table creation must be idempotent: %s", statement)
				}
			case strings.Contains(upper, "ADD COLUMN"):
				indexStatements++
				if !strings.Contains(upper, "IF NOT EXISTS") {
					t.Fatalf("column addition must be idempotent: %s", statement)
				}
			case strings.Contains(upper, "DROP CONSTRAINT"):
				indexStatements++
				if !strings.Contains(upper, "IF EXISTS") {
					t.Fatalf("constraint drop must be idempotent: %s", statement)
				}
			}
		}
	}
	if indexStatements == 0 {
		t.Fatal("expected idempotent schema statements in the migration series")
	}
}

func TestMigrationStatementsSplitAndStripComments(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			if strings.Contains(statement, "--") {
				t.Fatalf("comments must be stripped from executed statements: %s", statement)
			}
			if strings.HasSuffix(strings.TrimSpace(statement), ";") {
				t.Fatalf("statements must not retain the trailing semicolon: %q", statement)
			}
		}
	}
}

// ts-0014 / ec-0004: the final type constraint accepts exactly the narrowed
// set. The constraint may be introduced by any migration in the series.
func TestFinalTypeConstraintIsNarrowed(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	var constraint string
	for _, migration := range series {
		if strings.Contains(migration.SQL, "memory_units_type_check") {
			constraint = migration.SQL
		}
	}
	if constraint == "" {
		t.Fatal("a migration must redefine the type constraint")
	}
	if strings.Contains(constraint, "'opinion'") {
		t.Fatalf("final constraint must not retain the wider set: %s", constraint)
	}
	for _, value := range FinalMemoryUnitTypes() {
		if !strings.Contains(constraint, "'"+string(value)+"'") {
			t.Fatalf("final constraint must accept %q", value)
		}
	}
}

func TestLatestVersionMatchesSeries(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	latest, err := LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	if latest != series[len(series)-1].Version {
		t.Fatalf("latest = %d, series end = %d", latest, series[len(series)-1].Version)
	}
}

func TestMemoryStoreMigrateIsRepeatable(t *testing.T) {
	store := NewMemoryStore("public")
	for i := 0; i < 3; i++ {
		if err := store.Migrate(context.TODO()); err != nil {
			t.Fatal(err)
		}
	}
	if !store.Migrated() {
		t.Fatal("store must report itself migrated")
	}
}

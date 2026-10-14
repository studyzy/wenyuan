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

// The series is applied once per version and the applied versions are recorded,
// so re-running it is a no-op regardless of how the statements are written. The
// PostgreSQL side of that is exercised by TestMigrateIsRepeatable, which needs a
// database; the in-process side needs nothing.
func TestMigrationVersionsAreUnique(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, migration := range series {
		if seen[migration.Version] {
			t.Fatalf("migration version %d appears twice", migration.Version)
		}
		seen[migration.Version] = true
	}
}

// A statement must be handed to the database whole, with no comment text
// interrupting it and no terminator left over.
//
// The original form of this test asserted that no statement *contains* "--",
// which held only while comments appeared between statements. Inside a
// dollar-quoted function body a "--" is content, and removing it would corrupt
// the body — so the assertion is the property that matters: a comment must not
// split a statement. A statement carrying a "--" is therefore fine when the
// comment sits inside a quoted region, and is a split when it does not.
func TestMigrationStatementsAreWholeAndUnterminated(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			if strings.HasSuffix(strings.TrimSpace(statement), ";") {
				t.Fatalf("statements must not retain the trailing semicolon: %q", statement)
			}
			if splitByComment(statement) {
				t.Fatalf("a comment split a statement: %q", firstLine(statement))
			}
		}
	}
}

// splitByComment reports whether a statement holds a comment outside every
// quoted region, which means it is a fragment rather than a statement.
func splitByComment(statement string) bool {
	var (
		delimiter string
		single    bool
	)
	for i := 0; i < len(statement); i++ {
		c := statement[i]
		if delimiter == "" && !single && c == '\'' {
			if single && i+1 < len(statement) && statement[i+1] == '\'' {
				i++
				continue
			}
			single = !single
			continue
		}
		if !single && c == '$' {
			if tag, width := scanDollarTag(statement[i:]); width > 0 {
				if delimiter == "" {
					delimiter = tag
				} else if tag == delimiter {
					delimiter = ""
				}
				i += width - 1
				continue
			}
		}
		if delimiter == "" && !single && c == '-' && i+1 < len(statement) && statement[i+1] == '-' {
			return true
		}
	}
	return false
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
		if strings.Contains(migration.SQL, "memory_units_fact_type_check") {
			constraint = migration.SQL
		}
	}
	if constraint == "" {
		t.Fatal("a migration must define the fact-type constraint")
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

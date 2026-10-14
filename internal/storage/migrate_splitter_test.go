package storage

import (
	"strings"
	"testing"
)

// The splitter is a scanner rather than a split on ";", because a semicolon is
// only a statement boundary outside every quoted region. These tests pin the
// three regions that would otherwise break it; the series-wide guard in
// migrations_test.go covers the plain-DDL case.

func splitMigrationStatements(t *testing.T, sql string) []string {
	t.Helper()
	return Migration{Version: 1, Name: "test", SQL: sql}.Statements()
}

// A dollar-quoted function body keeps its inner semicolons: the body is one
// statement, not one per statement it contains.
func TestSplitterKeepsDollarQuotedBodyWhole(t *testing.T) {
	sql := `CREATE OR REPLACE FUNCTION public.probe() RETURNS int
LANGUAGE plpgsql
AS $$
BEGIN
  PERFORM 1;
  PERFORM 2;
  RETURN 3;
END;
$$;
SELECT 1;`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements, want the function and the select: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "PERFORM 2;") {
		t.Errorf("the function body was split; first statement is %q", statements[0])
	}
	if !strings.HasPrefix(statements[0], "CREATE OR REPLACE FUNCTION") {
		t.Errorf("first statement = %q", statements[0])
	}
	if statements[1] != "SELECT 1" {
		t.Errorf("second statement = %q, want the trailing select", statements[1])
	}
}

// A named tag exists so a body may contain the bare "$$" as data; the matching
// closer is the same tag, not the first "$$" seen.
func TestSplitterRespectsNamedDollarTag(t *testing.T) {
	sql := `CREATE FUNCTION public.tagged() RETURNS text
LANGUAGE sql
AS $fn$
  SELECT '$$ not a closer; nor is this';
$fn$;
SELECT 2;`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "not a closer") {
		t.Errorf("the tagged body was cut short: %q", statements[0])
	}
}

// Several functions in one migration each stay whole and stay separate.
func TestSplitterHandlesMultipleFunctions(t *testing.T) {
	sql := `CREATE FUNCTION public.a() RETURNS int LANGUAGE sql AS $$ SELECT 1; $$;
CREATE FUNCTION public.b() RETURNS int LANGUAGE sql AS $$ SELECT 2; $$;
CREATE INDEX idx_a ON public.t (a);`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 3 {
		t.Fatalf("got %d statements, want 3: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "SELECT 1;") || !strings.Contains(statements[1], "SELECT 2;") {
		t.Errorf("function bodies were not kept whole: %#v", statements)
	}
	if !strings.HasPrefix(statements[2], "CREATE INDEX") {
		t.Errorf("third statement = %q", statements[2])
	}
}

// A semicolon inside a string literal is a character, not a boundary.
func TestSplitterIgnoresSemicolonInString(t *testing.T) {
	sql := `INSERT INTO public.t (v) VALUES ('a;b');
SELECT 1;`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "'a;b'") {
		t.Errorf("the literal was split: %q", statements[0])
	}
}

// A doubled quote is an escaped quote, so the string does not end early.
func TestSplitterHandlesEscapedQuote(t *testing.T) {
	sql := `UPDATE public.t SET v = 'it''s; fine' WHERE id = 1;
SELECT 1;`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "fine'") {
		t.Errorf("the escaped quote ended the string early: %q", statements[0])
	}
}

// A semicolon inside a line comment is not a boundary, which is the defect the
// series-wide guard was written for.
func TestSplitterIgnoresSemicolonInComment(t *testing.T) {
	sql := `CREATE TABLE public.t (
  a int -- trailing; comment
);
SELECT 1;`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.HasPrefix(statements[0], "CREATE TABLE") {
		t.Errorf("first statement = %q", statements[0])
	}
}

// A "$" that does not open a dollar quote is left alone, so a positional
// parameter or a bare dollar in SQL text does not swallow the rest of the file.
func TestSplitterLeavesNonTagDollarAlone(t *testing.T) {
	sql := `SELECT $1, '$';
SELECT 2;`
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "$1") {
		t.Errorf("first statement = %q", statements[0])
	}
}

// The scanner reports the tag and its width, and declines anything that is not
// a dollar quote.
func TestScanDollarTag(t *testing.T) {
	cases := []struct {
		in    string
		tag   string
		width int
	}{
		{"$$ body", "$$", 2},
		{"$fn$ body", "$fn$", 4},
		{"$body_1$ x", "$body_1$", 8},
		{"$ body", "", 0},
		{"$; x", "", 0},
		{"$1", "", 0},
		{"", "", 0},
		{"not a tag", "", 0},
	}
	for _, tc := range cases {
		tag, width := scanDollarTag(tc.in)
		if tag != tc.tag || width != tc.width {
			t.Errorf("scanDollarTag(%q) = (%q, %d), want (%q, %d)", tc.in, tag, width, tc.tag, tc.width)
		}
	}
}

// A migration holding only plain DDL splits exactly as the old splitter did,
// so the change cannot regress the existing series.
func TestSplitterUnchangedForPlainDDL(t *testing.T) {
	sql := "CREATE TABLE a (x int);\n\nALTER TABLE a ADD COLUMN y text;  -- note\n\nCREATE INDEX i ON a (x);"
	statements := splitMigrationStatements(t, sql)
	if len(statements) != 3 {
		t.Fatalf("got %d statements, want 3: %#v", len(statements), statements)
	}
	for _, statement := range statements {
		if strings.Contains(statement, ";") {
			t.Errorf("a statement kept its terminator: %q", statement)
		}
	}
}

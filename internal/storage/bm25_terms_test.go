package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestTokenizeQuery(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"Where does Alice live?", []string{"where", "does", "alice", "live"}},
		{"  MULTIPLE   spaces ", []string{"multiple", "spaces"}},
		{"punctuation-only!!!", []string{"punctuation", "only"}},
		{"!!!", nil},
		{"文渊 memory_1", []string{"文渊", "memory_1"}},
	}
	for _, test := range tests {
		got := tokenizeQuery(test.query)
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Fatalf("tokenizeQuery(%q) = %v, want %v", test.query, got, test.want)
		}
	}
}

// A broad query keeps fewer terms than it contains, and the terms kept are the
// most selective (lowest document frequency) ones.
func TestSelectSelectiveTermsPrefersRareTerms(t *testing.T) {
	tokens := []string{"the", "and", "common", "topic", "rare1", "rare2", "vend", "nil"}
	stats := []bm25TermStat{
		{df: 9000, hasLexeme: true}, // the  — very common
		{df: 8000, hasLexeme: true}, // and
		{df: 700, hasLexeme: true},  // common
		{df: 500, hasLexeme: true},  // topic
		{df: 1, hasLexeme: true},    // rare1
		{df: 2, hasLexeme: true},    // rare2
		{df: 0, hasLexeme: true},    // vend
		{df: 0, hasLexeme: false},   // nil — a stopword, dropped
	}
	got := selectSelectiveTerms(tokens, stats, 3)
	if len(got) != 3 {
		t.Fatalf("kept %d terms, want 3: %v", len(got), got)
	}
	// The three lowest-df, non-stopword tokens, in their original order.
	if strings.Join(got, ",") != "rare1,rare2,vend" {
		t.Fatalf("kept %v, want the three most selective terms", got)
	}
}

// A query already within the cap is returned unchanged.
func TestSelectSelectiveTermsWithinCap(t *testing.T) {
	tokens := []string{"a", "b", "c"}
	got := selectSelectiveTerms(tokens, nil, 16)
	if len(got) != 3 {
		t.Fatalf("kept %d terms, want all 3", len(got))
	}
}

// Unusable statistics fall back to a positional cap rather than blocking recall.
func TestSelectSelectiveTermsFallsBack(t *testing.T) {
	tokens := []string{"a", "b", "c", "d"}
	if got := selectSelectiveTerms(tokens, []bm25TermStat{{df: 1, hasLexeme: true}}, 2); strings.Join(got, ",") != "a,b" {
		t.Fatalf("mismatched stats kept %v, want the first 2", got)
	}
	allStop := []bm25TermStat{{hasLexeme: false}, {hasLexeme: false}, {hasLexeme: false}}
	if got := selectSelectiveTerms([]string{"a", "b", "c"}, allStop, 2); strings.Join(got, ",") != "a,b" {
		t.Fatalf("all-stopword stats kept %v, want the first 2", got)
	}
}

// The BM25 statement runs with a server-side statement timeout.
func TestKeywordStatementSetsStatementTimeout(t *testing.T) {
	stub := &recordingDB{}
	store := NewPostgresStore(stub, "public")
	// The stub database does not answer a SELECT, and the assertion below is
	// about the statement the helper issued rather than the rows, so the error
	// is deliberately discarded.
	_, _ = store.queryWithStatementTimeout(context.Background(), "SELECT 1", nil, bm25StatementTimeout)
	var sawTimeout bool
	for _, exec := range stub.tx.execs {
		if strings.Contains(exec, "statement_timeout") && strings.Contains(exec, bm25StatementTimeout) {
			sawTimeout = true
		}
	}
	if !sawTimeout {
		t.Fatalf("no statement_timeout was set; execs = %v", stub.tx.execs)
	}
}

// A query's terms are OR-joined so any term can match a memory, rather than
// requiring every term to appear — the reference's native BM25 arm does the
// same, and an AND form loses a fact the caller plainly meant to find.
func TestBoundedBM25TextOrJoinsShortQuery(t *testing.T) {
	store := NewPostgresStore(&recordingDB{}, "public")
	query := "Where does Alice live?"
	want := "where | does | alice | live"
	if got := store.boundedBM25Text(context.Background(), query); got != want {
		t.Fatalf("short query = %q, want %q", got, want)
	}
}

// boundedBM25Text bounds a broad query to fewer terms than it contains.
func TestBoundedBM25TextBoundsBroadQuery(t *testing.T) {
	store := NewPostgresStore(&recordingDB{}, "public")
	words := make([]string, 0, 30)
	for range 30 {
		words = append(words, "word")
	}
	text := strings.Join(words, " ")
	got := store.boundedBM25Text(context.Background(), text)
	if got == text {
		t.Fatal("a broad query was passed through unbounded")
	}
	terms := 0
	for _, tok := range tokenizeQuery(got) {
		if tok != "or" {
			terms++
		}
	}
	if terms > DefaultBM25MaxQueryTerms {
		t.Fatalf("broad query kept %d terms, want at most %d", terms, DefaultBM25MaxQueryTerms)
	}
}

var errStubRows = errors.New("stub returns no rows")

// recordingDB is a minimal DB double that records the statements it is asked to
// run. Its rows always fail, so callers exercise only the statements, not the
// scanning.
type recordingDB struct {
	tx recordingTx
}

func (d *recordingDB) Ping(context.Context) error { return nil }
func (d *recordingDB) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, errStubRows
}
func (d *recordingDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubRows
}
func (d *recordingDB) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (d *recordingDB) BeginTx(context.Context, *sql.TxOptions) (Tx, error)      { return &d.tx, nil }
func (d *recordingDB) Close() error                                             { return nil }

type recordingTx struct {
	execs   []string
	queries []string
}

func (t *recordingTx) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	t.execs = append(t.execs, query)
	return nil, nil
}
func (t *recordingTx) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	t.queries = append(t.queries, query)
	return nil, errStubRows
}
func (t *recordingTx) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (t *recordingTx) Commit() error                                            { return nil }
func (t *recordingTx) Rollback() error                                          { return nil }

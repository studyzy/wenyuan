package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// The keyword arm renders the pg_search form when the extension is configured
// as such, and the native tsvector form otherwise. This is a SQL-level test:
// no ParadeDB is needed to see which statement the arm would send.
func TestBM25ArmQuerySelectsTextSearchBackend(t *testing.T) {
	q := RecallQuery{BankID: "bank", Types: []string{string(TypeWorld)}}

	native := &PostgresStore{defaultSchema: "public", textSearchExtension: TextSearchNative, tuning: DefaultTuning()}
	nativeSQL, nativeArgs := native.bm25ArmQuery(q, "alice | berlin", 20, "public")
	for _, want := range []string{
		"ts_rank_cd(search_vector, to_tsquery('english', $3)) AS score",
		"search_vector @@ to_tsquery('english', $3)",
		"ORDER BY score DESC, ctid ASC LIMIT $4",
	} {
		if !strings.Contains(nativeSQL, want) {
			t.Fatalf("native arm SQL missing %q:\n%s", want, nativeSQL)
		}
	}
	for _, unwanted := range []string{"@@@", "paradedb", "pdb."} {
		if strings.Contains(nativeSQL, unwanted) {
			t.Fatalf("native arm SQL carries %q:\n%s", unwanted, nativeSQL)
		}
	}
	if len(nativeArgs) != 4 || nativeArgs[2] != "alice | berlin" {
		t.Fatalf("native arm args = %v", nativeArgs)
	}

	pg := &PostgresStore{
		defaultSchema: "public", textSearchExtension: TextSearchPGSearch,
		pgFunctionSchema: "paradedb", pgTokenizer: "jieba", tuning: DefaultTuning(),
	}
	pgSQL, pgArgs := pg.bm25ArmQuery(q, "alice berlin", 20, "public")
	for _, want := range []string{
		"paradedb.score(id) AS score",
		"id @@@ paradedb.boolean(should =>",
		"unnest($3::text::pdb.jieba::text[])",
		"paradedb.term(f, t)",
		"LIMIT 16", // the resolved BM25MaxQueryTerms caps the tokenized terms
		"ORDER BY score DESC, ctid ASC LIMIT $4",
	} {
		if !strings.Contains(pgSQL, want) {
			t.Fatalf("pg_search arm SQL missing %q:\n%s", want, pgSQL)
		}
	}
	for _, unwanted := range []string{"ts_rank_cd", "to_tsquery", "search_vector"} {
		if strings.Contains(pgSQL, unwanted) {
			t.Fatalf("pg_search arm SQL still carries %q:\n%s", unwanted, pgSQL)
		}
	}
	// The same positional contract, so the same argument list.
	if len(pgArgs) != 4 || pgArgs[2] != "alice berlin" {
		t.Fatalf("pg_search arm args = %v", pgArgs)
	}

	// A configured function schema other than the ParadeDB default is used
	// verbatim.
	other := &PostgresStore{
		defaultSchema: "public", textSearchExtension: TextSearchPGSearch,
		pgFunctionSchema: "pgsearch", tuning: DefaultTuning(),
	}
	otherSQL, _ := other.bm25ArmQuery(q, "alice", 20, "public")
	if !strings.Contains(otherSQL, "pgsearch.score(id)") || !strings.Contains(otherSQL, "pgsearch.boolean(should =>") {
		t.Fatalf("custom function schema ignored:\n%s", otherSQL)
	}
	// With no tokenizer the raw match form is used, which keeps a CJK run and
	// an ngram query intact.
	if !strings.Contains(otherSQL, "ARRAY[pgsearch.match('text', $3), pgsearch.match('text_signals', $3)]") {
		t.Fatalf("tokenizer-less pg_search arm SQL:\n%s", otherSQL)
	}
}

// pgSearchShould falls back to the raw match form for ngram tokenizers, whose
// overlapping grams would be pruned to the query's first characters.
func TestPgSearchShouldNgramKeepsMatchForm(t *testing.T) {
	fields := []string{"text"}
	for _, tokenizer := range []string{"", "ngram(2,3)", "edge_ngram(1,2)"} {
		got := pgSearchShould("paradedb", fields, "$3", tokenizer, 16)
		want := "ARRAY[paradedb.match('text', $3)]"
		if got != want {
			t.Fatalf("tokenizer %q rendered %q, want %q", tokenizer, got, want)
		}
	}
}

// The arm binds the raw query text for pg_search and the term-joined tsquery
// for native; the two cannot drift because the branch lives in one place.
func TestKeywordArmBindsBackendSpecificQueryText(t *testing.T) {
	ctx := context.Background()
	query := RecallQuery{BankID: "bank"}

	pgDB := &armDB{}
	pg := NewPostgresStore(pgDB, "public")
	pg.textSearchExtension = TextSearchPGSearch
	pg.pgFunctionSchema = "paradedb"
	pg.tuning = DefaultTuning()
	if _, err := pg.keywordArm(ctx, query, "alice berlin"); !errors.Is(err, errStubRows) {
		t.Fatalf("keywordArm error = %v, want the stub's", err)
	}
	if pgDB.tx.args[2] != "alice berlin" {
		t.Fatalf("pg_search arm bound %v, want the raw query text", pgDB.tx.args[2])
	}
	if !strings.Contains(pgDB.tx.query, "@@@ paradedb.boolean(should =>") {
		t.Fatalf("pg_search arm query:\n%s", pgDB.tx.query)
	}

	nativeDB := &armDB{}
	native := NewPostgresStore(nativeDB, "public")
	native.tuning = DefaultTuning()
	if _, err := native.keywordArm(ctx, query, "alice berlin"); !errors.Is(err, errStubRows) {
		t.Fatalf("keywordArm error = %v, want the stub's", err)
	}
	if nativeDB.tx.args[2] != "alice | berlin" {
		t.Fatalf("native arm bound %v, want the disjunctive tsquery", nativeDB.tx.args[2])
	}
	if !strings.Contains(nativeDB.tx.query, "to_tsquery('english', $3)") {
		t.Fatalf("native arm query:\n%s", nativeDB.tx.query)
	}
}

// armDB is a DB double whose transaction records the one statement the BM25 arm
// runs, arguments included, so a test can see what the arm bound.
type armDB struct{ tx armTx }

func (d *armDB) Ping(context.Context) error { return nil }
func (d *armDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errStubRows
}
func (d *armDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubRows
}
func (d *armDB) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (d *armDB) BeginTx(context.Context, *sql.TxOptions) (Tx, error)      { return &d.tx, nil }
func (d *armDB) Close() error                                             { return nil }

type armTx struct {
	query string
	args  []any
}

func (t *armTx) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	// The arm's transaction first sets a local statement timeout; that
	// statement succeeds so the query itself is reached.
	return nil, nil
}
func (t *armTx) QueryContext(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	t.query, t.args = query, args
	return nil, errStubRows
}
func (t *armTx) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (t *armTx) Commit() error                                            { return nil }
func (t *armTx) Rollback() error                                          { return nil }

func TestResolveTextSearchBackend(t *testing.T) {
	cases := []struct {
		name    string
		cfg     BackendConfig
		want    textSearchBackend
		wantErr bool
	}{
		{name: "unset is native", cfg: BackendConfig{}, want: textSearchBackend{
			extension: TextSearchNative, functionSchema: defaultPGFunctionSchema, nativeLanguage: "english"}},
		{name: "whitespace and case fold", cfg: BackendConfig{TextSearchExtension: "  PG_SEARCH ", PGTokenizer: " JIEBA "},
			want: textSearchBackend{extension: TextSearchPGSearch, functionSchema: defaultPGFunctionSchema, tokenizer: "jieba", nativeLanguage: "english"}},
		{name: "custom schema", cfg: BackendConfig{TextSearchExtension: TextSearchPGSearch, PGFunctionSchema: "PgSearch"},
			want: textSearchBackend{extension: TextSearchPGSearch, functionSchema: "pgsearch", nativeLanguage: "english"}},
		{name: "lindera alias", cfg: BackendConfig{PGTokenizer: "chinese_lindera"},
			want: textSearchBackend{extension: TextSearchNative, functionSchema: defaultPGFunctionSchema, tokenizer: "lindera(chinese)", nativeLanguage: "english"}},
		{name: "ngram canonicalized", cfg: BackendConfig{PGTokenizer: "ngram(3, 5)"},
			want: textSearchBackend{extension: TextSearchNative, functionSchema: defaultPGFunctionSchema, tokenizer: "ngram(3,5)", nativeLanguage: "english"}},
		{name: "configured dictionary", cfg: BackendConfig{TextSearchNativeLanguage: "simple"},
			want: textSearchBackend{extension: TextSearchNative, functionSchema: defaultPGFunctionSchema, nativeLanguage: "simple"}},
		{name: "dictionary case folds", cfg: BackendConfig{TextSearchNativeLanguage: "  ZHParser "},
			want: textSearchBackend{extension: TextSearchNative, functionSchema: defaultPGFunctionSchema, nativeLanguage: "zhparser"}},
		{name: "invalid schema", cfg: BackendConfig{PGFunctionSchema: "paradedb; DROP TABLE x"}, wantErr: true},
		{name: "invalid tokenizer", cfg: BackendConfig{PGTokenizer: "bogus"}, wantErr: true},
		{name: "ngram min above max", cfg: BackendConfig{PGTokenizer: "ngram(5,3)"}, wantErr: true},
		{name: "ngram zero min", cfg: BackendConfig{PGTokenizer: "ngram(0,3)"}, wantErr: true},
		{name: "invalid dictionary", cfg: BackendConfig{TextSearchNativeLanguage: "english'; DROP TABLE x; --"}, wantErr: true},
		{name: "dictionary starting with a digit", cfg: BackendConfig{TextSearchNativeLanguage: "3english"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTextSearchBackend(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("resolved %+v, want %+v", got, tc.want)
			}
		})
	}
}

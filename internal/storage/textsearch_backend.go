package storage

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The text-search backends the serve path knows about. The reference switches
// its whole lexical implementation on HINDSIGHT_API_TEXT_SEARCH_EXTENSION; this
// build implements the native tsvector backend and ParadeDB's pg_search, which
// is the backend an operator reaches for when the corpus is not English.
const (
	// TextSearchNative is tsvector with a per-language dictionary — the
	// reference's default and this build's.
	TextSearchNative = "native"
	// TextSearchPGSearch is ParadeDB's pg_search (BM25 over its own index).
	TextSearchPGSearch = "pg_search"
)

// defaultPGFunctionSchema is where ParadeDB installs its SQL functions, the
// reference's DEFAULT_TEXT_SEARCH_EXTENSION_PG_SEARCH_FUNCTION_SCHEMA.
const defaultPGFunctionSchema = "paradedb"

// textSearchBackend is the resolved lexical backend: which arm to build, and
// the pg_search settings its SQL embeds.
type textSearchBackend struct {
	extension      string
	functionSchema string
	tokenizer      string
}

// resolveTextSearchBackend reads the configured text-search backend.
//
// Both pg_search settings are embedded verbatim in the arm's SQL — there is no
// bind parameter for a function schema or a tokenizer — so a value the
// reference would reject (`config.py` validates the schema as a PostgreSQL
// identifier and the tokenizer against a whitelist) is an error here too rather
// than an injection. Validation runs whether or not pg_search is selected, so a
// deployment that later switches the extension does not discover a malformed
// setting by failing to start for a different reason.
func resolveTextSearchBackend(cfg BackendConfig) (textSearchBackend, error) {
	extension := strings.ToLower(strings.TrimSpace(cfg.TextSearchExtension))
	if extension == "" {
		extension = TextSearchNative
	}
	schema := strings.ToLower(strings.TrimSpace(cfg.PGFunctionSchema))
	if schema == "" {
		schema = defaultPGFunctionSchema
	} else if !pgIdentifierPattern.MatchString(schema) {
		return textSearchBackend{}, fmt.Errorf(
			"invalid pg_search function schema %q: must be a PostgreSQL identifier", cfg.PGFunctionSchema)
	}
	tokenizer, err := normalizePGSearchTokenizer(cfg.PGTokenizer)
	if err != nil {
		return textSearchBackend{}, err
	}
	return textSearchBackend{extension: extension, functionSchema: schema, tokenizer: tokenizer}, nil
}

// pgIdentifierPattern is the identifier shape the reference accepts for the
// pg_search function schema: letters, digits and underscores, not starting with
// a digit.
var pgIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// pgSearchTokenizers are ParadeDB's simple tokenizers, spelled exactly as the
// reference's whitelist spells them.
var pgSearchTokenizers = map[string]bool{
	"unicode_words":      true,
	"simple":             true,
	"whitespace":         true,
	"literal":            true,
	"literal_normalized": true,
	"chinese_compatible": true,
	"icu":                true,
	"jieba":              true,
	"source_code":        true,
}

// pgSearchTokenizerAliases spell the lindera tokenizers the way the reference
// lets them be abbreviated.
var pgSearchTokenizerAliases = map[string]string{
	"chinese_lindera":  "lindera(chinese)",
	"japanese_lindera": "lindera(japanese)",
	"korean_lindera":   "lindera(korean)",
	"lindera_chinese":  "lindera(chinese)",
	"lindera_japanese": "lindera(japanese)",
	"lindera_korean":   "lindera(korean)",
}

var (
	pgLinderaPattern = regexp.MustCompile(`^lindera\((chinese|japanese|korean)\)$`)
	pgNgramPattern   = regexp.MustCompile(`^(ngram|edge_ngram)\((\d{1,3}),\s*(\d{1,3})\)$`)
)

// normalizePGSearchTokenizer validates and normalizes a ParadeDB tokenizer
// setting, mirroring the reference's normalize_pg_search_tokenizer. The result
// is safe to place after `pdb.` in a SQL expression; an unset value is the
// empty string, which selects the plain match form.
func normalizePGSearchTokenizer(value string) (string, error) {
	tokenizer := strings.ToLower(strings.TrimSpace(value))
	if tokenizer == "" {
		return "", nil
	}
	if alias, ok := pgSearchTokenizerAliases[tokenizer]; ok {
		return alias, nil
	}
	if pgSearchTokenizers[tokenizer] {
		return tokenizer, nil
	}
	if pgLinderaPattern.MatchString(tokenizer) {
		return tokenizer, nil
	}
	if m := pgNgramPattern.FindStringSubmatch(tokenizer); m != nil {
		// Only the digits and the kind survive the round trip, so the rendered
		// setting is the canonical `kind(min,max)` the reference stores.
		min, _ := strconv.Atoi(m[2])
		max, _ := strconv.Atoi(m[3])
		if min <= 0 || min > max {
			return "", fmt.Errorf(
				"invalid pg_search tokenizer %q: ngram and edge_ngram require positive min/max gram sizes with min <= max", value)
		}
		return fmt.Sprintf("%s(%d,%d)", m[1], min, max), nil
	}
	return "", fmt.Errorf(
		"invalid pg_search tokenizer %q: supported values are unicode_words, simple, whitespace, literal, "+
			"literal_normalized, chinese_compatible, icu, jieba, source_code, chinese_lindera, japanese_lindera, "+
			"korean_lindera, lindera(chinese|japanese|korean), ngram(min,max), edge_ngram(min,max)", value)
}

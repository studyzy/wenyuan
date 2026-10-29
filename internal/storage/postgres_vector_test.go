//go:build pgx

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"
)

// postgresVectorStore opens a real PostgreSQL backend in its own schema. The
// test is skipped when no database URL is configured, so the default gate stays
// offline; it must be RUN explicitly rather than assumed.
func postgresVectorStore(t *testing.T) Store {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL vector test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("vector_e2e_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema, RunMigrations: true, BankStatsCacheTTL: BankStatsCacheTTL})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		// Drop the schema, not just the connection. Each test migrates its own
		// uniquely named schema, and leaving it behind accumulates: the
		// cross-schema discovery routines walk pg_class, so a database that
		// keeps every retired test schema eventually runs out of lock table
		// when a routine opens a cursor per schema.
		if pg, ok := store.(*PostgresStore); ok {
			_, _ = pg.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
		}
		if closer, ok := store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	if _, err := store.CreateBank(ctx, Bank{BankID: "vec-bank", Name: "vec-bank"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	return store
}

// unitVector builds a full-width vector that is dense only at one position, so
// two vectors sharing a position are cosine-similar and two that do not are
// orthogonal. That makes the expected ranking unambiguous without needing a
// real embedding provider.
func unitVector(position int) []float32 {
	vec := make([]float32, EmbeddingDimensions)
	vec[position] = 1
	return vec
}

// test-vector-similarity-ranks-by-distance: recall with an embedded query
// returns the stored vector that is actually closest, not merely the one whose
// text contains the query words.
func TestVectorSimilarityRanksByDistance(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	// Three units whose text has nothing in common with the query words, so a
	// lexical arm would return nothing and only real vector ranking can order
	// them.
	units := []struct {
		id       string
		text     string
		position int
	}{
		{"unit-near", "alpha bravo", 7},
		{"unit-mid", "charlie delta", 8},
		{"unit-far", "echo foxtrot", 9},
	}
	for _, u := range units {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: pgUUID(u.id), BankID: "vec-bank", Text: u.text,
			FactType: TypeWorld, Embedding: unitVector(u.position),
		}); err != nil {
			t.Fatalf("upsert %s: %v", u.id, err)
		}
	}

	// A query vector closest to position 9, then 8, then 7. The three
	// similarities it produces sit above the default semantic floor (0.3), so
	// distance is the only thing that can order or drop them.
	query := make([]float32, EmbeddingDimensions)
	query[9] = 0.6
	query[8] = 0.5
	query[7] = 0.4

	result, err := store.Recall(ctx, RecallQuery{
		BankID:         "vec-bank",
		Query:          "nothing in common with any stored text",
		QueryEmbedding: query,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Matches) != 3 {
		t.Fatalf("recall returned %d matches, want 3 (ids %v)", len(result.Matches), idsOf(result.Matches))
	}
	if result.Matches[0].ID != pgUUID("unit-far") {
		t.Fatalf("closest match = %s, want unit-far (ordered %v)",
			result.Matches[0].ID, idsOf(result.Matches))
	}
	if result.Matches[1].ID != pgUUID("unit-mid") {
		t.Fatalf("second match = %s, want unit-mid", result.Matches[1].ID)
	}
	if result.Matches[2].ID != pgUUID("unit-near") {
		t.Fatalf("third match = %s, want unit-near", result.Matches[2].ID)
	}

	// The scores must be a real similarity, distinct per row and descending.
	if result.Matches[0].Scores[ArmSemantic] <= result.Matches[1].Scores[ArmSemantic] {
		t.Fatalf("similarity scores are not descending: %v", result.Matches[0].Scores)
	}
	// A cosine similarity always sits in [0,1].
	for _, match := range result.Matches {
		score := match.Scores[ArmSemantic]
		if score < 0 || score > 1 {
			t.Fatalf("similarity %v out of range for %s", score, match.ID)
		}
	}
}

// test-vector-recall-skips-unembedded-rows: a row with no embedding carries no
// semantic evidence, so it must not be scored as a zero-similarity match.
func TestVectorRecallSkipsUnembeddedRows(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("embedded"), BankID: "vec-bank", Text: "has a vector",
		FactType: TypeWorld, Embedding: unitVector(3),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("unembedded"), BankID: "vec-bank", Text: "has no vector",
		FactType: TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := store.Recall(ctx, RecallQuery{
		BankID:         "vec-bank",
		Query:          "anything",
		QueryEmbedding: unitVector(3),
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].ID != pgUUID("embedded") {
		t.Fatalf("only the embedded row may match, got %v", idsOf(result.Matches))
	}
}

// test-embedding-round-trips: a stored vector reads back with the same values,
// which is what makes ranking stable across a write and a read.
func TestEmbeddingRoundTrips(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	original := make([]float32, EmbeddingDimensions)
	for i := range original {
		original[i] = float32(i%7) / 7
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("round-trip"), BankID: "vec-bank", Text: "round trip",
		FactType: TypeWorld, Embedding: original,
	}); err != nil {
		t.Fatal(err)
	}

	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatal("expected a postgres store")
	}
	var raw string
	query := `SELECT COALESCE(embedding::text, '') FROM ` + quoteIdent(pg.defaultSchema) + `.memory_units WHERE id = $1`
	if err := pg.db.QueryRowContext(ctx, query, pgUUID("round-trip")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEmbedding(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded width = %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if diff := decoded[i] - original[i]; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("component %d = %v, want %v", i, decoded[i], original[i])
		}
	}
}

// test-wrong-width-is-rejected: a vector of the wrong width is refused rather
// than silently stored, because it would rank against every row incorrectly.
func TestWrongWidthEmbeddingIsRejected(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	_, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: "wrong-width", BankID: "vec-bank", Text: "too short",
		FactType: TypeWorld, Embedding: []float32{1, 2, 3},
	})
	if err == nil {
		t.Fatal("a wrong-width embedding must be rejected")
	}
}

// test-cosine-similarity-bounds: the distance-to-similarity conversion clamps,
// so a distance outside the theoretical range cannot produce a rank above one.
func TestCosineSimilarityBounds(t *testing.T) {
	cases := []struct{ distance, want float64 }{
		{0, 1},
		{1, 0},
		{2, 0},
		{3, 0},
		{-1, 1},
		{0.25, 0.75},
	}
	for _, tc := range cases {
		if got := CosineSimilarity(tc.distance); got != tc.want {
			t.Fatalf("cosine similarity of distance %v = %v, want %v", tc.distance, got, tc.want)
		}
	}
}

// idsOf renders the match identifiers for a failure message.
func idsOf(matches []RecallMatch) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.ID)
	}
	return out
}

// pgUUID derives a deterministic uuid from a label so a test fixture can name
// a row readably. The reference declares these identities as uuid, so a
// literal like "fact-1" cannot be stored; the label is hashed into the
// canonical layout with the version and variant bits set.
func pgUUID(label string) string {
	sum := sha256.Sum256([]byte(label))
	buf := make([]byte, 16)
	copy(buf, sum[:16])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

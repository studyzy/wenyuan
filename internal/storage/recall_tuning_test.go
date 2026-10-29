package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// tuneStore installs a tuning derived from the reference defaults with the
// supplied customisation applied. It is the seam every test here uses to make
// one switch observable without disturbing the others.
func tuneStore(t *testing.T, s *MemoryStore, apply func(*Tuning)) {
	t.Helper()
	tune := DefaultTuning()
	apply(&tune)
	s.SetTuning(tune)
}

// A query over the token cap is truncated to the cap, and the truncation is a
// prefix of the original so a caller cannot receive text it did not send.
func TestTruncateQueryToTokenLimit(t *testing.T) {
	prefix := "alpha bravo"
	cap := CountTokens(prefix)
	tests := []struct {
		name      string
		query     string
		maxTokens int
		want      string
	}{
		{"a disabled cap leaves the query alone", "alpha bravo charlie delta", 0, "alpha bravo charlie delta"},
		{"a query no longer than the cap in characters is untouched", "hello world", 500, "hello world"},
		{"a query within the token cap is untouched", "alpha bravo", 3, "alpha bravo"},
		{"an over-cap query is cut to the cap", "alpha bravo charlie delta", cap, prefix},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := TruncateQueryToTokens(test.query, test.maxTokens)
			if got != test.want {
				t.Fatalf("TruncateQueryToTokens(%q, %d) = %q, want %q", test.query, test.maxTokens, got, test.want)
			}
			if test.maxTokens > 0 && CountTokens(got) > test.maxTokens {
				t.Fatalf("%q costs %d tokens, over the cap %d", got, CountTokens(got), test.maxTokens)
			}
		})
	}
}

// The cap is applied inside Recall, so a query longer than the cap cannot reach
// the arms with the terms beyond the cap: a memory only the dropped tail names
// is not returned.
func TestRecallTruncatesOverLongQueryAtIngress(t *testing.T) {
	s := NewMemoryStore("public")
	prefix := strings.Repeat("x ", 30)
	// The cap is far below the query's length, so the trailing word is what the
	// truncation drops.
	tuneStore(t, s, func(tune *Tuning) { tune.MaxQueryTokens = 5 })
	seedBank(t, s, "b1",
		RecallMatch{ID: "tail", Text: "zebra", Type: "world"},
		RecallMatch{ID: "prefix", Text: "x", Type: "world"},
	)
	query := prefix + "zebra"

	// The temporal arm scores every memory by recency regardless of the query's
	// words, so it is disabled here to keep the assertion about the query the
	// arms actually see.
	capped, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: query, DisabledArms: []string{ArmTemporal}})
	if err != nil {
		t.Fatal(err)
	}
	if containsID(recallIDs(capped), "tail") {
		t.Fatalf("a term past the query cap reached the arms: %v", recallIDs(capped))
	}

	// With the cap off the same query reaches the dropped term, proving the
	// truncation is what removed it rather than the arm's own matching.
	tuneStore(t, s, func(tune *Tuning) { tune.MaxQueryTokens = 0 })
	unbounded, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: query, DisabledArms: []string{ArmTemporal}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(recallIDs(unbounded), "tail") {
		t.Fatalf("the uncapped query missed the tail memory: %v", recallIDs(unbounded))
	}
}

// More than the configured number of recalls in flight wait for a permit
// rather than all running at once.
func TestRecallWaitsForAConcurrencyPermit(t *testing.T) {
	s := NewMemoryStore("public")
	tuneStore(t, s, func(tune *Tuning) { tune.MaxConcurrent = 1 })
	seedBank(t, s, "b1", RecallMatch{ID: "a", Text: "alpha", Type: "world"})

	// Occupy the single permit the way a running recall does.
	s.recallGate <- struct{}{}
	done := make(chan error, 1)
	go func() {
		_, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a recall ran without a free permit (err = %v)", err)
	case <-time.After(50 * time.Millisecond):
	}
	<-s.recallGate
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a recall blocked after the permit was released")
	}
}

// A recall whose context ends while it waits for a permit reports the context's
// error rather than running without one.
func TestRecallPermitRespectsContextCancellation(t *testing.T) {
	s := NewMemoryStore("public")
	tuneStore(t, s, func(tune *Tuning) { tune.MaxConcurrent = 1 })
	seedBank(t, s, "b1", RecallMatch{ID: "a", Text: "alpha", Type: "world"})
	s.recallGate <- struct{}{}
	defer func() { <-s.recallGate }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Recall(ctx, RecallQuery{BankID: "b1", Query: "alpha"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The fact token budget bounds the returned matches, keeping the best-ranked
// ones that fit; a zero budget is unbounded.
func TestRecallAppliesFactTokenBudget(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
	)
	query := RecallQuery{BankID: "b1", Query: "alpha"}

	all, err := s.Recall(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Matches) != 2 {
		t.Fatalf("unbounded recall returned %d matches, want 2", len(all.Matches))
	}

	// The budget is exactly one match's cost, so it fits one and not two.
	query.MaxTokens = CountTokens("alpha")
	capped, err := s.Recall(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Matches) != 1 {
		t.Fatalf("budgeted recall returned %d matches, want 1: %v", len(capped.Matches), recallIDs(capped))
	}
}

// The chunk token budget bounds the chunk side channel: the last chunk that
// fits is clipped and marked truncated, and a zero budget is unbounded.
func TestRecallAppliesChunkTokenBudget(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "a", Text: "alpha", Type: "world", ChunkID: "c1"})
	s.chunks = append(s.chunks, Chunk{ChunkID: "c1", BankID: "b1", ChunkText: "one two three four five six seven eight nine ten"})

	query := RecallQuery{BankID: "b1", Query: "alpha", IncludeChunks: true}
	unbounded, err := s.Recall(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(unbounded.ChunkEntries) != 1 || unbounded.ChunkEntries[0].Truncated {
		t.Fatalf("unbounded chunk channel = %+v, want one untruncated chunk", unbounded.ChunkEntries)
	}

	query.MaxChunkTokens = 3
	capped, err := s.Recall(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.ChunkEntries) != 1 {
		t.Fatalf("budgeted chunk channel dropped the only chunk: %+v", capped.ChunkEntries)
	}
	got := capped.ChunkEntries[0]
	if !got.Truncated {
		t.Fatalf("a clipped chunk was not marked truncated: %+v", got)
	}
	if CountTokens(got.Text) > 3 {
		t.Fatalf("clipped chunk costs %d tokens, over the budget 3", CountTokens(got.Text))
	}
	if got.Text == unbounded.ChunkEntries[0].Text {
		t.Fatal("the chunk budget did not clip the chunk text")
	}
}

// countingDB is a database double that counts the read statements issued
// against it, so a test can tell whether a branch read the corpus statistics.
type countingDB struct {
	recordingDB
	reads int
}

func (d *countingDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	d.reads++
	return nil, errStubRows
}

// The BM25 arm selects terms by document frequency by default, and a deployment
// that turns that off takes the positional first-N cap instead — no statistics
// read at all.
func TestBoundedBM25TextSelectiveTermsSwitch(t *testing.T) {
	words := make([]string, 0, 30)
	for range 30 {
		words = append(words, "word")
	}
	text := strings.Join(words, " ")

	selective := NewPostgresStore(&countingDB{}, "public")
	selective.boundedBM25Text(context.Background(), text)
	if selective.reader().(*countingDB).reads == 0 {
		t.Fatal("selective term selection did not read the corpus statistics")
	}

	positional := NewPostgresStore(&countingDB{}, "public")
	tune := DefaultTuning()
	tune.BM25SelectiveTerms = false
	positional.SetTuning(tune)
	got := positional.boundedBM25Text(context.Background(), text)
	if positional.reader().(*countingDB).reads != 0 {
		t.Fatal("the positional cap still read the corpus statistics")
	}
	want := strings.Join(words[:DefaultBM25MaxQueryTerms], " | ")
	if got != want {
		t.Fatalf("positional cap = %q, want the first %d terms %q", got, DefaultBM25MaxQueryTerms, want)
	}
}

// The reranker candidate cap resolves per budget level: a level's override wins
// when it is set (> 0), otherwise the flat cap answers, and an unrecognised
// level falls back to the middle one — the reference's resolution
// (engine/memory_engine.py:1715-1730). An unset cap stays unset.
func TestRerankerMaxCandidatesFor(t *testing.T) {
	tuning := Tuning{RerankerMaxCandidates: 300, RerankerMaxCandidatesLow: 100}
	tests := []struct {
		budget string
		want   int
	}{
		{BudgetLow, 100},
		{BudgetMid, 300},
		{BudgetHigh, 300},
		{"", 300},
		{"sometimes", 300},
	}
	for _, test := range tests {
		if got := tuning.RerankerMaxCandidatesFor(test.budget); got != test.want {
			t.Fatalf("budget %q cap = %d, want %d", test.budget, got, test.want)
		}
	}
	if got := (Tuning{}).RerankerMaxCandidatesFor(BudgetLow); got != 0 {
		t.Fatalf("unset tuning cap = %d, want 0", got)
	}
}

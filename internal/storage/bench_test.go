package storage

import (
	"context"
	"fmt"
	"testing"
)

// seedBenchStore builds an in-memory bank with a fixed number of memories, so a
// benchmark measures the retrieval path rather than setup.
func seedBenchStore(b *testing.B, bankID string, count int) *MemoryStore {
	b.Helper()
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID:       fmt.Sprintf("unit-%06d", i),
			BankID:   bankID,
			Text:     fmt.Sprintf("memory %d records that the deployment runs on day %d", i, i%7),
			FactType: TypeWorld,
			Tags:     []string{"ops"},
			ChunkID:  fmt.Sprintf("chunk-%06d", i),
		}); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

// BenchmarkRecallFusion measures the multi-arm retrieval and ranking path, which
// is the hot read for every reflection and search request.
func BenchmarkRecallFusion(b *testing.B) {
	store := seedBenchStore(b, "bench-bank", 1000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Recall(ctx, RecallQuery{
			BankID: "bench-bank",
			Query:  "deployment runs",
			Limit:  50,
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRecallFusionLargeBank measures the same path against a bank an order
// of magnitude larger, so a regression that is invisible at a small size shows
// up here.
func BenchmarkRecallFusionLargeBank(b *testing.B) {
	store := seedBenchStore(b, "bench-bank", 10000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Recall(ctx, RecallQuery{
			BankID: "bench-bank",
			Query:  "deployment runs",
			Limit:  50,
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRetainWrite measures the write path, which every ingest call takes.
func BenchmarkRetainWrite(b *testing.B) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "bench-bank", Name: "bench-bank"}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RetainWrite(ctx, RetainRequest{
			BankID: "bench-bank",
			Items: []RetainItem{{
				DocumentID: fmt.Sprintf("doc-%06d", i),
				Content:    "the deployment runs on Tuesdays and the rollback runs on Fridays.",
			}},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDocumentListing measures the paginated listing, which the read
// surface serves.
func BenchmarkDocumentListing(b *testing.B) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "bench-bank", Name: "bench-bank"}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := store.PutDocument(ctx, "bench-bank", fmt.Sprintf("doc-%06d", i), Document{
			OriginalText: fmt.Sprintf("document %d body", i),
			Tags:         []string{"ops"},
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ListDocuments(ctx, DocumentQuery{BankID: "bench-bank", Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGraphTraversal measures the graph read, which walks the units and
// their links.
func BenchmarkGraphTraversal(b *testing.B) {
	store := seedBenchStore(b, "bench-bank", 1000)
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		if err := store.UpsertLink(ctx, MemoryLink{
			BankID:     "bench-bank",
			FromUnitID: fmt.Sprintf("unit-%06d", i),
			ToUnitID:   fmt.Sprintf("unit-%06d", i+1),
			LinkType:   LinkSemantic,
			Weight:     1,
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Graph(ctx, GraphQuery{BankID: "bench-bank", Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

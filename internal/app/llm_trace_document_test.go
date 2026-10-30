package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// factsProvider answers the extraction call with one fact, so a retain over it
// writes a unit and emits a trace row.
type factsProvider struct{ result provider.CompletionResult }

func (f factsProvider) Name() string { return "openai" }

func (f factsProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	return f.result, nil
}

func (f factsProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (f factsProvider) VerifyConnection(context.Context) error { return nil }
func (f factsProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// A document's retain attributes its model calls to that document, which is
// what makes `GET .../llm-requests?document_id=` find them. The reference binds
// the same value per chunk (engine/retain/orchestrator.py:2849), and the route
// reads it back from the row's metadata (engine/memory_engine.py:14294-14299).
func TestRetainAttributesModelCallsToTheirDocument(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := factsProvider{result: provider.CompletionResult{
		Structured: map[string]any{"facts": []any{map[string]any{"text": "Alice works at Google."}}},
	}}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 100000, Model: "gpt-x"})
	extraction := newFactExtractor(traced, ExtractionSettings{})

	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "retain"})
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID:     "b1",
		Items:      []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-a"}},
		Extraction: extraction,
	}); err != nil {
		t.Fatal(err)
	}

	matching, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{
		BankID: "b1", DocumentID: "doc-a", DocumentIDSet: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matching.Requests) == 0 {
		t.Fatal("the document's retain emitted no trace row carrying its document id")
	}

	other, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{
		BankID: "b1", DocumentID: "doc-b", DocumentIDSet: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Requests) != 0 {
		t.Fatalf("a different document matched %d row(s); document ids are not being carried", len(other.Requests))
	}
}

// A call outside a document's retain carries no document id, so the filter
// cannot attribute it to a document that never made it.
func TestTraceRowWithoutDocumentCarriesNoDocumentID(t *testing.T) {
	store := storage.NewMemoryStore("public")
	inner := traceStubProvider{name: "openai", result: provider.CompletionResult{Content: "x"}}
	traced := TraceProvider(inner, store, config.ProviderConfig{TraceEnabled: true, TraceMaxChars: 1000, Model: "gpt-x"})
	ctx := provider.WithTraceContext(context.Background(), provider.TraceContext{BankID: "b1", Operation: "reflect"})
	if _, err := traced.Complete(ctx, provider.CompletionRequest{Scope: "reflect"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListLLMRequests(context.Background(), storage.LLMRequestQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 {
		t.Fatalf("rows = %d", len(page.Requests))
	}
	if _, ok := page.Requests[0].Metadata["document_id"]; ok {
		t.Fatalf("a call outside a document's retain reported document_id: %+v", page.Requests[0].Metadata)
	}
}

// The extractor reports the item's document to the layer that owns the model
// boundary, and nothing outside a document's retain.
func TestItemDocumentIDIsBoundPerChunk(t *testing.T) {
	if got := storage.ItemDocumentIDFrom(context.Background()); got != "" {
		t.Fatalf("an unbound context reported document %q", got)
	}
}

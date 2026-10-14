package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// completingProvider answers a completion with a scripted structured value, so
// the extraction path can be exercised without a live model.
type completingProvider struct {
	name       string
	structured any
}

func (p *completingProvider) Name() string                               { return p.name }
func (p *completingProvider) Dimensions() int                            { return provider.DeclaredDimensions() }
func (p *completingProvider) VerifyConnection(ctx context.Context) error { return nil }
func (p *completingProvider) Complete(_ context.Context, _ provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{Structured: p.structured}, nil
}
func (p *completingProvider) Embed(_ context.Context, _ provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}

// A write served through the composed application stores one unit per extracted
// fact when a model is configured, which is what proves the extractor is wired
// into the served handler rather than merely present in the tree.
func TestRetainExtractsFactsThroughServedHandler(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	model := &completingProvider{
		name: "openai-compatible",
		structured: map[string]any{
			"facts": []any{
				map[string]any{"text": "Alice works at Google.", "entities": []any{"Alice", "Google"}},
				map[string]any{"text": "Google is in California."},
			},
		},
	}
	application, err := New(context.Background(), testConfig(), Dependencies{Store: store, Provider: model})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"items":[{"content":"Alice works at Google. Google is in California.","document_id":"doc-1"}]}`
	rec := send(application, http.MethodPost, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("retain status = %d: %s", rec.Code, rec.Body.String())
	}

	units := store.UnitsForTest("b1")
	if len(units) != 2 {
		t.Fatalf("stored %d units, want one per extracted fact (2): %+v", len(units), units)
	}
	texts := make(map[string]bool, len(units))
	for _, unit := range units {
		texts[unit.Text] = true
	}
	for _, want := range []string{"Alice works at Google.", "Google is in California."} {
		if !texts[want] {
			t.Fatalf("extracted fact %q was not stored: %+v", want, texts)
		}
	}
}

// With no model configured the served write stores the item's text verbatim,
// so the default offline deployment behaves exactly as before.
func TestRetainStoresVerbatimWithoutModelThroughServedHandler(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	application, err := New(context.Background(), testConfig(), Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	content := "a single paragraph of prose"
	body, err := json.Marshal(map[string]any{
		"items": []any{map[string]any{"content": content, "document_id": "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := send(application, http.MethodPost, "/v1/default/banks/b1/memories", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("retain status = %d: %s", rec.Code, rec.Body.String())
	}
	units := store.UnitsForTest("b1")
	if len(units) != 1 || units[0].Text != content {
		t.Fatalf("expected the verbatim path: %+v", units)
	}
}

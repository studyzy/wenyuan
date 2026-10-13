package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// toolCallingProvider answers the first turn with a tool call and every later
// turn with an answer, which is what makes the retrieval loop run.
type toolCallingProvider struct {
	name   string
	turns  int
	answer string
}

func (p *toolCallingProvider) Name() string                               { return p.name }
func (p *toolCallingProvider) Dimensions() int                            { return provider.DeclaredDimensions() }
func (p *toolCallingProvider) VerifyConnection(ctx context.Context) error { return nil }
func (p *toolCallingProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (p *toolCallingProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	// A turn that offers tools is a retrieval turn; the final answering call
	// carries no tools, so it is answered in prose.
	if len(req.Tools) > 0 && p.turns == 0 {
		p.turns++
		return provider.CompletionResult{ToolCalls: []provider.ToolCall{{
			ID: "c1", Name: "recall", Arguments: []byte(`{"query":"alice"}`),
		}}}, nil
	}
	return provider.CompletionResult{Content: p.answer}, nil
}

// A reflection served through the composed application is driven by the model
// loop: the model's answer comes back rather than the deterministic synthesis.
func TestReflectUsesModelLoopThroughServedHandler(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(context.Background(), storage.RetainRequest{
		BankID: "b1",
		Items:  []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	model := &toolCallingProvider{name: "openai-compatible", answer: "Alice works at Google."}
	application, err := New(context.Background(), testConfig(), Dependencies{Store: store, Provider: model})
	if err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{"query": "where does Alice work?"})
	if err != nil {
		t.Fatal(err)
	}
	rec := send(application, http.MethodPost, "/v1/default/banks/b1/reflect", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("reflect status = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Text != model.answer {
		t.Fatalf("answer = %q, want the model's %q", response.Text, model.answer)
	}
	if model.turns == 0 {
		t.Fatal("the retrieval loop never ran, so the runner was not wired")
	}
}

// With no model configured the reflection falls back to the deterministic
// in-process loop, so the default offline deployment still answers.
func TestReflectFallsBackWithoutModel(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(context.Background(), storage.RetainRequest{
		BankID: "b1",
		Items:  []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	application, err := New(context.Background(), testConfig(), Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"query": "where does Alice work?"})
	if err != nil {
		t.Fatal(err)
	}
	rec := send(application, http.MethodPost, "/v1/default/banks/b1/reflect", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("reflect status = %d: %s", rec.Code, rec.Body.String())
	}
}

package mock

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// A completion echoes the last user turn, and embeddings are deterministic.
func TestMockProviderDeterministic(t *testing.T) {
	p := &Provider{}
	result, err := p.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
	})
	if err != nil || result.Content != "hello" {
		t.Fatalf("completion = %+v (err %v)", result, err)
	}
	first, err := p.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := p.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x"}})
	if len(first.Vectors) != 1 || first.Vectors[0][0] != second.Vectors[0][0] {
		t.Fatalf("embeddings must be deterministic")
	}
	if len(first.Vectors[0]) != provider.DeclaredDimensions() {
		t.Fatalf("embedding width = %d", len(first.Vectors[0]))
	}
}

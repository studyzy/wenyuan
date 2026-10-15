package memoryops

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// recordingEmbedder records the query text the recall handler embeds.
type recordingEmbedder struct {
	queryText string
}

func (r *recordingEmbedder) EmbedPassages(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (r *recordingEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	r.queryText = text
	return []float32{1, 0, 0}, nil
}

// With an embedding model configured, recall embeds the query text so the
// semantic arm can rank by vector similarity.
func TestRecallEmbedsQueryText(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	embedder := &recordingEmbedder{}
	h := NewHandlers(store, 5, 30000)
	h.Embeddings = embedder
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})

	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"hello world"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if embedder.queryText != "hello world" {
		t.Fatalf("embedded query text = %q, want %q", embedder.queryText, "hello world")
	}
}

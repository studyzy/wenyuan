package memoryops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The query is bounded at the HTTP ingress, before it reaches the embedder —
// one of the sinks an over-long query used to crash — and not only inside the
// store's retrieval.
func TestRecallTruncatesQueryBeforeEmbedding(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	tuning := storage.DefaultTuning()
	tuning.MaxQueryTokens = 5
	store.SetTuning(tuning)

	embedder := &recordingEmbedder{}
	h := NewHandlers(store, 5, 30000)
	h.Embeddings = embedder
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})

	query := strings.Repeat("x ", 40) + "zebra"
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", fmt.Sprintf(`{"query":%q}`, query))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	embedded := embedder.queryText
	if strings.Contains(embedded, "zebra") {
		t.Fatalf("the term past the cap reached the embedder: %q", embedded)
	}
	if storage.CountTokens(embedded) > tuning.MaxQueryTokens {
		t.Fatalf("the embedded query costs %d tokens, over the cap %d: %q",
			storage.CountTokens(embedded), tuning.MaxQueryTokens, embedded)
	}
}

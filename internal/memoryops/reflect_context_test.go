package memoryops

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// queryCapturingStore records the reflect query the handler resolved, so a test
// can assert the question the reflection actually ran on.
type queryCapturingStore struct {
	storage.Store
	queries []storage.ReflectQuery
}

func (s *queryCapturingStore) Reflect(ctx context.Context, query storage.ReflectQuery) (storage.ReflectOutcome, error) {
	s.queries = append(s.queries, query)
	return s.Store.Reflect(ctx, query)
}

// ts-0005: the deprecated context field is appended to the query the way the
// reference concatenates it — after a blank line, as an "Additional context"
// block — so the reflection reads the request as one question.
func TestReflectContextConcatenationCarriesTheBlock(t *testing.T) {
	_, store := newServer(t)
	capturing := &queryCapturingStore{Store: store}
	h := NewHandlers(capturing, 5, 30000)
	h.ReflectCatalog = storeCatalog{store: store}
	mux := bindHandlers(t, h)

	if code, _ := reflectBody(t, mux, "b1", `{"query":"q","context":"the caller's plan"}`); code != http.StatusOK {
		t.Fatalf("reflect with context must be accepted, got %d", code)
	}
	if len(capturing.queries) != 1 {
		t.Fatalf("reflect ran %d times, want one", len(capturing.queries))
	}
	if got := capturing.queries[0].Query; got != "q\n\nAdditional context: the caller's plan" {
		t.Fatalf("query = %q, want the reference's concatenation", got)
	}

	// No context: the query rides alone, unchanged.
	if code, _ := reflectBody(t, mux, "b1", `{"query":"q"}`); code != http.StatusOK {
		t.Fatalf("reflect without context must be accepted, got %d", code)
	}
	if got := capturing.queries[1].Query; got != "q" {
		t.Fatalf("query = %q, want it unchanged without context", got)
	}
}

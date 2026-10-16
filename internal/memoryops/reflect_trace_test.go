package memoryops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// capturingStore records the context a reflect run was started with, so a test
// can assert the attribution the run's model calls are recorded under.
type capturingStore struct {
	storage.Store
	ctx context.Context
}

func (s *capturingStore) Reflect(ctx context.Context, _ storage.ReflectQuery) (storage.ReflectOutcome, error) {
	s.ctx = ctx
	return storage.ReflectOutcome{}, nil
}

// A reflect run carries its bank and operation into the model calls it makes.
//
// Without it every reflection's calls are recorded with no attribution, so a
// per-bank trace view shows them nowhere: the frontend reported "1 entry" while
// the table held a dozen reflections, and filtering by Reflect returned none.
func TestReflectAttributesItsModelCalls(t *testing.T) {
	store := &capturingStore{Store: storage.NewMemoryStore("public")}
	handlers := NewHandlers(store, 5, 30000)

	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/team%2Falpha/reflect",
		strings.NewReader(`{"query":"who worked on Mercury?"}`))
	req.SetPathValue("bank_id", "team/alpha")
	rec := httptest.NewRecorder()
	handlers.Reflect(rec, req)

	if store.ctx == nil {
		t.Fatalf("reflect was not run; status = %d", rec.Code)
	}
	attribution, ok := provider.TraceContextFrom(store.ctx)
	if !ok {
		t.Fatal("the reflect run carries no trace attribution, so its model calls are recorded unattributed")
	}
	if attribution.BankID != "team/alpha" || attribution.Operation != "reflect" {
		t.Fatalf("attribution = %+v, want the addressed bank and the reflect operation", attribution)
	}
}

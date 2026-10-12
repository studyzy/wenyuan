package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// storeCatalog adapts the in-memory store to the reflection catalog port so
// tests can seed perspectives and directives per bank.
type storeCatalog struct{ store *storage.MemoryStore }

func (c storeCatalog) MentalModels(bankID string) []storage.ReflectMentalModel {
	return c.store.MentalModels(bankID)
}

func (c storeCatalog) Directives(bankID string) []storage.ReflectDirective {
	return c.store.Directives(bankID)
}

// reflectServer wires a handler set with the reflection catalog bound.
func reflectServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	_, store := newServer(t)
	h := NewHandlers(store, 5, 30000)
	h.ReflectCatalog = storeCatalog{store: store}
	return bindHandlers(t, h), store
}

// reflectBody posts a reflection and decodes the JSON object.
func reflectBody(t *testing.T, mux *http.ServeMux, bankID, body string) (int, map[string]any) {
	t.Helper()
	rec := post(t, mux, "/v1/default/banks/"+bankID+"/reflect", body)
	decoded := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	}
	return rec.Code, decoded
}

// failingStore wraps a Store and forces Reflect to return an injected error.
type failingStore struct {
	storage.Store
	err error
}

func (s failingStore) Reflect(ctx context.Context, query storage.ReflectQuery) (storage.ReflectOutcome, error) {
	if s.err == nil {
		return s.Store.Reflect(ctx, query)
	}
	return storage.ReflectOutcome{}, s.err
}

// noAnswerStore wraps a Store and forces a no-answer reflection outcome.
type noAnswerStore struct{ storage.Store }

func (s noAnswerStore) Reflect(ctx context.Context, query storage.ReflectQuery) (storage.ReflectOutcome, error) {
	return storage.ReflectOutcome{}, storage.ErrReflectNoAnswer
}

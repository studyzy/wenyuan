package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A body that is the JSON literal null is the framework's missing required
// body, not a value its model cannot read fields from; a list body keeps the
// model_attributes_type item.
func TestPreviewNullBodyIsMissing(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodPost, base+"/consolidation-strategies/preview", "null")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null body: %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail []struct {
			Type  string `json:"type"`
			MSG   string `json:"msg"`
			Loc   []any  `json:"loc"`
			Input any    `json:"input"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Detail) != 1 || envelope.Detail[0].Type != "missing" ||
		envelope.Detail[0].MSG != "Field required" || len(envelope.Detail[0].Loc) != 1 || envelope.Detail[0].Loc[0] != "body" {
		t.Fatalf("null body item = %s", rec.Body.String())
	}

	list := send(mux, http.MethodPost, base+"/consolidation-strategies/preview", "[]")
	if list.Code != http.StatusUnprocessableEntity || !strings.Contains(list.Body.String(), `"model_attributes_type"`) {
		t.Fatalf("list body = %d, %s", list.Code, list.Body.String())
	}
}

// statsInvalidationSpy records the cache evictions the handler asks for.
type statsInvalidationSpy struct {
	*storage.MemoryStore
	invalidated []string
}

func (s *statsInvalidationSpy) InvalidateBankStats(ctx context.Context, bankID string) {
	s.invalidated = append(s.invalidated, bankID)
}

// newServerWithSpy builds the family's routes over an instrumented store so a
// case can see the cache evictions the handlers request.
func newServerWithSpy(t *testing.T) (*http.ServeMux, *statsInvalidationSpy) {
	t.Helper()
	store := &statsInvalidationSpy{MemoryStore: storage.NewMemoryStore("public")}
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bank,
		Items:  []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		if !strings.Contains(pattern, "{bank_id}") {
			mux.HandleFunc(method+" "+pattern, handler)
			return
		}
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			for _, param := range []string{"memory_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	return mux, store
}

// A bank-wide clear drops the cached summary so the next stats read recomputes;
// the memory-scoped clear leaves the cache alone, as the reference does.
func TestBankWideClearInvalidatesStats(t *testing.T) {
	mux, store := newServerWithSpy(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodGet, base+"/stats", ""); rec.Code != http.StatusOK {
		t.Fatalf("stats: %d", rec.Code)
	}
	if rec := send(mux, http.MethodDelete, base+"/memories/"+memoryID(t, mux)+"/observations", ""); rec.Code != http.StatusOK {
		t.Fatalf("memory clear: %d", rec.Code)
	}
	if len(store.invalidated) != 0 {
		t.Fatalf("memory-scoped clear invalidated %v, want none", store.invalidated)
	}
	if rec := send(mux, http.MethodDelete, base+"/observations", ""); rec.Code != http.StatusOK {
		t.Fatalf("bank clear: %d", rec.Code)
	}
	if len(store.invalidated) != 1 || store.invalidated[0] != bank {
		t.Fatalf("bank clear invalidated %v, want [%s]", store.invalidated, bank)
	}
}

// An explicit but empty observation_scopes list is a scoped submit: it never
// deduplicates against a pending full-bank pass, while an absent or null one
// does.
func TestEmptyObservationScopesDoesNotDeduplicate(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	first := send(mux, http.MethodPost, base+"/consolidate", "")
	if first.Code != http.StatusOK {
		t.Fatalf("first consolidate: %d, body = %s", first.Code, first.Body.String())
	}
	var full ConsolidationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}

	empty := send(mux, http.MethodPost, base+"/consolidate", `{"observation_scopes":[]}`)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty-scope consolidate: %d, body = %s", empty.Code, empty.Body.String())
	}
	var scoped ConsolidationResponse
	if err := json.Unmarshal(empty.Body.Bytes(), &scoped); err != nil {
		t.Fatal(err)
	}
	if scoped.Deduplicated || scoped.OperationID == full.OperationID {
		t.Fatalf("empty scope deduplicated: %+v vs %+v", scoped, full)
	}

	nulled := send(mux, http.MethodPost, base+"/consolidate", `{"observation_scopes":null}`)
	if nulled.Code != http.StatusOK {
		t.Fatalf("null-scope consolidate: %d", nulled.Code)
	}
	var reused ConsolidationResponse
	if err := json.Unmarshal(nulled.Body.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if !reused.Deduplicated {
		t.Fatalf("null scope did not deduplicate against a pending pass: %+v", reused)
	}
}

package app

import (
	"context"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// recallSpy records the recall queries the consolidation lookup makes, so a
// test can assert the tuning the pass asked for.
type recallSpy struct {
	*storage.MemoryStore
	mu      sync.Mutex
	queries []storage.RecallQuery
}

func (s *recallSpy) Recall(ctx context.Context, q storage.RecallQuery) (storage.RecallResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()
	return storage.RecallResult{}, nil
}

func (s *recallSpy) lastQuery() storage.RecallQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[len(s.queries)-1]
}

// test-consolidation-recall-budget: the recall the consolidation lookup runs
// carries the deployment's configured budget level and token budget, rather than
// a hardcoded level.
func TestConsolidationLookupUsesConfiguredRecallTuning(t *testing.T) {
	store := storage.NewMemoryStore("public")
	tuning := storage.DefaultTuning()
	tuning.Consolidation.RecallBudget = "high"
	tuning.Consolidation.MaxTokens = 777
	store.SetTuning(tuning)
	spy := &recallSpy{MemoryStore: store}
	lookup := consolidationStore{store: spy}

	if _, err := lookup.RelatedObservations(context.Background(), "bank-a", nil, []string{"a fact"}, 3); err != nil {
		t.Fatal(err)
	}
	query := spy.lastQuery()
	if query.Budget != "high" {
		t.Fatalf("recall budget = %q, want the configured high", query.Budget)
	}
	if query.MaxTokens != 777 {
		t.Fatalf("recall max tokens = %d, want the configured 777", query.MaxTokens)
	}
}

// A store that does not carry the deployment's consolidation tuning leaves the
// lookup on the reference's defaults, so an unset key changes nothing.
func TestConsolidationLookupDefaultsWithoutTuningStore(t *testing.T) {
	store := storage.NewMemoryStore("public")
	spy := &recallSpy{MemoryStore: store}
	lookup := consolidationStore{store: bareStore{Store: spy}}

	if _, err := lookup.RelatedObservations(context.Background(), "bank-a", nil, []string{"a fact"}, 3); err != nil {
		t.Fatal(err)
	}
	query := spy.lastQuery()
	if query.Budget != storage.DefaultConsolidationRecallBudget {
		t.Fatalf("recall budget = %q, want the default %q", query.Budget, storage.DefaultConsolidationRecallBudget)
	}
	if query.MaxTokens != storage.DefaultConsolidationTuning().MaxTokens {
		t.Fatalf("recall max tokens = %d, want the default %d", query.MaxTokens, storage.DefaultConsolidationTuning().MaxTokens)
	}
}

// bareStore exposes only the storage.Store interface, so it hides the optional
// consolidation tuning the concrete store carries.
type bareStore struct{ storage.Store }

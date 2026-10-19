package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The consolidation-strategy preview's draft may carry keys the reference's
// request model does not declare — the bank-wide batch/parallelism/round-cap
// settings of a consolidation_strategies entry among them. The model is
// non-strict, so such a key is dropped without checking its type: even a value
// that could never be a setting leaves the request valid. Declaring the keys
// here instead (to type-check them) would reject bodies the reference accepts.
func TestConsolidationPreviewIgnoresUndeclaredStrategySettings(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := boundMux(store)
	base := "/v1/default/banks/" + bank

	for _, value := range []string{`"x"`, "1.5", `{"a":1}`, "[1,2]", "4"} {
		body := `{"strategies":[{"scopes":[{"tags":["company:*"]}],"consolidation_llm_batch_size":` + value + `}]}`
		rec := send(mux, http.MethodPost, base+"/consolidation-strategies/preview", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview with consolidation_llm_batch_size=%s = %d, want 200 (body %s)",
				value, rec.Code, rec.Body.String())
		}
		var preview ConsolidationStrategiesPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
			t.Fatal(err)
		}
		// A dropped setting makes no strategy apply, exactly as the reference has it.
		if len(preview.Strategies) != 1 || preview.Strategies[0].Active {
			t.Fatalf("strategies = %+v, want one inactive strategy", preview.Strategies)
		}
	}
	// The settings the model does declare keep their items.
	bad := send(mux, http.MethodPost, base+"/consolidation-strategies/preview",
		`{"strategies":[{"max_observations_per_scope":"x"}]}`)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("string per-scope cap = %d, want 422 (body %s)", bad.Code, bad.Body.String())
	}
	if want := `"loc":["body","strategies",0,"max_observations_per_scope"]`; !strings.Contains(bad.Body.String(), want) {
		t.Fatalf("item = %s, want the field located", bad.Body.String())
	}
}

// The reference's retry is one UPDATE filtered by bank_id with no bank
// existence check, so it answers 200 with a zero count for a bank that was
// never created rather than 404ing.
func TestConsolidationRecoverUnknownBankRetriesNothing(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := boundMux(store)

	rec := send(mux, http.MethodPost, "/v1/default/banks/never-created/consolidation/recover", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("recover = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var response RecoverConsolidationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RetriedCount != 0 {
		t.Fatalf("retried_count = %d, want 0", response.RetriedCount)
	}
}

// boundMux serves the family's routes from the given store.
func boundMux(store storage.Store) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}

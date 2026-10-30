package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedScopedObservationScopes stores one observation per scope: two of kate's
// private scopes, dan's one, and a rule dan's scope admits.
func seedScopedObservationScopes(t *testing.T) storage.Store {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "o-kate", BankID: bank, Text: "Kate is interviewing", FactType: storage.TypeObservation, Tags: []string{"user:kate"}},
		{ID: "o-kate2", BankID: bank, Text: "Kate is relocating", FactType: storage.TypeObservation, Tags: []string{"user:kate", "topic:career"}},
		{ID: "o-dan", BankID: bank, Text: "Dan deploys", FactType: storage.TypeObservation, Tags: []string{"user:dan"}},
		{ID: "o-rule", BankID: bank, Text: "Nobody deploys on Fridays", FactType: storage.TypeObservation, Tags: []string{"user:kate", "kind:rule"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// The observation-scope histogram is confined the way every other tag-scoped
// read is: the reference AND-s the caller's resolved scope into the count query
// (engine/memories/pg/counts.py:172-202, reached from memory_engine.py:12438),
// so a scoped caller must not learn that other scopes exist. An unscoped caller
// still sees every scope.
func TestObservationScopesHonorTagScope(t *testing.T) {
	handler := scopedHandler(t, seedScopedObservationScopes(t))
	base := "/v1/default/banks/" + bank

	var admin ObservationScopesResponse
	rec := sendKeyed(handler, "", http.MethodGet, base+"/observations/scopes")
	if rec.Code != http.StatusOK {
		t.Fatalf("unscoped = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	if admin.Total != 4 || len(admin.Scopes) != 4 {
		t.Fatalf("unscoped scopes = %d/%d, want 4/4", admin.Total, len(admin.Scopes))
	}

	var dan ObservationScopesResponse
	rec = sendKeyed(handler, "dan", http.MethodGet, base+"/observations/scopes")
	if rec.Code != http.StatusOK {
		t.Fatalf("dan = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dan); err != nil {
		t.Fatal(err)
	}
	for _, scope := range dan.Scopes {
		if len(scope.Tags) == 1 && scope.Tags[0] == "user:kate" {
			t.Fatalf("dan's scope histogram leaked kate's private scope: %+v", dan.Scopes)
		}
	}
	// Dan's scope is `user:dan` OR `kind:rule`, so it admits his own scope and
	// the shared rule scope, and refuses kate's private one.
	if dan.Total != 2 || len(dan.Scopes) != 2 {
		t.Fatalf("dan scopes = %+v (total %d), want his own and the shared rule", dan.Scopes, dan.Total)
	}
}

// The consolidation-strategy preview builds from the same histogram, so it is
// confined by the same rule (memory_engine.py:12394-12405 delegates to the
// scoped list_observation_scopes call).
func TestConsolidationPreviewHonorsTagScope(t *testing.T) {
	handler := scopedHandler(t, seedScopedObservationScopes(t))
	base := "/v1/default/banks/" + bank
	body := `{"strategies":[{"name":"career","scopes":[{"tags":["user:kate","topic:career"],"match":"all_strict"}]}]}`

	rec := sendKeyedBody(handler, "dan", http.MethodPost, base+"/consolidation-strategies/preview", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dan preview = %d: %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Strategies []struct {
			Rules []struct {
				MatchCount int `json:"match_count"`
			} `json:"rules"`
		} `json:"strategies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Strategies) != 1 || len(preview.Strategies[0].Rules) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	// Kate's career scope is outside dan's reach, so his preview never counts it.
	if got := preview.Strategies[0].Rules[0].MatchCount; got != 0 {
		t.Fatalf("dan's preview matched kate's private scope %d time(s); the histogram must be confined", got)
	}

	// The unscoped operator still walks it, which dan's histogram never did.
	rec = sendKeyedBody(handler, "", http.MethodPost, base+"/consolidation-strategies/preview", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator preview = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if got := preview.Strategies[0].Rules[0].MatchCount; got != 1 {
		t.Fatalf("operator preview matched kate's scope %d time(s), want 1", got)
	}
}

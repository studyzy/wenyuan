package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedScopedTimeseries stores one memory per tag set: two of kate's, one of
// dan's, and one rule dan's read scope admits. Every row lands in the same
// period, so the bucket counts are what tells the scopes apart.
func seedScopedTimeseries(t *testing.T) storage.Store {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "ts-kate", BankID: bank, Text: "Kate is interviewing", FactType: storage.TypeWorld, Tags: []string{"user:kate"}},
		{ID: "ts-kate2", BankID: bank, Text: "Kate is relocating", FactType: storage.TypeExperience, Tags: []string{"user:kate", "topic:career"}},
		{ID: "ts-dan", BankID: bank, Text: "Dan deploys", FactType: storage.TypeWorld, Tags: []string{"user:dan"}},
		{ID: "ts-rule", BankID: bank, Text: "Nobody deploys on Fridays", FactType: storage.TypeWorld, Tags: []string{"user:kate", "kind:rule"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// bucketCount returns how many memories the served chart reports in total
// across its buckets.
func bucketCount(t *testing.T, payload MemoriesTimeseriesResponse) int {
	t.Helper()
	total := 0
	for _, bucket := range payload.Buckets {
		total += bucket.World + bucket.Experience + bucket.Observation
	}
	return total
}

// The memories-ingested chart is confined the way every other tag-scoped read
// is: the reference resolves the caller's read scope and passes it as the tag
// groups of the bucketing query (engine/memory_engine.py:17046,17070 →
// engine/memories/pg/counts.py:138-148). A scoped caller therefore charts only
// the memories its scope admits, while an unscoped caller still sees the bank.
func TestMemoriesTimeseriesHonorsTagScope(t *testing.T) {
	handler := scopedHandler(t, seedScopedTimeseries(t))
	base := "/v1/default/banks/" + bank + "/stats/memories-timeseries"

	var admin MemoriesTimeseriesResponse
	rec := sendKeyed(handler, "", http.MethodGet, base)
	if rec.Code != http.StatusOK {
		t.Fatalf("unscoped = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	if got := bucketCount(t, admin); got != 4 {
		t.Fatalf("unscoped counts %d memories, want the bank's 4", got)
	}

	// Dan's scope is `user:dan` OR `kind:rule`, so it admits his own memory and
	// the shared rule, and refuses both of kate's private ones.
	var dan MemoriesTimeseriesResponse
	rec = sendKeyed(handler, "dan", http.MethodGet, base)
	if rec.Code != http.StatusOK {
		t.Fatalf("dan = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dan); err != nil {
		t.Fatal(err)
	}
	if got := bucketCount(t, dan); got != 2 {
		t.Fatalf("dan counts %d memories, want his own and the shared rule: %+v", got, dan.Buckets)
	}

	// Kate's scope still admits the rule row, which she may read.
	var kate MemoriesTimeseriesResponse
	rec = sendKeyed(handler, "kate", http.MethodGet, base)
	if rec.Code != http.StatusOK {
		t.Fatalf("kate = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &kate); err != nil {
		t.Fatal(err)
	}
	if got := bucketCount(t, kate); got != 3 {
		t.Fatalf("kate counts %d memories, want her two and the shared rule: %+v", got, kate.Buckets)
	}
}

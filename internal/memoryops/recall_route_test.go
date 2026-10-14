package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ts-0001: the route is bound under the versioned bank-scoped prefix and
// answers POST with the documented path.
func TestRecallRouteBoundUnderBankPrefix(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// beh-0010 / ts-0015: results belong to the addressed bank only.
func TestRecallIsBankScoped(t *testing.T) {
	mux, store := newServer(t)
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	store.SeedMemory("b1", storage.RecallMatch{ID: "one", Text: "alpha", Type: "world"})
	store.SeedMemory("b2", storage.RecallMatch{ID: "two", Text: "alpha", Type: "world"})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","max_tokens":100}`)
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, item := range response.Results {
		if item.ID == "two" {
			t.Fatalf("recall returned a memory from another bank: %s", rec.Body.String())
		}
	}
}

// ts-0007: a non-positive token cap returns no facts while chunks may still be
// returned independently.
func TestRecallNonPositiveCapReturnsNoFactsButChunks(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha beta", Type: "world", ChunkID: "c1"})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"alpha","max_tokens":0,"include":{"chunks":{}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("non-positive cap must return no facts: %s", rec.Body.String())
	}
}

// ts-0013: a temporal window with an inverted bound order is rejected, while a
// valid one is accepted.
func TestRecallTemporalWindowBounds(t *testing.T) {
	mux, _ := newServer(t)
	valid := post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"q","temporal_window":{"start":"2026-01-01T00:00:00Z","end":"2026-12-31T00:00:00Z"}}`)
	if valid.Code != http.StatusOK {
		t.Fatalf("valid window status = %d, body = %s", valid.Code, valid.Body.String())
	}
	malformed := post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"q","temporal_window":{"start":"nope","end":"2026-12-31T00:00:00Z"}}`)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed window status = %d, want 400", malformed.Code)
	}
	inverted := post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"q","temporal_window":{"start":"2026-12-31T00:00:00Z","end":"2026-01-01T00:00:00Z"}}`)
	if inverted.Code != http.StatusUnprocessableEntity {
		t.Fatalf("inverted window status = %d, want 422", inverted.Code)
	}
}

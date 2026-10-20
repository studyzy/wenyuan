package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A recalled observation reports `context: null`, while a fact retained without
// a context reports "": the reference's observation insert never names the
// context column (engine/memories/pg/consolidation.py:268-320) and its recall
// result passes the value through unchanged (engine/memory_engine.py:10517).
// Folding the NULL into "" erases the difference on the wire.
func TestRecallRendersAbsentContextAsNull(t *testing.T) {
	mux, store := newServer(t)
	ctx := context.Background()
	fact, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		BankID: "b1", Text: "the deployment runs on Tuesdays", FactType: storage.TypeWorld,
	})
	if err != nil {
		t.Fatal(err)
	}
	observationID, err := store.CreateObservation(ctx, "b1", "deployments happen weekly", []string{fact.ID})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"query":"deployments","types":["observation"],"limit":10,"prefer_observations":false}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	for _, item := range response.Results {
		if id, _ := item["id"].(string); id != observationID {
			continue
		}
		raw, present := item["context"]
		if !present || raw != nil {
			t.Fatalf("observation context = %#v, want null", raw)
		}
		return
	}
	t.Fatalf("recall did not return the observation: %s", rec.Body.String())
}

// A fact's empty context stays "": only the observation's NULL renders as null.
func TestRecallKeepsAFactsEmptyContextAsAString(t *testing.T) {
	mux, store := newServer(t)
	fact, err := store.UpsertUnit(context.Background(), storage.MemoryUnit{
		BankID: "b1", Text: "the deployment runs on Tuesdays", FactType: storage.TypeWorld,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall",
		strings.NewReader(`{"query":"Tuesdays","types":["world"],"limit":10}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	for _, item := range response.Results {
		if id, _ := item["id"].(string); id != fact.ID {
			continue
		}
		raw, present := item["context"]
		if !present || raw != "" {
			t.Fatalf("fact context = %#v, want the empty string", raw)
		}
		return
	}
	t.Fatalf("recall did not return the fact: %s", rec.Body.String())
}

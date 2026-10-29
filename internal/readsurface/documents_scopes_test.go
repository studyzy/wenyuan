package readsurface

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A q listing reports which fields each item matched and never estimates a
// listing without a q.
func TestDocumentListMatchedFields(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	var list DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents?q=DOC", ""), &list)
	if list.Total != 1 || list.TotalIsEstimate {
		t.Fatalf("q=DOC total = %d estimate = %v, want 1 false", list.Total, list.TotalIsEstimate)
	}
	fields := list.Items[0].MatchedFields
	if len(fields) != 1 || fields[0] != "id" {
		t.Fatalf("matched_fields = %v, want [id]", fields)
	}
	// The listing renders matched_fields as an empty array without q, never null.
	rec := send(mux, http.MethodGet, base+"/documents", "")
	var raw struct {
		Items           []map[string]json.RawMessage `json:"items"`
		TotalIsEstimate *bool                        `json:"total_is_estimate"`
	}
	decodeInto(t, rec, &raw)
	if string(raw.Items[0]["matched_fields"]) != "[]" {
		t.Fatalf("matched_fields without q = %s, want []", raw.Items[0]["matched_fields"])
	}
	if raw.TotalIsEstimate == nil || *raw.TotalIsEstimate {
		t.Fatalf("total_is_estimate = %v, want false", raw.TotalIsEstimate)
	}
}

// PATCH accepts observation_scopes on its own, records it on the document so a
// reprocess keeps it, and rejects a body naming neither mutable field or an
// unknown mode.
func TestUpdateDocumentObservationScopes(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank
	url := base + "/documents/doc-1"

	if rec := send(mux, http.MethodPatch, url, `{"observation_scopes": "shared"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH observation_scopes: %d, body = %s", rec.Code, rec.Body.String())
	}
	var detail DocumentResponse
	decodeInto(t, send(mux, http.MethodGet, url, ""), &detail)
	if detail.ObservationScopes != "shared" {
		t.Fatalf("document observation_scopes = %#v, want shared", detail.ObservationScopes)
	}
	doc, err := store.GetDocument(context.Background(), bank, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.RetainParams["observation_scopes"] != "shared" {
		t.Fatalf("retain params scopes = %#v, want shared", doc.RetainParams["observation_scopes"])
	}

	// An explicit list keeps its shape on the document and on every moved memory.
	if rec := send(mux, http.MethodPatch, url, `{"observation_scopes": [["work"]]}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH explicit scopes: %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, unit := range documentUnits(t, store) {
		var scopes [][]string
		if err := json.Unmarshal(unit.ObservationScopes, &scopes); err != nil {
			t.Fatalf("unit scopes %s: %v", unit.ObservationScopes, err)
		}
		if len(scopes) != 1 || len(scopes[0]) != 1 || scopes[0][0] != "work" {
			t.Fatalf("unit scopes = %s, want [[work]]", unit.ObservationScopes)
		}
	}

	// An unknown mode is the framework's 422 for the field, not a plain detail.
	rec := send(mux, http.MethodPatch, url, `{"observation_scopes": "everything"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown mode: %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var failure struct {
		Detail []struct {
			Loc []string `json:"loc"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil || len(failure.Detail) != 1 {
		t.Fatalf("422 body = %s", rec.Body.String())
	}
	if loc := failure.Detail[0].Loc; len(loc) != 2 || loc[0] != "body" || loc[1] != "observation_scopes" {
		t.Fatalf("422 loc = %v, want [body observation_scopes]", loc)
	}
	if rec := send(mux, http.MethodPatch, url, `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty body: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodPatch, base+"/documents/nope", `{"observation_scopes": "shared"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing document: %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	// A scope-only update leaves the document's tags alone.
	if detail.Tags == nil || len(detail.Tags) != 1 || detail.Tags[0] != "work" {
		t.Fatalf("tags after a scope-only PATCH = %v, want [work]", detail.Tags)
	}
}

// documentUnits returns the source facts the fixture document owns.
func documentUnits(t *testing.T, store *storage.MemoryStore) []storage.MemoryUnit {
	t.Helper()
	page, err := store.ListMemories(context.Background(), storage.MemoryQuery{
		BankID: bank, DocumentID: "doc-1", Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return page.Memories
}

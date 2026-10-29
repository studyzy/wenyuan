package readsurface

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// A q listing that stops at the match cap serves the cap as total and says so:
// total_is_estimate marks total a lower bound, the way the reference serves the
// envelope past its 1,000-match cap.
func TestDocumentListServesTotalIsEstimate(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	for i := 0; i < storage.DocumentSearchCap+5; i++ {
		if _, err := store.PutDocument(ctx, bank, fmt.Sprintf("match-%d", i), storage.Document{
			OriginalText: "shared searchable text",
		}); err != nil {
			t.Fatal(err)
		}
	}
	mux := serve(t, store)

	var list DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents?q=SEARCH&limit=5", ""), &list)
	if list.Total != storage.DocumentSearchCap || !list.TotalIsEstimate {
		t.Fatalf("capped listing total = %d estimate = %v, want %d true",
			list.Total, list.TotalIsEstimate, storage.DocumentSearchCap)
	}
	if len(list.Items) != 5 {
		t.Fatalf("page = %d, want 5", len(list.Items))
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

	// A value that fits neither branch of the field's union is answered with the
	// framework's failure for both branches, in branch order: the literal's
	// members first, then the list of tag groups.
	rec := send(mux, http.MethodPatch, url, `{"observation_scopes": "everything"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown mode: %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	items := patchValidationItems(t, rec)
	if len(items) != 2 {
		t.Fatalf("unknown mode items = %d (%s), want 2", len(items), rec.Body.String())
	}
	if items[0]["type"] != "literal_error" || items[0]["msg"] !=
		"Input should be 'per_tag', 'combined', 'all_combinations' or 'shared'" {
		t.Fatalf("literal item = %v", items[0])
	}
	if loc := items[0]["loc"].([]any); len(loc) != 3 || loc[2] !=
		"literal['per_tag','combined','all_combinations','shared']" {
		t.Fatalf("literal loc = %v", loc)
	}
	if items[1]["type"] != "list_type" || items[1]["msg"] != "Input should be a valid list" {
		t.Fatalf("list item = %v", items[1])
	}
	if loc := items[1]["loc"].([]any); len(loc) != 3 || loc[2] != "list[list[str]]" {
		t.Fatalf("list loc = %v", loc)
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

// A flat list is not a list of tag groups: the union reports the literal's
// failure once and the list branch's once per element that is not a group, and
// a non-string tag inside a group is named at its own position.
func TestUpdateDocumentObservationScopesUnionFailures(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	url := "/v1/default/banks/" + bank + "/documents/doc-1"

	rec := send(mux, http.MethodPatch, url, `{"observation_scopes": ["work", "play"]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("flat list: %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	items := patchValidationItems(t, rec)
	if len(items) != 3 {
		t.Fatalf("flat list items = %d (%s), want 3", len(items), rec.Body.String())
	}
	if items[0]["type"] != "literal_error" {
		t.Fatalf("first item = %v, want literal_error", items[0])
	}
	for index, input := range []string{"work", "play"} {
		item := items[index+1]
		if item["type"] != "list_type" || item["input"] != input {
			t.Fatalf("element item %d = %v, want list_type input %s", index, item, input)
		}
		// The framework renders an element's position as a number.
		loc := item["loc"].([]any)
		if len(loc) != 4 || loc[2] != "list[list[str]]" || loc[3] != float64(index) {
			t.Fatalf("element item %d loc = %v", index, loc)
		}
	}

	// A non-string tag inside a group is the list branch's member failure.
	rec = send(mux, http.MethodPatch, url, `{"observation_scopes": [["work", 5]]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-string tag: %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	items = patchValidationItems(t, rec)
	if len(items) != 2 || items[1]["type"] != "string_type" {
		t.Fatalf("non-string tag items = %v", items)
	}
	loc := items[1]["loc"].([]any)
	if len(loc) != 5 || loc[3] != float64(0) || loc[4] != float64(1) {
		t.Fatalf("tag loc = %v", loc)
	}
}

// An explicit null is the union's admitted None: a body naming only it names no
// field, and one naming it beside tags updates the tags alone, leaving the
// scoping the document already carries in place.
func TestUpdateDocumentObservationScopesNullIsAbsent(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank
	url := base + "/documents/doc-1"

	if rec := send(mux, http.MethodPatch, url, `{"observation_scopes": [["work"]]}`); rec.Code != http.StatusOK {
		t.Fatalf("seed scopes: %d (body %s)", rec.Code, rec.Body.String())
	}

	var failure struct {
		Detail string `json:"detail"`
	}
	rec := send(mux, http.MethodPatch, url, `{"observation_scopes": null}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null alone: %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil ||
		failure.Detail != "At least one field (tags, observation_scopes) must be provided" {
		t.Fatalf("null alone body = %s", rec.Body.String())
	}

	if rec := send(mux, http.MethodPatch, url, `{"tags":["fresh"],"observation_scopes":null}`); rec.Code != http.StatusOK {
		t.Fatalf("null beside tags: %d (body %s)", rec.Code, rec.Body.String())
	}
	var detail DocumentResponse
	decodeInto(t, send(mux, http.MethodGet, url, ""), &detail)
	if len(detail.Tags) != 1 || detail.Tags[0] != "fresh" {
		t.Fatalf("tags = %v, want [fresh]", detail.Tags)
	}
	scopes, ok := detail.ObservationScopes.([]any)
	if !ok || len(scopes) != 1 {
		t.Fatalf("observation_scopes = %#v, want the seeded [[work]]", detail.ObservationScopes)
	}
	if params, ok := detail.RetainParams["observation_scopes"]; !ok || params == nil {
		t.Fatalf("retain params lost the scopes: %#v", detail.RetainParams)
	}
}

// patchValidationItems decodes a 422's detail items.
func patchValidationItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var failure struct {
		Detail []map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode 422 body %s: %v", rec.Body.String(), err)
	}
	return failure.Detail
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

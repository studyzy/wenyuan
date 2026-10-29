package app

// Contract-parity cases for the document read and update surface: the v1.1.0
// `q` search over the id and the stored text, and a PATCH that moves a
// document's memories to another observation scoping without re-extracting them.

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The document listing's q searches the id and the stored text, says which of
// the two matched each item, and marks nothing as estimated for a small search.
func TestParityDocumentListQSearch(t *testing.T) {
	a := parityApp(t)
	bankURL := "/v1/default/banks/search_bank"
	if rec := exchange(a, http.MethodPut, bankURL, `{"name":"Search Bank"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}
	for _, item := range []struct{ documentID, content string }{
		{"report-2024", "quarterly numbers"},
		{"notes", "the report landed late"},
		{"other", "nothing about it"},
	} {
		body := `{"items":[{"content":"` + item.content + `","document_id":"` + item.documentID + `"}]}`
		if rec := exchange(a, http.MethodPost, bankURL+"/memories", body); rec.Code != http.StatusOK {
			t.Fatalf("retain %s = %d (body %s)", item.documentID, rec.Code, rec.Body.String())
		}
	}

	rec := exchange(a, http.MethodGet, bankURL+"/documents?q=REPORT", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list documents = %d (body %s)", rec.Code, rec.Body.String())
	}
	list := decodeJSON(t, rec)
	if list["total"] != float64(2) || list["total_is_estimate"] != false {
		t.Fatalf("q=REPORT envelope = %v", list)
	}
	items, _ := list["items"].([]any)
	matched := map[string]any{}
	for _, entry := range items {
		doc, _ := entry.(map[string]any)
		matched[doc["id"].(string)] = doc["matched_fields"]
	}
	if got := matched["report-2024"]; !sameStringList(got, "id") {
		t.Fatalf("id match fields = %v, want [id]", got)
	}
	if got := matched["notes"]; !sameStringList(got, "original_text") {
		t.Fatalf("text match fields = %v, want [original_text]", got)
	}

	// Without q every item carries an empty list, never null.
	rec = exchange(a, http.MethodGet, bankURL+"/documents", "")
	raw := decodeJSON(t, rec)
	first, _ := raw["items"].([]any)[0].(map[string]any)
	if fields, ok := first["matched_fields"].([]any); !ok || len(fields) != 0 {
		t.Fatalf("matched_fields without q = %#v, want []", first["matched_fields"])
	}
	if raw["total_is_estimate"] != false {
		t.Fatalf("total_is_estimate without q = %v, want false", raw["total_is_estimate"])
	}
}

// A PATCH naming observation_scopes alone rescopes the document's memories, is
// kept as the scoping a reprocess would use, and rejects an unknown mode while
// a body naming neither mutable field is a 422.
func TestParityDocumentPatchObservationScopes(t *testing.T) {
	a := parityApp(t)
	bankURL := "/v1/default/banks/scopes_bank"
	if rec := exchange(a, http.MethodPut, bankURL, `{"name":"Scopes Bank"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := `{"items":[{"content":"Alice prefers dark roast coffee.","document_id":"session-a","tags":["session:a"]}]}`
	if rec := exchange(a, http.MethodPost, bankURL+"/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("retain = %d (body %s)", rec.Code, rec.Body.String())
	}

	docURL := bankURL + "/documents/session-a"
	if rec := exchange(a, http.MethodPatch, docURL, `{"observation_scopes":"shared"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH scopes = %d (body %s)", rec.Code, rec.Body.String())
	}
	detail := decodeJSON(t, exchange(a, http.MethodGet, docURL, ""))
	if detail["observation_scopes"] != "shared" {
		t.Fatalf("document observation_scopes = %v, want shared", detail["observation_scopes"])
	}
	if tags, _ := detail["tags"].([]any); len(tags) != 1 || tags[0] != "session:a" {
		t.Fatalf("a scope-only PATCH changed the tags: %v", detail["tags"])
	}
	params, _ := detail["retain_params"].(map[string]any)
	if params["observation_scopes"] != "shared" {
		t.Fatalf("retain params observation_scopes = %v, want shared (a reprocess keeps it)", params)
	}

	// An explicit list is stored in the shape it was sent.
	if rec := exchange(a, http.MethodPatch, docURL, `{"observation_scopes":[["session:a"]]}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH explicit scopes = %d (body %s)", rec.Code, rec.Body.String())
	}
	detail = decodeJSON(t, exchange(a, http.MethodGet, docURL, ""))
	if !sameNestedList(detail["observation_scopes"], "session:a") {
		t.Fatalf("explicit scopes = %#v, want [[session:a]]", detail["observation_scopes"])
	}

	if rec := exchange(a, http.MethodPatch, docURL, `{"observation_scopes":"everything"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown mode = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	rec := exchange(a, http.MethodPatch, docURL, `{}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty body = %d, want 422", rec.Code)
	}
	var failure struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Detail != "At least one field (tags, observation_scopes) must be provided" {
		t.Fatalf("422 detail = %q", failure.Detail)
	}
	if rec := exchange(a, http.MethodPatch, bankURL+"/documents/absent", `{"observation_scopes":"shared"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing document = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func sameStringList(value any, want string) bool {
	list, ok := value.([]any)
	if !ok || len(list) != 1 {
		return false
	}
	return list[0] == want
}

func sameNestedList(value any, want string) bool {
	list, ok := value.([]any)
	if !ok || len(list) != 1 {
		return false
	}
	inner, ok := list[0].([]any)
	return ok && len(inner) == 1 && inner[0] == want
}

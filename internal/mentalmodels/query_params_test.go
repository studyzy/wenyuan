package mentalmodels

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A declared query parameter binds whenever its key appears, so a value that is
// present but empty is a VALUE the framework then validates — not an absence that
// falls back to the default. The reference answers `?detail=` with a 422
// naming the empty string, and so must this route: defaulting it instead would
// silently answer a request the reference refuses.
func TestListEmptyQueryValuesAreValidatedNotDefaulted(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"

	cases := []struct {
		name     string
		query    string
		wantType string
		wantLoc  []any
	}{
		{"empty detail", "detail=", "literal_error", []any{"query", "detail"}},
		{"empty tags_match", "tags_match=", "literal_error", []any{"query", "tags_match"}},
		{"empty limit", "limit=", "int_parsing", []any{"query", "limit"}},
		{"empty offset", "offset=", "int_parsing", []any{"query", "offset"}},
		// exclude_knowledge_pages is a plain bool to the framework: an empty
		// binding is handed to its boolean parser and refused, not read as false.
		{"empty exclude_knowledge_pages", "exclude_knowledge_pages=", "bool_parsing", []any{"query", "exclude_knowledge_pages"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := decodeValidationFailure(t, send(mux, http.MethodGet, base+"?"+tc.query, ""))
			if len(detail) != 1 {
				t.Fatalf("detail = %+v, want exactly one item", detail)
			}
			if detail[0].Type != tc.wantType {
				t.Fatalf("type = %q, want %q", detail[0].Type, tc.wantType)
			}
			if len(detail[0].Loc) != len(tc.wantLoc) || detail[0].Loc[0] != tc.wantLoc[0] || detail[0].Loc[1] != tc.wantLoc[1] {
				t.Fatalf("loc = %v, want %v", detail[0].Loc, tc.wantLoc)
			}
		})
	}
}

// The reference declares the list's offset with ge=0 and no upper bound, so an
// offset past the end is an empty page — not a 422. Only a NEGATIVE offset is a
// violation.
func TestListOffsetHasNoUpperBound(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"
	if rec := send(mux, http.MethodPost, base, `{"name":"one","source_query":"q"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	for _, offset := range []string{"1", "100", "999999"} {
		rec := send(mux, http.MethodGet, base+"?offset="+offset, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("offset=%s: status = %d, want 200 (%s)", offset, rec.Code, rec.Body.String())
		}
		var page ListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 || page.Total != 1 {
			t.Fatalf("offset=%s: page = %+v, want an empty window with total 1", offset, page)
		}
	}

	detail := decodeValidationFailure(t, send(mux, http.MethodGet, base+"?offset=-1", ""))
	if len(detail) != 1 || detail[0].Type != "greater_than_equal" {
		t.Fatalf("negative offset detail = %+v, want one greater_than_equal item", detail)
	}
}

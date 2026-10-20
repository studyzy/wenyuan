package reporting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The observation-scopes listing declares `limit` and `offset` as typed query
// parameters (api/http.py:9490-9535), so the reference's framework validates
// them before the route body runs and coerces each value from its raw text with
// its own string-to-int rules — not strconv.Atoi. A present-but-empty value is
// a request the framework refuses (`int_parsing`), never the parameter's
// default, and every refused parameter contributes an item in declaration
// order rather than only the first.
func TestObservationScopeQueryValidation(t *testing.T) {
	mux := newServer(t)
	path := "/v1/default/banks/" + bank + "/observations/scopes"

	accepted := []struct {
		query      string
		limit      int
		offset     int
		wantStatus int
	}{
		// Absent parameters take their declared defaults.
		{"", 100, 0, http.StatusOK},
		// A repeated scalar binds its last occurrence.
		{"?limit=5&limit=7", 7, 0, http.StatusOK},
		{"?limit=-1&limit=5", 5, 0, http.StatusOK},
		// The framework's conversion: surrounding whitespace is stripped, a
		// sign is allowed, single underscores may separate digits, and a
		// zero fractional part is still an integer.
		{"?limit=+5", 5, 0, http.StatusOK},
		{"?limit=007", 7, 0, http.StatusOK},
		{"?limit=1_000", 1000, 0, http.StatusOK},
		{"?limit=5.0", 5, 0, http.StatusOK},
		{"?limit=5.00", 5, 0, http.StatusOK},
		{"?limit=5_0.0", 50, 0, http.StatusOK},
		// str.strip() trims Unicode whitespace, U+00A0 and U+3000 included.
		{"?limit=5%C2%A0", 5, 0, http.StatusOK},
		{"?limit=%E3%80%805", 5, 0, http.StatusOK},
		{"?limit=%095", 5, 0, http.StatusOK},
	}
	for _, tc := range accepted {
		rec := send(mux, http.MethodGet, path+tc.query, "")
		if rec.Code != tc.wantStatus {
			t.Fatalf("GET %q = %d, want %d (body %s)", tc.query, rec.Code, tc.wantStatus, rec.Body.String())
		}
		var body ObservationScopesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Limit != tc.limit || body.Offset != tc.offset {
			t.Fatalf("GET %q = limit %d offset %d, want limit %d offset %d",
				tc.query, body.Limit, body.Offset, tc.limit, tc.offset)
		}
	}

	// A present-but-empty value is not an absent one: the reference parses the
	// empty string and answers its int_parsing item instead of the default.
	for _, param := range []string{"limit", "offset"} {
		query := "?" + param + "="
		assertSingleValidationItem(t, send(mux, http.MethodGet, path+query, ""),
			"int_parsing", []any{"query", param}, "")
	}

	// A numeric value past the int64 range is still a number: the framework
	// compares it with the declared bound and reports the bound, never a
	// parse failure. Python integers are unbounded.
	rec := send(mux, http.MethodGet, path+"?limit=12345678901234567890", "")
	assertSingleValidationItem(t, rec, "less_than_equal", []any{"query", "limit"}, "12345678901234567890")

	// The values the framework will not parse as an integer dump the raw text.
	for _, tc := range []struct {
		query string
		raw   string
	}{
		{"?limit=abc", "abc"},
		{"?limit=1.5", "1.5"},
		{"?limit=5.", "5."},
		{"?limit=5e2", "5e2"},
		{"?limit=1__0", "1__0"},
		{"?limit=0x10", "0x10"},
		{"?limit=true", "true"},
	} {
		rec := send(mux, http.MethodGet, path+tc.query, "")
		assertSingleValidationItem(t, rec, "int_parsing", []any{"query", "limit"}, tc.raw)
	}
}

// Every refused query parameter is reported, in declaration order — `limit`
// before `offset` — because the framework validates the whole request model
// before the route body runs. Reporting only the first would drop the second
// item the reference sends.
func TestObservationScopeQueryReportsEveryRejectedParameter(t *testing.T) {
	mux := newServer(t)
	path := "/v1/default/banks/" + bank + "/observations/scopes"

	rec := send(mux, http.MethodGet, path+"?limit=-1&offset=-1", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail []struct {
			Type string `json:"type"`
			Loc  []any  `json:"loc"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Detail) != 2 {
		t.Fatalf("items = %d, want 2 (body %s)", len(envelope.Detail), rec.Body.String())
	}
	for i, want := range []string{"limit", "offset"} {
		item := envelope.Detail[i]
		if item.Type != "greater_than_equal" {
			t.Fatalf("item %d type = %q, want greater_than_equal", i, item.Type)
		}
		if len(item.Loc) != 2 || item.Loc[0] != "query" || item.Loc[1] != want {
			t.Fatalf("item %d loc = %v, want [query %s]", i, item.Loc, want)
		}
	}

	// The same collection across two different kinds of rejection.
	rec = send(mux, http.MethodGet, path+"?limit=2000&offset=abc", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var mixed struct {
		Detail []struct {
			Type string `json:"type"`
			Loc  []any  `json:"loc"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mixed); err != nil {
		t.Fatal(err)
	}
	if len(mixed.Detail) != 2 ||
		mixed.Detail[0].Type != "less_than_equal" || mixed.Detail[0].Loc[1] != "limit" ||
		mixed.Detail[1].Type != "int_parsing" || mixed.Detail[1].Loc[1] != "offset" {
		t.Fatalf("items = %s, want less_than_equal(limit) then int_parsing(offset)", rec.Body.String())
	}
}

// assertSingleValidationItem checks a 422 envelope carries exactly one item of
// wantType at wantLoc with wantInput as its raw `input`.
func assertSingleValidationItem(t *testing.T, rec *httptest.ResponseRecorder, wantType string, wantLoc []any, wantInput string) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail []struct {
			Type  string `json:"type"`
			Loc   []any  `json:"loc"`
			Input string `json:"input"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Detail) != 1 {
		t.Fatalf("items = %d, want 1 (body %s)", len(envelope.Detail), rec.Body.String())
	}
	item := envelope.Detail[0]
	if item.Type != wantType {
		t.Fatalf("type = %q, want %q (body %s)", item.Type, wantType, rec.Body.String())
	}
	if len(item.Loc) != len(wantLoc) {
		t.Fatalf("loc = %v, want %v", item.Loc, wantLoc)
	}
	for i := range wantLoc {
		if item.Loc[i] != wantLoc[i] {
			t.Fatalf("loc = %v, want %v", item.Loc, wantLoc)
		}
	}
	if wantInput != "" && item.Input != wantInput {
		t.Fatalf("input = %q, want %q", item.Input, wantInput)
	}
}

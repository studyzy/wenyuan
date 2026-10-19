package directives

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newPathServer builds a route table that takes the bank and directive ids from
// the request path, so a test can address a bank that does not exist. newServer
// pins both values, which is right for the lifecycle tests but hides exactly the
// unknown-bank behaviour these tests are about.
func newPathServer(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(t.Context(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			rest := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
			bankID, tail, _ := strings.Cut(rest, "/")
			r.SetPathValue("bank_id", bankID)
			if strings.Contains(pattern, "{directive_id}") {
				r.SetPathValue("directive_id", strings.TrimPrefix(tail, "directives/"))
			}
			handler(w, r)
		})
	})
	return mux
}

// A directive id is bound to a `uuid` column, and the reference passes the raw
// path segment straight through, so a segment that is not a UUID fails while the
// driver binds the argument. That is a 500 whose detail is the driver's own
// message — leaking driver text is part of the contract here, so these tests pin
// the text rather than the status alone.
func TestDirectiveUUIDReportMatchesDriverMessage(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		// Length is checked before any character is looked at.
		{"not-a-uuid", `invalid input for query argument $2: 'not-a-uuid' (invalid UUID 'not-a-uuid': length must be between 32..36 characters, got 10)`},
		{"123", `invalid input for query argument $2: '123' (invalid UUID '123': length must be between 32..36 characters, got 3)`},
		{"-", `invalid input for query argument $2: '-' (invalid UUID '-': length must be between 32..36 characters, got 1)`},
		// In range, so the characters are checked: a non-hex digit is rejected
		// as unexpected, and the first offending byte is the one named.
		{"xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
			`invalid input for query argument $2: 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' (invalid UUID 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx': unexpected character 'x')`},
		{"0123456789abcdef0123456789abcdeZ",
			`invalid input for query argument $2: '0123456789abcdef0123456789abcdeZ' (invalid UUID '0123456789abcdef0123456789abcdeZ': unexpected character 'Z')`},
		{"12345678-1234-1234-1234-1234567890 2",
			`invalid input for query argument $2: '12345678-1234-1234-1234-1234567890 2' (invalid UUID '12345678-1234-1234-1234-1234567890 2': unexpected character ' ')`},
		// Hyphens are skipped wherever they appear, so only the hex-digit count
		// decides whether the value fills sixteen bytes.
		{"12345678-1234-1234-1234-12345678901",
			`invalid input for query argument $2: '12345678-1234-1234-1234-12345678901' (invalid UUID '12345678-1234-1234-1234-12345678901': decodes to less than 16 bytes)`},
		{"0123456789abcdef0123456789abcdeff",
			`invalid input for query argument $2: '0123456789abcdef0123456789abcdeff' (invalid UUID '0123456789abcdef0123456789abcdeff': decodes to more than 16 bytes)`},
		{"--------------------------------",
			`invalid input for query argument $2: '--------------------------------' (invalid UUID '--------------------------------': decodes to less than 16 bytes)`},
		// A long value is printed with a bounded width: the opening quote stays,
		// the closing quote is replaced by "...", and the whole value reappears
		// in the parenthesized reason.
		{"0123456789abcdef0123456789abcdef0-0-0-0-0",
			`invalid input for query argument $2: '0123456789abcdef0123456789abcdef0-0-0-0... (invalid UUID '0123456789abcdef0123456789abcdef0-0-0-0-0': length must be between 32..36 characters, got 41)`},
		{strings.Repeat("a", 39),
			`invalid input for query argument $2: '` + strings.Repeat("a", 39) + `... (invalid UUID '` + strings.Repeat("a", 39) + `': length must be between 32..36 characters, got 39)`},
		// 38 characters is still printed whole; the cut-off is above it.
		{strings.Repeat("a", 38),
			`invalid input for query argument $2: '` + strings.Repeat("a", 38) + `' (invalid UUID '` + strings.Repeat("a", 38) + `': length must be between 32..36 characters, got 38)`},
	}
	for _, tc := range cases {
		got, bad := directiveUUIDReport(tc.value, 2)
		if !bad {
			t.Fatalf("directiveUUIDReport(%q) accepted a non-UUID", tc.value)
		}
		if got != tc.want {
			t.Fatalf("directiveUUIDReport(%q)\n got: %s\nwant: %s", tc.value, got, tc.want)
		}
	}
}

// The driver accepts a UUID whose hyphens are not in the canonical positions,
// because it skips them rather than requiring them at fixed offsets — and then
// the route reports the absent row, not a driver failure.
func TestDirectiveUUIDReportAcceptsUnusualHyphenation(t *testing.T) {
	for _, value := range []string{
		"0123456789abcdef0123456789abcdef",
		"01234567-89abcdef0123456789abcdef",
		"01234567-89ab-cdef-0123-456789abcdef",
		"00000000-0000-0000-0000-000000000000",
	} {
		if report, bad := directiveUUIDReport(value, 2); bad {
			t.Fatalf("directiveUUIDReport(%q) refused a UUID: %s", value, report)
		}
	}
}

// PATCH binds the id after the members the body supplied, so the argument number
// follows the body: {} leaves the id at $2, and each of the five updatable
// members pushes it one further.
func TestUpdateDirectiveArgNumberFollowsSuppliedMembers(t *testing.T) {
	cases := []struct {
		supplied [5]bool
		want     int
	}{
		{[5]bool{}, 2},
		{[5]bool{true, false, false, false, false}, 3},
		{[5]bool{false, false, true, false, false}, 3},
		{[5]bool{false, false, false, false, true}, 3},
		{[5]bool{true, true, true, true, true}, 7},
		{[5]bool{false, true, false, false, true}, 4},
	}
	for _, tc := range cases {
		if got := updateDirectiveArgNumber(tc.supplied); got != tc.want {
			t.Fatalf("updateDirectiveArgNumber(%v) = %d, want %d", tc.supplied, got, tc.want)
		}
	}
}

// Served: the id-bearing routes answer a non-UUID id with the driver's message
// as a 500, and PATCH numbers the argument after the body's members.
func TestDirectiveRoutesReportDriverFailureForNonUUIDID(t *testing.T) {
	cases := []struct {
		method string
		body   string
		want   string
	}{
		{http.MethodGet, "", "invalid input for query argument $2: 'nope' (invalid UUID 'nope': length must be between 32..36 characters, got 4)"},
		{http.MethodDelete, "", "invalid input for query argument $2: 'nope' (invalid UUID 'nope': length must be between 32..36 characters, got 4)"},
		// An empty body is the framework's missing-body 422, which is raised
		// before the id is bound at all; {} binds nothing ahead of the id.
		{http.MethodPatch, "{}", "invalid input for query argument $2: 'nope' (invalid UUID 'nope': length must be between 32..36 characters, got 4)"},
		// priority is the third member, so it moves the id to $3.
		{http.MethodPatch, `{"priority":5}`, "invalid input for query argument $3: 'nope' (invalid UUID 'nope': length must be between 32..36 characters, got 4)"},
		// All five supplied moves it to $7.
		{http.MethodPatch, `{"name":"a","content":"b","priority":1,"is_active":true,"tags":["t"]}`,
			"invalid input for query argument $7: 'nope' (invalid UUID 'nope': length must be between 32..36 characters, got 4)"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.body, func(t *testing.T) {
			mux := newPathServer(t)
			rec := send(mux, tc.method, "/v1/default/banks/b1/directives/nope", tc.body)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s = %d, want 500: %s", tc.method, rec.Code, rec.Body.String())
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode detail: %v (%s)", err, rec.Body.String())
			}
			if envelope.Detail != tc.want {
				t.Fatalf("detail\n got: %s\nwant: %s", envelope.Detail, tc.want)
			}
		})
	}
}

// A UUID the driver accepts but no row carries is the route's own 404, in the
// reference's wording — not the driver failure.
func TestDirectiveRoutesReportNotFoundForAbsentUUID(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000000"
	mux := newPathServer(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := send(mux, method, "/v1/default/banks/b1/directives/"+id, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404: %s", method, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Directive '"+id+"' not found") {
			t.Fatalf("%s body = %s", method, rec.Body.String())
		}
	}
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/directives/"+id, "{}")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// The list query's selectors are validated before the bank is looked up: in the
// reference they are `Query(...)` parameters the framework resolves ahead of the
// handler body, so a bad selector beats the unknown-bank 404.
func TestListValidatesSelectorsBeforeTheBankLookup(t *testing.T) {
	mux := newPathServer(t)
	cases := map[string]string{
		"/v1/default/banks/absent/directives?limit=0":           `"type":"greater_than_equal"`,
		"/v1/default/banks/absent/directives?limit=1001":        `"type":"less_than_equal"`,
		"/v1/default/banks/absent/directives?offset=-1":         `"type":"greater_than_equal"`,
		"/v1/default/banks/absent/directives?tags_match=bogus":  `"type":"literal_error"`,
		"/v1/default/banks/absent/directives?active_only=bogus": `"type":"bool_parsing"`,
	}
	for path, want := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s body %q lacks %s", path, rec.Body.String(), want)
		}
	}
	// With every selector valid, the unknown bank is still the 404.
	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/absent/directives", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("plain list on unknown bank = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// Creating a directive in an unknown bank is the foreign-key rejection, reported
// as the reference's `str(e)` — the driver's sentence plus a DETAIL line, with
// neither this service's ERROR: prefix nor its SQLSTATE suffix.
func TestCreateInUnknownBankReportsForeignKeyRejection(t *testing.T) {
	mux := newPathServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/absent/directives", `{"name":"a","content":"b"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	want := `insert or update on table "directives" violates foreign key constraint "fk_directives_bank_id"` +
		"\n" + `DETAIL:  Key (bank_id)=(absent) is not present in table "banks".`
	var envelope struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode detail: %v (%s)", err, rec.Body.String())
	}
	if envelope.Detail != want {
		t.Fatalf("detail\n got: %q\nwant: %q", envelope.Detail, want)
	}
}

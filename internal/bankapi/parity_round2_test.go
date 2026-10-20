package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each test here pins one behavior the second audit round changed. The comment
// on each says what the reference answers and which earlier answer the test
// rejects, so reverting the fix fails the test rather than merely changing it.

// bodyResponse is the item list a rejected body answers with, read back as raw
// JSON so an item's input keeps the shape the reference echoes.
type bodyResponse struct {
	Detail []struct {
		Type  string          `json:"type"`
		Loc   []any           `json:"loc"`
		Msg   string          `json:"msg"`
		Input json.RawMessage `json:"input"`
		Ctx   map[string]any  `json:"ctx"`
	} `json:"detail"`
}

// readItems decodes a validation response and reports its items as
// "type loc1.loc2 input" strings, which is what these tests compare.
func readItems(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	var parsed bodyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	out := make([]string, 0, len(parsed.Detail))
	for _, item := range parsed.Detail {
		loc := make([]string, 0, len(item.Loc))
		for _, part := range item.Loc {
			loc = append(loc, stringifyPart(part))
		}
		out = append(out, item.Type+" "+strings.Join(loc, ".")+" "+string(item.Input))
	}
	return out
}

func stringifyPart(part any) string {
	if text, ok := part.(string); ok {
		return text
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		return "?"
	}
	return string(encoded)
}

// A number the reference's parser reads as a float infinity makes its handler
// raise: the model rejects the value, and encoding the rejected value fails
// again, so the framework answers its bare server error. The earlier answer
// here was the item list with a rendered 1e999 in it, which no client of the
// reference ever sees.
func TestNonFiniteValueAnswersTheServerError(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		for _, body := range []string{`{"name":1e999}`, `{"retain_chunk_size":1e999}`, `{"name":NaN}`, `{"name":-1e999}`} {
			mux, _ := newTestServer(t)
			createBank(t, mux, "audit-bl-nf")

			rec := doJSON(t, mux, method, "/v1/default/banks/audit-bl-nf", body)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s %s: status = %d, want 500", method, body, rec.Code)
			}
			if got := rec.Body.String(); got != "Internal Server Error" {
				t.Fatalf("%s %s: body = %q, want the framework's own text", method, body, got)
			}
			if contentType := rec.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
				t.Fatalf("%s %s: content type = %q", method, body, contentType)
			}
		}
	}
}

// A finite number is still the ordinary type item: the server error belongs to
// the non-finite values alone.
func TestFiniteValueKeepsTheTypeItem(t *testing.T) {
	mux, _ := newTestServer(t)
	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/x", `{"name":123}`)
	want := []string{`string_type body.name 123`}
	if got := readItems(t, rec); !sameStrings(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
}

// The reference validates the nested disposition model in one pass over its
// members: a member that failed still leaves the others' bounds checked, and
// the members the body never supplied are reported afterwards, once each. The
// earlier answers here were a duplicated missing item and a spurious
// greater_than_equal on the zero value of an unparsable member.
func TestNestedDispositionReportsEachMemberOnce(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "failed member does not zero its siblings",
			body: `{"disposition":{"skepticism":"x","literalism":9}}`,
			want: []string{
				`int_parsing body.disposition.skepticism "x"`,
				`less_than_equal body.disposition.literalism 9`,
				`missing body.disposition.empathy {"skepticism":"x","literalism":9}`,
			},
		},
		{
			name: "missing members echo the object as written",
			body: `{"disposition":{"skepticism":0}}`,
			want: []string{
				`greater_than_equal body.disposition.skepticism 0`,
				`missing body.disposition.literalism {"skepticism":0}`,
				`missing body.disposition.empathy {"skepticism":0}`,
			},
		},
		{
			name: "an empty object reports every member",
			body: `{"disposition":{}}`,
			want: []string{
				`missing body.disposition.skepticism {}`,
				`missing body.disposition.literalism {}`,
				`missing body.disposition.empathy {}`,
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mux, _ := newTestServer(t)
			rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/x", testCase.body)
			got := readItems(t, rec)
			want := make([]string, len(testCase.want))
			for i, item := range testCase.want {
				// The echoed object is a map, so its members come back sorted;
				// the comparison normalises the expected text the same way.
				want[i] = normaliseInput(t, item)
			}
			if !sameItems(t, got, want) {
				t.Fatalf("items = %v, want %v", got, want)
			}
		})
	}
}

// normaliseInput rewrites the input portion of an expected item so a hand
// written object matches the member order the encoder produces.
func normaliseInput(t *testing.T, item string) string {
	t.Helper()
	parts := strings.SplitN(item, " ", 3)
	if len(parts) != 3 {
		return item
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(parts[2]), &value); err != nil {
		return item
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return item
	}
	return parts[0] + " " + parts[1] + " " + string(encoded)
}

// sameStrings compares two item lists member by member, with each item's input
// normalised first: an echoed object is a map on both sides, and a map has no
// member order to compare.
func sameStrings(left, right []string) bool { return sameItems(nil, left, right) }

func sameItems(t *testing.T, left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if normaliseItem(t, left[i]) != normaliseItem(t, right[i]) {
			return false
		}
	}
	return true
}

// normaliseItem rewrites one item's input into the member order the encoder
// produces, so a hand written object compares equal to an echoed one.
func normaliseItem(t *testing.T, item string) string {
	if t == nil {
		return item
	}
	return normaliseInput(t, item)
}

// The alias body is validated whole even when part of it did not decode: the
// reference's model reports the member it never received before the value it
// could not read, and reports a member the body did carry once, as a type item.
// The earlier answer here dropped the missing member entirely.
func TestAliasBodyReportsEveryMember(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "missing alias before an unreadable primary",
			body: `{"primary":"bad"}`,
			want: []string{
				`missing body.alias {"primary":"bad"}`,
				`bool_parsing body.primary "bad"`,
			},
		},
		{
			name: "unreadable primary alone",
			body: `{"alias":"a","primary":"bad"}`,
			want: []string{`bool_parsing body.primary "bad"`},
		},
		{
			name: "null primary is a type item",
			body: `{"alias":"a","primary":null}`,
			want: []string{`bool_type body.primary null`},
		},
		{
			name: "null alias is a type item, not a missing member",
			body: `{"alias":null}`,
			want: []string{`string_type body.alias null`},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mux, _ := newTestServer(t)
			createBank(t, mux, "audit-bl-al")
			rec := post(t, mux, "/v1/default/banks/audit-bl-al/aliases", testCase.body)
			want := make([]string, len(testCase.want))
			for i, item := range testCase.want {
				want[i] = normaliseInput(t, item)
			}
			if got := readItems(t, rec); !sameStrings(got, want) {
				t.Fatalf("items = %v, want %v", got, want)
			}
		})
	}
}

// An alias body that is not an object has no members to report, and a body with
// no bytes at all is the framework's item for the parameter itself.
func TestAliasNonObjectBodyReportsTheParameter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"no body", "", []string{`missing body null`}},
		{"null body", "null", []string{`missing body null`}},
		{"array body", "[1]", []string{`model_attributes_type body [1]`}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mux, _ := newTestServer(t)
			createBank(t, mux, "audit-bl-ob")
			rec := post(t, mux, "/v1/default/banks/audit-bl-ob/aliases", testCase.body)
			if got := readItems(t, rec); !sameStrings(got, testCase.want) {
				t.Fatalf("items = %v, want %v", got, testCase.want)
			}
		})
	}
}

// The reference's paging parameters are unbounded Python ints: a limit wider
// than any page keeps every match and is echoed back as written, and an offset
// wider than any page starts past the end. The earlier answers here fell back
// to the default page size and reported 100 back to a caller who asked for
// more.
func TestPageParametersWiderThanAMachineInt(t *testing.T) {
	const huge = "999999999999999999999999"
	cases := []struct {
		name       string
		query      string
		wantBanks  int
		wantLimit  string
		wantOffset string
	}{
		{"huge limit keeps every match", "?limit=" + huge, 3, huge, "0"},
		{"huge offset starts past the end", "?offset=" + huge, 0, "100", huge},
		{"huge offset leaves a named limit alone", "?limit=5&offset=" + huge, 0, "5", huge},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mux, _ := newTestServer(t)
			for _, id := range []string{"audit-bl-p1", "audit-bl-p2", "audit-bl-p3"} {
				createBank(t, mux, id)
			}
			rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks"+testCase.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			var page struct {
				Banks  []json.RawMessage `json:"banks"`
				Total  int               `json:"total"`
				Limit  json.RawMessage   `json:"limit"`
				Offset json.RawMessage   `json:"offset"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			if len(page.Banks) != testCase.wantBanks {
				t.Fatalf("banks = %d, want %d", len(page.Banks), testCase.wantBanks)
			}
			if page.Total != 3 {
				t.Fatalf("total = %d, want the whole match count 3", page.Total)
			}
			if got := string(page.Limit); got != testCase.wantLimit {
				t.Fatalf("limit = %s, want %s", got, testCase.wantLimit)
			}
			if got := string(page.Offset); got != testCase.wantOffset {
				t.Fatalf("offset = %s, want %s", got, testCase.wantOffset)
			}
		})
	}
}

// createBank puts a bank in place through the served route, so a test starts
// from the same state a client would.
func createBank(t *testing.T, mux *http.ServeMux, bankID string) {
	t.Helper()
	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/"+bankID, `{"name":"`+bankID+`"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status = %d; body = %s", bankID, rec.Code, rec.Body.String())
	}
}

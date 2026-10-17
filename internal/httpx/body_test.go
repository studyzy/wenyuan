package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A rejected body answers the framework's structured item with the status and
// the vocabulary the reference uses, not a 400 with a sentence.
func TestDecodeBodyRejections(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	cases := []struct {
		name       string
		body       string
		wantType   string
		wantLoc    string
		wantStatus int
	}{
		{"not json", `{"name": `, "json_invalid", `"loc":["body",9]`, 422},
		{"wrong scalar type", `{"name":"a","count":"many"}`, "int_parsing", `"loc":["body","count"]`, 422},
		{"wrong string type", `{"name":7,"count":1}`, "string_type", `"loc":["body","name"]`, 422},
		{"empty body", ``, "missing", `"loc":["body"]`, 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
			var target payload
			err := DecodeBody(req, &target)
			if err == nil {
				t.Fatal("the body must be rejected")
			}
			rec := httptest.NewRecorder()
			WriteErrorFor(rec, req, err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"type":"`+tc.wantType+`"`) {
				t.Fatalf("body %s does not carry the type %q", body, tc.wantType)
			}
			if !strings.Contains(body, tc.wantLoc) {
				t.Fatalf("body %s does not carry %s", body, tc.wantLoc)
			}
			if !strings.HasPrefix(body, `{"detail":[`) {
				t.Fatalf("body %s is not the detail envelope", body)
			}
		})
	}
	// A well-formed body decodes.
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"a","count":2}`))
	var ok payload
	if err := DecodeBody(req, &ok); err != nil {
		t.Fatalf("a valid body must decode: %v", err)
	}
	if ok.Name != "a" || ok.Count != 2 {
		t.Fatalf("decoded %+v", ok)
	}
}

// DecodeStructFields reports every field of one object the reference's model
// rejects, in the struct's field-declaration order, and keeps the lax coercion
// a single field would get from the body decoder.
func TestDecodeStructFieldsReportsEveryBadField(t *testing.T) {
	type twoInts struct {
		MaxTokens               int `json:"max_tokens"`
		MaxTokensPerObservation int `json:"max_tokens_per_observation"`
	}
	type intAndBool struct {
		MaxTokens int  `json:"max_tokens"`
		Output    bool `json:"output"`
	}
	render := func(errs []error) []ValidationError { return NestedFailureItems(errs) }

	// Two bad integers in one member: both items, in declaration order, even
	// though the JSON keys are reversed.
	var ints twoInts
	items := render(DecodeStructFields([]byte(`{"max_tokens_per_observation":"b","max_tokens":"a"}`), &ints, []string{"include", "source_facts"}))
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(items), items)
	}
	for i, want := range []struct {
		kind string
		loc  string
	}{
		{"int_parsing", "body.include.source_facts.max_tokens"},
		{"int_parsing", "body.include.source_facts.max_tokens_per_observation"},
	} {
		if items[i].Type != want.kind || strings.Join(items[i].Loc, ".") != want.loc {
			t.Fatalf("item %d = %+v, want %s at %s", i, items[i], want.kind, want.loc)
		}
	}

	// A bad integer and a bad boolean in one member: one item each, in field
	// order. No reference include member carries both today, so this pins the
	// generic mechanism the per-field decode relies on.
	var mixed intAndBool
	items = render(DecodeStructFields([]byte(`{"output":"maybe","max_tokens":"a"}`), &mixed, []string{"include", "member"}))
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(items), items)
	}
	if items[0].Type != "int_parsing" || strings.Join(items[0].Loc, ".") != "body.include.member.max_tokens" {
		t.Fatalf("item 0 = %+v", items[0])
	}
	if items[1].Type != "bool_parsing" || strings.Join(items[1].Loc, ".") != "body.include.member.output" {
		t.Fatalf("item 1 = %+v", items[1])
	}

	// A numeric string is the lax coercion the reference accepts, so the field
	// is stored rather than reported.
	var lax twoInts
	if errs := DecodeStructFields([]byte(`{"max_tokens":"5","max_tokens_per_observation":"7"}`), &lax, []string{"include", "source_facts"}); len(errs) != 0 {
		t.Fatalf("lax errors = %+v", render(errs))
	}
	if lax.MaxTokens != 5 || lax.MaxTokensPerObservation != 7 {
		t.Fatalf("decoded %+v", lax)
	}
}

// A list position in loc is a number in the framework's shape, not a string.
func TestValidationLocPositionsAreNumbers(t *testing.T) {
	failure := &ValidationFailure{Errors: []ValidationError{{
		Type: "string_type", Loc: []string{"body", "items", "0", "text"}, Msg: "Input should be a valid string",
	}}}
	body, err := failure.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `["body","items",0,"text"]`) {
		t.Fatalf("loc = %s", body)
	}
}

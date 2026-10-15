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

package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// retiredWriteRequest fires one body at a retired write route through the
// served mux and returns the status and decoded detail.
func retiredWriteRequest(t *testing.T, method, pattern, body string) (int, string) {
	t.Helper()
	if path, ok := strings.CutPrefix(pattern, method+" "); ok {
		pattern = path
	}
	mux := http.NewServeMux()
	(&RetiredHandler{}).BindRetiredRoutes(func(m string, p string, h http.HandlerFunc) {
		mux.HandleFunc(m+" "+p, func(w http.ResponseWriter, r *http.Request) {
			// The production router sets the matched pattern on the request; a
			// plain ServeMux does not, and the body gate reads it to resolve the
			// route's declared schema.
			r.Pattern = m + " " + p
			h(w, r)
		})
	})
	req := httptest.NewRequest(method, replaceBankID(pattern), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body = %q, not JSON", rec.Body.String())
	}
	return rec.Code, string(envelope.Detail)
}

// The retired profile write declares UpdateDispositionRequest, and the framework
// validates a declared body BEFORE the handler runs. A body the model rejects is
// therefore answered 422 even though the route is retired; only an acceptable
// body reaches the 410. Reverting the body gate (answering 410 unconditionally)
// makes every 422 case below report 410.
func TestRetiredProfileWriteValidatesItsBodyBeforeTheGoneStatus(t *testing.T) {
	const pattern = "PUT /v1/default/banks/{bank_id}/profile"
	const gone = 410
	cases := []struct {
		name string
		body string
		// want is the exact detail body the reference answers with.
		want string
	}{
		{"no body", ``, `[{"type":"missing","loc":["body"],"msg":"Field required","input":null}]`},
		{"empty object", `{}`, `[{"type":"missing","loc":["body","disposition"],"msg":"Field required","input":{}}]`},
		{"disposition null", `{"disposition":null}`,
			`[{"type":"model_attributes_type","loc":["body","disposition"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null}]`},
		{"disposition is a string", `{"disposition":"x"}`,
			`[{"type":"model_attributes_type","loc":["body","disposition"],"msg":"Input should be a valid dictionary or object to extract fields from","input":"x"}]`},
		{"disposition is a list", `{"disposition":[]}`,
			`[{"type":"model_attributes_type","loc":["body","disposition"],"msg":"Input should be a valid dictionary or object to extract fields from","input":[]}]`},
		{"all traits missing", `{"disposition":{}}`,
			`[{"type":"missing","loc":["body","disposition","skepticism"],"msg":"Field required","input":{}},` +
				`{"type":"missing","loc":["body","disposition","literalism"],"msg":"Field required","input":{}},` +
				`{"type":"missing","loc":["body","disposition","empathy"],"msg":"Field required","input":{}}]`},
		{"trait not an integer", `{"disposition":{"skepticism":"x"}}`,
			`[{"type":"int_parsing","loc":["body","disposition","skepticism"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"x"},` +
				`{"type":"missing","loc":["body","disposition","literalism"],"msg":"Field required","input":{"skepticism":"x"}},` +
				`{"type":"missing","loc":["body","disposition","empathy"],"msg":"Field required","input":{"skepticism":"x"}}]`},
		{"trait with a fractional part", `{"disposition":{"skepticism":3.5,"literalism":3,"empathy":3}}`,
			`[{"type":"int_from_float","loc":["body","disposition","skepticism"],"msg":"Input should be a valid integer, got a number with a fractional part","input":3.5}]`},
		{"trait null", `{"disposition":{"skepticism":null,"literalism":3,"empathy":3}}`,
			`[{"type":"int_type","loc":["body","disposition","skepticism"],"msg":"Input should be a valid integer","input":null}]`},
		{"below the scale", `{"disposition":{"skepticism":0,"literalism":3,"empathy":3}}`,
			`[{"type":"greater_than_equal","loc":["body","disposition","skepticism"],"msg":"Input should be greater than or equal to 1","input":0,"ctx":{"ge":1}}]`},
		{"above the scale", `{"disposition":{"skepticism":6,"literalism":3,"empathy":3}}`,
			`[{"type":"less_than_equal","loc":["body","disposition","skepticism"],"msg":"Input should be less than or equal to 5","input":6,"ctx":{"le":5}}]`},
		{"a body that is not an object", `[]`,
			`[{"type":"model_attributes_type","loc":["body"],"msg":"Input should be a valid dictionary or object to extract fields from","input":[]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := retiredWriteRequest(t, http.MethodPut, pattern, tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (the reference validates the body before the handler raises 410)", status)
			}
			if detail != tc.want {
				t.Fatalf("detail =\n  %s\nwant\n  %s", detail, tc.want)
			}
		})
	}

	// An acceptable body (including the lax coercions the reference's parser
	// applies) reaches the retirement status.
	for _, acceptable := range []string{
		`{"disposition":{"skepticism":3,"literalism":3,"empathy":3}}`,
		`{"disposition":{"skepticism":3,"literalism":3,"empathy":3},"mission":"ignored"}`,
		`{"disposition":{"skepticism":"3","literalism":3,"empathy":3}}`,
		`{"disposition":{"skepticism":true,"literalism":3,"empathy":3}}`,
		`{"disposition":{"skepticism":3.0,"literalism":3,"empathy":3}}`,
	} {
		status, detail := retiredWriteRequest(t, http.MethodPut, pattern, acceptable)
		if status != gone {
			t.Fatalf("acceptable body %s: status = %d, want 410", acceptable, status)
		}
		var quoted string
		if err := json.Unmarshal([]byte(detail), &quoted); err != nil || quoted != httpx.DetailRetiredProfile {
			t.Fatalf("acceptable body %s: detail = %v", acceptable, detail)
		}
	}
}

// The retired background write declares AddBackgroundRequest, so its body is
// validated before the 410 in the same way. Reverting the gate makes every 422
// case report 410.
func TestRetiredBackgroundWriteValidatesItsBodyBeforeTheGoneStatus(t *testing.T) {
	const pattern = "POST /v1/default/banks/{bank_id}/background"
	const gone = 410
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no body", ``, `[{"type":"missing","loc":["body"],"msg":"Field required","input":null}]`},
		{"empty object", `{}`, `[{"type":"missing","loc":["body","content"],"msg":"Field required","input":{}}]`},
		{"content not a string", `{"content":42}`,
			`[{"type":"string_type","loc":["body","content"],"msg":"Input should be a valid string","input":42}]`},
		{"content null", `{"content":null}`,
			`[{"type":"string_type","loc":["body","content"],"msg":"Input should be a valid string","input":null}]`},
		{"content is a list", `{"content":["a"]}`,
			`[{"type":"string_type","loc":["body","content"],"msg":"Input should be a valid string","input":["a"]}]`},
		{"update_disposition unparseable", `{"content":"x","update_disposition":"z"}`,
			`[{"type":"bool_parsing","loc":["body","update_disposition"],"msg":"Input should be a valid boolean, unable to interpret input","input":"z"}]`},
		{"update_disposition null", `{"content":"x","update_disposition":null}`,
			`[{"type":"bool_type","loc":["body","update_disposition"],"msg":"Input should be a valid boolean","input":null}]`},
		{"a body that is not an object", `[]`,
			`[{"type":"model_attributes_type","loc":["body"],"msg":"Input should be a valid dictionary or object to extract fields from","input":[]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := retiredWriteRequest(t, http.MethodPost, pattern, tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", status)
			}
			if detail != tc.want {
				t.Fatalf("detail =\n  %s\nwant\n  %s", detail, tc.want)
			}
		})
	}

	for _, acceptable := range []string{
		`{"content":"x"}`,
		`{"content":""}`,
		`{"content":"x","update_disposition":true}`,
		`{"content":"x","update_disposition":1}`,
		`{"content":"x","update_disposition":"yes"}`,
		`{"content":"x","unknown":1}`,
	} {
		status, detail := retiredWriteRequest(t, http.MethodPost, pattern, acceptable)
		if status != gone {
			t.Fatalf("acceptable body %s: status = %d, want 410", acceptable, status)
		}
		var quoted string
		if err := json.Unmarshal([]byte(detail), &quoted); err != nil || quoted != httpx.DetailRetiredBackground {
			t.Fatalf("acceptable body %s: detail = %v", acceptable, detail)
		}
	}
}

// The retired profile READ declares no body, so it answers the retirement status
// whatever the request carries. This is the case a blanket "validate the body"
// change would break.
func TestRetiredProfileReadAnswersGoneWithoutABody(t *testing.T) {
	status, detail := retiredWriteRequest(t, http.MethodGet, "GET /v1/default/banks/{bank_id}/profile", ``)
	if status != http.StatusGone {
		t.Fatalf("status = %d, want 410", status)
	}
	var quoted string
	if err := json.Unmarshal([]byte(detail), &quoted); err != nil || quoted != httpx.DetailRetiredProfile {
		t.Fatalf("detail = %v", detail)
	}
}

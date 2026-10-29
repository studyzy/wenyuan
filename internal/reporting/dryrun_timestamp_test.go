package reporting

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The dry-run body's timestamp is a plain datetime in the reference, so the
// framework rejects a value that is not one. These cases are the live
// reference's own answers, captured request by request.
func TestDryRunTimestampMatchesTheReferenceAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a word is too short to be a datetime",
			body: `{"content":"x","timestamp":"nope"}`,
			want: `{"detail":[{"type":"datetime_from_date_parsing","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime or date, input is too short",` +
				`"input":"nope","ctx":{"error":"input is too short"}}]}`,
		},
		{
			name: "an empty string is not a datetime",
			body: `{"content":"x","timestamp":""}`,
			want: `{"detail":[{"type":"datetime_from_date_parsing","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime or date, input is too short",` +
				`"input":"","ctx":{"error":"input is too short"}}]}`,
		},
		{
			name: "a month outside 1-12 says so",
			body: `{"content":"x","timestamp":"2024-13-45"}`,
			want: `{"detail":[{"type":"datetime_from_date_parsing","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime or date, month value is outside expected range of 1-12",` +
				`"input":"2024-13-45","ctx":{"error":"month value is outside expected range of 1-12"}}]}`,
		},
		{
			name: "a day the month does not have says so",
			body: `{"content":"x","timestamp":"2024-02-30"}`,
			want: `{"detail":[{"type":"datetime_from_date_parsing","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime or date, day value is outside expected range",` +
				`"input":"2024-02-30","ctx":{"error":"day value is outside expected range"}}]}`,
		},
		{
			name: "a minute outside 0-59 is trailing text",
			body: `{"content":"x","timestamp":"2024-01-15T10:61:00"}`,
			want: `{"detail":[{"type":"datetime_from_date_parsing","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime or date, unexpected extra characters at the end of the input",` +
				`"input":"2024-01-15T10:61:00","ctx":{"error":"unexpected extra characters at the end of the input"}}]}`,
		},
		{
			name: "a date that cannot become a datetime fails one step later",
			body: `{"content":"x","timestamp":"0000-01-15"}`,
			want: `{"detail":[{"type":"datetime_parsing","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime, year 0 is out of range",` +
				`"input":"0000-01-15","ctx":{"error":"year 0 is out of range"}}]}`,
		},
		{
			name: "a boolean is not a datetime at all",
			body: `{"content":"x","timestamp":true}`,
			want: `{"detail":[{"type":"datetime_type","loc":["body","timestamp"],` +
				`"msg":"Input should be a valid datetime","input":true}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mux := dryRunServer(t, nil, bank)
			rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract", testCase.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != testCase.want {
				t.Fatalf("got  %s\nwant %s", got, testCase.want)
			}
		})
	}
}

// A value the reference accepts as a datetime still reaches the extractor's
// reference time, and a number of seconds is accepted and simply names none: the
// framework reads it as a duration, and the dry run has no instant to take from
// it.
func TestDryRunTimestampAcceptedValuesRunTheExtraction(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantTime bool
	}{
		{name: "an ISO timestamp sets the reference time", body: `{"content":"x","timestamp":"2024-01-15T10:30:00Z"}`, wantTime: true},
		{name: "a bare date is a datetime but not an instant the dry run keeps", body: `{"content":"x","timestamp":"2024-01-15"}`},
		{name: "a number of seconds is accepted", body: `{"content":"x","timestamp":12}`},
		{name: "null is no timestamp", body: `{"content":"x","timestamp":null}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			extractor := &fakeDryRunExtractor{}
			mux := dryRunServer(t, extractor, bank)
			rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract", testCase.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if extractor.calls != 1 {
				t.Fatalf("extraction calls = %d, want 1", extractor.calls)
			}
			if gotTime := !extractor.options.ReferenceTime.IsZero(); gotTime != testCase.wantTime {
				t.Fatalf("reference time set = %v, want %v", gotTime, testCase.wantTime)
			}
		})
	}
}

// A member the reference's model refuses reports the value the caller sent as
// the item's input, not null: the shared decoder keeps it, and the route serves
// its rendering rather than one rebuilt from the error alone.
func TestDryRunDecodeFailureKeepsTheRefusedValue(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{
			name: "a string member sent a number",
			body: `{"content":"x","context":5}`,
			want: `{"detail":[{"type":"string_type","loc":["body","context"],` +
				`"msg":"Input should be a valid string","input":5}]}`,
		},
		{
			name: "an integer member sent a word",
			body: `{"content":"x","retain_chunk_size":"big"}`,
			want: `{"detail":[{"type":"int_parsing","loc":["body","retain_chunk_size"],` +
				`"msg":"Input should be a valid integer, unable to parse string as an integer","input":"big"}]}`,
		},
		{
			name: "a list member sent a word",
			body: `{"content":"x","entity_labels":"bad"}`,
			want: `{"detail":[{"type":"list_type","loc":["body","entity_labels"],` +
				`"msg":"Input should be a valid list","input":"bad"}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mux := dryRunServer(t, nil, bank)
			rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract", testCase.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != testCase.want {
				t.Fatalf("got  %s\nwant %s", got, testCase.want)
			}
		})
	}
}

// A missing request-level member reports the whole body as its input, the object
// the member was missing from.
func TestDryRunMissingContentReportsTheBody(t *testing.T) {
	mux := dryRunServer(t, nil, bank)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract",
		`{"items":[{"content":"x"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":[{"type":"missing","loc":["body","content"],"msg":"Field required",` +
		`"input":{"items":[{"content":"x"}]}}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	var body any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
}

// The preview model refuses a member of the wrong type before the route runs, so
// a bad `strategy` is a 422 even where the path names a bank that does not exist.
func TestPromptPreviewRejectsStrategyTypeBeforeBankLookup(t *testing.T) {
	mux := dryRunServer(t, nil, "no-such-bank")
	rec := send(mux, http.MethodPost, "/v1/default/banks/no-such-bank/prompts/preview",
		`{"operation":"retain","strategy":5}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":[{"type":"string_type","loc":["body","strategy"],` +
		`"msg":"Input should be a valid string","input":5}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// A datetime the framework accepted still reaches the engine's
// parse_datetime_flexible, which refuses the year-1/9999 placeholders; the
// route's `except ValueError` answers that refusal with a plain 400
// (orchestrator.py:468-475).
func TestDryRunTimestampRejectsPlaceholderDates(t *testing.T) {
	mux := dryRunServer(t, nil, bank)
	for _, value := range []string{"9999-12-31T23:59:59Z", "0001-01-01T00:00:00"} {
		rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract",
			`{"content":"x","timestamp":"`+value+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("timestamp %s = %d, want 400: %s", value, rec.Code, rec.Body.String())
		}
		year := "9999"
		if strings.HasPrefix(value, "0001") {
			year = "1"
		}
		want := `{"detail":"Date year ` + year + ` is a placeholder, not a date."}`
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Fatalf("timestamp %s: body = %s, want %s", value, got, want)
		}
	}
}

package memoryops

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The temporal window is decoded by the window model, not on the bad-request
// path: the reference declares two required `datetime` fields, so a missing,
// null or unparseable bound is a field-level item at its own path and both
// bounds of one window are reported together.
func TestRecallTemporalWindowBoundItems(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		status   int
		rawItems []string
	}{
		{
			name:     "empty object reports both bounds missing",
			body:     `{"query":"q","temporal_window":{}}`,
			status:   http.StatusUnprocessableEntity,
			rawItems: []string{`{"input":{},"loc":["body","temporal_window","start"],"msg":"Field required","type":"missing"}`, `{"input":{},"loc":["body","temporal_window","end"],"msg":"Field required","type":"missing"}`},
		},
		{
			name:     "one missing bound is reported alone",
			body:     `{"query":"q","temporal_window":{"start":"2023-04-01T00:00:00Z"}}`,
			status:   http.StatusUnprocessableEntity,
			rawItems: []string{`{"input":{"start":"2023-04-01T00:00:00Z"},"loc":["body","temporal_window","end"],"msg":"Field required","type":"missing"}`},
		},
		{
			name:     "a too-short bound carries the parser's own message",
			body:     `{"query":"q","temporal_window":{"start":"bad","end":"2023-06-30T23:59:59Z"}}`,
			status:   http.StatusUnprocessableEntity,
			rawItems: []string{`{"ctx":{"error":"input is too short"},"input":"bad","loc":["body","temporal_window","start"],"msg":"Input should be a valid datetime or date, input is too short","type":"datetime_from_date_parsing"}`},
		},
		{
			name:     "a null bound is a type failure",
			body:     `{"query":"q","temporal_window":{"start":null,"end":"2023-06-30T23:59:59Z"}}`,
			status:   http.StatusUnprocessableEntity,
			rawItems: []string{`{"input":null,"loc":["body","temporal_window","start"],"msg":"Input should be a valid datetime","type":"datetime_type"}`},
		},
		{
			name:     "an inverted window names both bounds in the message",
			body:     `{"query":"q","temporal_window":{"start":"2023-06-30T00:00:00Z","end":"2023-04-01T00:00:00Z"}}`,
			status:   http.StatusUnprocessableEntity,
			rawItems: []string{`{"ctx":{"error":{}},"input":{"end":"2023-04-01T00:00:00Z","start":"2023-06-30T00:00:00Z"},"loc":["body","temporal_window"],"msg":"Value error, temporal_window.end must not be earlier than temporal_window.start","type":"value_error"}`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newServer(t)
			rec := post(t, mux, "/v1/default/banks/b1/memories/recall", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.status, rec.Body.String())
			}
			assertDetailItems(t, rec.Body.Bytes(), tc.rawItems)
		})
	}
}

// A bound the reference accepts for a `datetime` field parses: a date, a naive
// datetime read as UTC, an offset datetime, and a number read as a Unix
// timestamp. All four windows reach the store as the same instant range.
func TestTemporalWindowAcceptanceForms(t *testing.T) {
	cases := []struct {
		body      string
		wantStart time.Time
		wantEnd   time.Time
		leftovers bool
	}{
		{
			body:      `{"start":"2023-04-01","end":"2023-06-30"}`,
			wantStart: time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC),
		},
		{
			body:      `{"start":"2023-04-01T00:00:00","end":"2023-06-30T23:59:59"}`,
			wantStart: time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2023, 6, 30, 23, 59, 59, 0, time.UTC),
		},
		{
			body:      `{"start":"2023-04-01T02:00:00+02:00","end":"2023-06-30T23:59:59.5Z"}`,
			wantStart: time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2023, 6, 30, 23, 59, 59, 500000000, time.UTC),
		},
		{
			body:      `{"start":1680307200,"end":1688169599}`,
			wantStart: time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2023, 6, 30, 23, 59, 59, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		window := windowFromJSON(t, tc.body)
		parsed, err := parseTemporalWindow(window)
		if err != nil || parsed == nil {
			t.Fatalf("%s: parse = %v, %v", tc.body, parsed, err)
		}
		if !parsed.Start.Equal(tc.wantStart) || !parsed.End.Equal(tc.wantEnd) {
			t.Fatalf("%s: window = %s..%s, want %s..%s", tc.body,
				parsed.Start.Format(time.RFC3339Nano), parsed.End.Format(time.RFC3339Nano),
				tc.wantStart.Format(time.RFC3339Nano), tc.wantEnd.Format(time.RFC3339Nano))
		}
	}
}

// Only a non-positive token budget drops the entity channel: a budget that keeps
// results leaves the channels alone.
func TestRecallEntityChannelOnlyDroppedWithNoFacts(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha beta gamma", Type: "world"})
	for _, tc := range []struct {
		maxTokens int
		wantFacts bool
	}{{0, false}, {-1, false}, {200, true}} {
		body, _ := json.Marshal(map[string]any{"query": "alpha", "max_tokens": tc.maxTokens})
		rec := post(t, mux, "/v1/default/banks/b1/memories/recall", string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("max_tokens=%d status = %d, body = %s", tc.maxTokens, rec.Code, rec.Body.String())
		}
		var response RecallResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if got := len(response.Results) > 0; got != tc.wantFacts {
			t.Fatalf("max_tokens=%d facts = %d, want present=%v: %s",
				tc.maxTokens, len(response.Results), tc.wantFacts, rec.Body.String())
		}
	}
}

// The date grammar's own messages are what a caller reads when its temporal
// input is wrong, so they are pinned against the reference's parser: each case
// is the message CPython 3.11 raises through the reference's framework.
func TestPythonDateTimeParserMessages(t *testing.T) {
	for _, tc := range []struct{ value, message string }{
		{"nonsense", "input is too short"},
		{"bad", "input is too short"},
		{"", "input is too short"},
		{"2023-5-3", "input is too short"},
		{"2023-05-30T25:40:00", "unexpected extra characters at the end of the input"},
		{"2023-05-30T254000", "unexpected extra characters at the end of the input"},
		{"2023-05-30T23:60:00", "unexpected extra characters at the end of the input"},
		{"2023-05-30T23:40:61", "unexpected extra characters at the end of the input"},
		{"2023-05-30T23", "unexpected extra characters at the end of the input"},
		{"2023-05-30T23:40:00+2:00", "unexpected extra characters at the end of the input"},
		{"2023-13-45", "month value is outside expected range of 1-12"},
		{"2023-02-30", "day value is outside expected range"},
		{"2023-W22-2", "invalid character in month"},
		{"2023/05/30", "invalid date separator, expected `-`"},
		{"20230530T2340", "invalid date separator, expected `-`"},
	} {
		if _, err := parsePythonDateTime(tc.value); err == nil || err.Error() != tc.message {
			t.Errorf("%q = %v, want %q", tc.value, err, tc.message)
		}
	}
	for _, tc := range []struct{ value, want string }{
		{"2023-06-30T23:59:59Z", "2023-06-30T23:59:59Z"},
		{"2023-04-01", "2023-04-01T00:00:00Z"},
		{"1700000000", "2023-11-14T22:13:20Z"},
		{"2023-04-01T02:00:00+02:00", "2023-04-01T00:00:00Z"},
		{"2023-06-30T23:59:59.5Z", "2023-06-30T23:59:59.5Z"},
		{"2023-05-30T23:40:00.123456789", "2023-05-30T23:40:00.123456Z"},
		{"2023-05-30 23:40", "2023-05-30T23:40:00Z"},
		{"2023-05-30T23:40:00z", "2023-05-30T23:40:00Z"},
		{"2023-05-30T23:40:00+0200", "2023-05-30T21:40:00Z"},
		{"2023-05-30T23:40:00-05:30", "2023-05-31T05:10:00Z"},
		{"0001-01-01", "0001-01-01T00:00:00Z"},
		{"9999-12-31T23:59:59", "9999-12-31T23:59:59Z"},
	} {
		parsed, err := parsePythonDateTime(tc.value)
		if err != nil {
			t.Errorf("%q: %v", tc.value, err)
			continue
		}
		if got := parsed.Format("2006-01-02T15:04:05.999999Z07:00"); got != tc.want {
			t.Errorf("%q = %s, want %s", tc.value, got, tc.want)
		}
	}
}

// `query_timestamp` is parsed by the reference with the standard library's
// `datetime.fromisoformat`, whose messages name the offending component — a
// different parser from the framework's `datetime` field, so a table of its own.
func TestQueryTimestampStdlibMessages(t *testing.T) {
	for _, tc := range []struct{ value, message string }{
		{"nonsense", "Invalid isoformat string: 'nonsense'"},
		{"bad", "Invalid isoformat string: 'bad'"},
		{"", "Invalid isoformat string: ''"},
		{"20235", "Invalid isoformat string: '20235'"},
		{"1700000000", "Invalid isoformat string: '1700000000'"},
		{"2023-05-30T25:40:00", "hour must be in 0..23"},
		{"2023-13-45", "month must be in 1..12"},
		{"2023-02-30", "day is out of range for month"},
	} {
		if _, err := parseStdlibDateTime(tc.value); err == nil || err.Error() != tc.message {
			t.Errorf("%q = %v, want %q", tc.value, err, tc.message)
		}
	}
	for _, tc := range []struct{ value, want string }{
		{"2023-04-01", "2023-04-01T00:00:00Z"},
		{"2023-04-01T00:00:00", "2023-04-01T00:00:00Z"},
		{"2023-04-01T02:00:00+02:00", "2023-04-01T00:00:00Z"},
		{"2023-04-01T00:00:00Z", "2023-04-01T00:00:00Z"},
		{"20230530T2340", "2023-05-30T23:40:00Z"},
		{"2023-05-30 23:40", "2023-05-30T23:40:00Z"},
		{"2023-05-30T23", "2023-05-30T23:00:00Z"},
		{"2023-05-30T23:40:00.123456789", "2023-05-30T23:40:00.123456Z"},
	} {
		parsed, err := parseStdlibDateTime(tc.value)
		if err != nil {
			t.Errorf("%q: %v", tc.value, err)
			continue
		}
		if got := parsed.Format("2006-01-02T15:04:05.999999Z07:00"); got != tc.want {
			t.Errorf("%q = %s, want %s", tc.value, got, tc.want)
		}
	}
}

// assertDetailItems compares the failure's items with the JSON texts the
// reference serves, in order.
func assertDetailItems(t *testing.T, body []byte, want []string) {
	t.Helper()
	var envelope struct {
		Detail []json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("detail is not an item list: %s", body)
	}
	if len(envelope.Detail) != len(want) {
		t.Fatalf("items = %d, want %d: %s", len(envelope.Detail), len(want), body)
	}
	for index, raw := range envelope.Detail {
		var got, expected any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want[index]), &expected); err != nil {
			t.Fatal(err)
		}
		gotText, _ := json.Marshal(got)
		expectedText, _ := json.Marshal(expected)
		if string(gotText) != string(expectedText) {
			t.Fatalf("item %d = %s, want %s", index, gotText, expectedText)
		}
	}
}

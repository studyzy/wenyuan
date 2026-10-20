package validation

import (
	"encoding/json"
	"os"
	"testing"
)

// The reference types a dry-run timestamp as a plain `datetime`, so the
// framework's parser decides what the field accepts and what it says when it
// does not. testdata/datetime_corpus.json is that parser's own answers, each
// pair a value and the `ctx.error` text the reference reports for it ("" for an
// accepted value), captured from the pydantic build the reference pins.
func TestTimestampFailureMatchesTheReferenceParser(t *testing.T) {
	for _, testCase := range loadDatetimeCorpus(t) {
		if got := timestampParseError(testCase[0]); got != testCase[1] {
			t.Errorf("%q: message = %q, want %q", testCase[0], got, testCase[1])
		}
	}
}

// loadDatetimeCorpus reads the captured answers, skipping the test when the
// corpus has not been checked out (a source-only build).
func loadDatetimeCorpus(t *testing.T) [][2]string {
	t.Helper()
	data, err := os.ReadFile("testdata/datetime_corpus.json")
	if err != nil {
		t.Skipf("datetime corpus unavailable: %v", err)
	}
	var cases [][2]string
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("datetime corpus is not readable: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("datetime corpus is empty")
	}
	return cases
}

// TestTimestampFailureAgreesOnTheShapesClientsSend is the corpus slice a reader
// can scan: the boundary shapes of every message family, so a change to the
// parser that alters one of them fails without needing the full corpus.
func TestTimestampFailureAgreesOnTheShapesClientsSend(t *testing.T) {
	for _, testCase := range timestampShapes {
		if got := timestampParseError(testCase.value); testCase.message != got {
			t.Errorf("%q: message = %q, want %q", testCase.value, got, testCase.message)
		}
	}
}

var timestampShapes = []struct{ value, message string }{
	{"", "input is too short"},
	{" ", "input is too short"},
	{"  7t477-t:", "invalid character in year"},
	{" +2,-8tT 6", "invalid character in year"},
	{"0000-01-15", "year 0 is out of range"},
	{"0000-01-99", "day value is outside expected range"},
	{"0000-02-29", "year 0 is out of range"},
	{"0000-02-30", "day value is outside expected range"},
	{"0001-00-01", "month value is outside expected range of 1-12"},
	{"0001-00-15", "month value is outside expected range of 1-12"},
	{"05333Z37--", "invalid date separator, expected `-`"},
	{"0645-.9.+T06z,T4 z:_ 6", "invalid character in month"},
	{"12345-01-0", "invalid date separator, expected `-`"},
	{"2023-02-29T10:30:00+02:00:00", "day value is outside expected range"},
	{"2023-02-29T10:30:00.1234567", "day value is outside expected range"},
	{"2024-00-10T10:30:00+02:00:00", "month value is outside expected range of 1-12"},
	{"2024-01-15 ", "unexpected extra characters at the end of the input"},
	{"2024-01-15T", "unexpected extra characters at the end of the input"},
	{"2024-01-15T10:30:00+02:00:00", "unexpected extra characters at the end of the input"},
	{"2024-02-29T10:30:", "unexpected extra characters at the end of the input"},
	{"2024-02-29T10:30:00+02:00:00", "unexpected extra characters at the end of the input"},
	{"2024-02-31T23:59:59", "day value is outside expected range"},
	{"2024-13-01T10:30:00+02:00:00", "month value is outside expected range of 1-12"},
	{"2038-13-29T23:59:59", "month value is outside expected range of 1-12"},
	{"2520z44+48_8456t8Z", "invalid date separator, expected `-`"},
	{"42-6t2-69:Zz8t8:Z", "invalid character in year"},
	{"5:344:", "input is too short"},
	{"60283_-9zTt.Z.25:76..7t .zZT32", "invalid date separator, expected `-`"},
	{"8171093,-:44-2:tZ20+_3Z.t+Z,6T", "invalid date separator, expected `-`"},
	{"8907-Z0-.- ", "invalid character in month"},
	{"9481-0t 111,T0618t5 z-", "invalid character in month"},
	{"9738-0-0-5T+:12786z10.+t.,", "invalid character in month"},
	{"9913-0_56,-3+-T 2t0748,TTT+:", "invalid character in month"},
	{"z_z5_:246", "input is too short"},
	{"zt.:8Z  .7T59 0t 2_9-7:15zt3-6", "invalid character in year"},
	{"zt6zZ4+,-", "input is too short"},
	{"zz+251986 Z4Z: _838 90T7z_ 7-7", "invalid character in year"},
}

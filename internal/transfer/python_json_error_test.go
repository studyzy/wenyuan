package transfer

import "testing"

// The reference reads a template manifest body with
// `raw_body = await request.json()` (api/http.py:8755), outside the handler's
// inner try, so a decode failure escapes to the outer handler and is served as
// `detail=str(e)` -- CPython's message. These are the exact strings CPython
// 3.11 raises for each body; Go's own messages ("invalid character 'o' in
// literal null", "unexpected end of JSON input") are not the contract.
func TestPythonJSONErrorMatchesCPython(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{``, `Expecting value: line 1 column 1 (char 0)`},
		{`notjson`, `Expecting value: line 1 column 1 (char 0)`},
		{`{`, `Expecting property name enclosed in double quotes: line 1 column 2 (char 1)`},
		{`{"a":}`, `Expecting value: line 1 column 6 (char 5)`},
		{`{"a" 1}`, `Expecting ':' delimiter: line 1 column 6 (char 5)`},
		{`[1,]`, `Expecting value: line 1 column 4 (char 3)`},
		{`"unterminated`, `Unterminated string starting at: line 1 column 1 (char 0)`},
		{`{"a":1}x`, `Extra data: line 1 column 8 (char 7)`},
		{`tru`, `Expecting value: line 1 column 1 (char 0)`},
		{`nul`, `Expecting value: line 1 column 1 (char 0)`},
		{`{"a":1,}`, `Expecting property name enclosed in double quotes: line 1 column 8 (char 7)`},
		{`  [`, `Expecting value: line 1 column 4 (char 3)`},
		{`[`, `Expecting value: line 1 column 2 (char 1)`},
		{`[1`, `Expecting ',' delimiter: line 1 column 3 (char 2)`},
		{`[1 2]`, `Expecting ',' delimiter: line 1 column 4 (char 3)`},
		{`truex`, `Extra data: line 1 column 5 (char 4)`},
		{`  tru`, `Expecting value: line 1 column 3 (char 2)`},
		{`{"a":1 "b":2}`, `Expecting ',' delimiter: line 1 column 8 (char 7)`},
		{`[{"a":1}`, `Expecting ',' delimiter: line 1 column 9 (char 8)`},
		{`{"a":`, `Expecting value: line 1 column 6 (char 5)`},
		{`{"a":1,`, `Expecting property name enclosed in double quotes: line 1 column 8 (char 7)`},
		// Multi-line bodies report the line and the column within it.
		{"{\n  \"a\":\n}", `Expecting value: line 3 column 1 (char 9)`},
		{"{\n \"a\" 1\n}", `Expecting ':' delimiter: line 2 column 6 (char 7)`},
		{"[\n1,\n]", `Expecting value: line 3 column 1 (char 5)`},
		{"{\n", `Expecting property name enclosed in double quotes: line 2 column 1 (char 2)`},
		{"[\n", `Expecting value: line 2 column 1 (char 2)`},
	}
	for _, tc := range cases {
		if got := pythonJSONError([]byte(tc.body)).Error(); got != tc.want {
			t.Errorf("body %q:\n got %s\nwant %s", tc.body, got, tc.want)
		}
	}
}

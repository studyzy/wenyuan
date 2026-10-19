package httpx

import "testing"

// pythonDecodeErrorCases pins str(JSONDecodeError) — the sentence CPython's json
// module renders for a malformed document, line and column included. The MCP
// transport reports this text where the reference's own transport does, so it
// is derived from CPython's json.loads rather than from the Go decoder.
var pythonDecodeErrorCases = []struct {
	name string
	body string
	want string
}{
	{"empty", "", "Expecting value: line 1 column 1 (char 0)"},
	{"bare word", "x", "Expecting value: line 1 column 1 (char 0)"},
	{"open brace", "{", "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"},
	{"missing colon", `{"a"`, "Expecting ':' delimiter: line 1 column 5 (char 4)"},
	{"array trailing comma", "[1,", "Expecting value: line 1 column 4 (char 3)"},
	{"missing value", `{"a":}`, "Expecting value: line 1 column 6 (char 5)"},
	{"object trailing comma", `{"a":1,}`, "Expecting property name enclosed in double quotes: line 1 column 8 (char 7)"},
	{"literal typo", "nul", "Expecting value: line 1 column 1 (char 0)"},
	{"unterminated string", `"abc`, "Unterminated string starting at: line 1 column 1 (char 0)"},
	{"trailing text", "{} {}", "Extra data: line 1 column 4 (char 3)"},
	{"whitespace only", "  ", "Expecting value: line 1 column 3 (char 2)"},
	{"second line", "{\n\"a\": ,\n}", "Expecting value: line 2 column 6 (char 7)"},
	{"multibyte prefix", "{\"é\": x}", "Expecting value: line 1 column 7 (char 6)"},
}

func TestPythonJSONDecodeErrorText(t *testing.T) {
	for _, tc := range pythonDecodeErrorCases {
		got, ok := PythonJSONDecodeError([]byte(tc.body))
		if !ok {
			t.Errorf("%s: reported no failure for %q", tc.name, tc.body)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A document CPython accepts — including the NaN and Infinity literals, which
// the Go decoder rejects — reports no failure, so the caller keeps its own
// rendering instead of inventing a CPython sentence.
func TestPythonJSONDecodeErrorAcceptsCleanDocument(t *testing.T) {
	for _, body := range []string{`{"a": 1}`, "NaN", "[Infinity, -Infinity]"} {
		if text, ok := PythonJSONDecodeError([]byte(body)); ok {
			t.Errorf("%q: unexpected failure %q", body, text)
		}
	}
}

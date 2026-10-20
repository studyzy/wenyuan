package fileparse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture loads one committed testdata file.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// The built-in parser converts each advertised format into markdown the retain
// path can consume.
func TestMarkitdownConvertsEachFormat(t *testing.T) {
	cases := []struct {
		file  string
		wants []string
	}{
		{"sample.pdf", []string{"sample document", "OCR"}},
		{"sample.docx", []string{"Quarterly report", "Revenue", "grew", "twice"}},
		{"sample.pptx", []string{"## Slide 1", "Welcome to the deck", "Slide body text"}},
		{"sample.xlsx", []string{"## Totals", "| Name | Score |", "Alice", "42"}},
		{"sample.html", []string{"# Heading One", "Intro & overview.", "- First item", "[the docs](https://example.com/doc)"}},
		{"sample.csv", []string{"| City | Population |", "Paris", "2148000"}},
	}
	parser := NewMarkitdownParser()
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data := readFixture(t, tc.file)
			if !parser.Supports(tc.file, "") {
				t.Fatalf("%s should be supported", tc.file)
			}
			out, err := parser.Convert(data, tc.file)
			if err != nil {
				t.Fatalf("convert %s: %v", tc.file, err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(out, want) {
					t.Errorf("converted %s missing %q:\n%s", tc.file, want, out)
				}
			}
		})
	}
}

// A plain-text format round-trips unchanged, and a format the build cannot read
// is reported as unsupported.
func TestMarkitdownTextAndUnsupported(t *testing.T) {
	parser := NewMarkitdownParser()
	out, err := parser.Convert([]byte("# Title\n\nbody"), "notes.md")
	if err != nil || out != "# Title\n\nbody" {
		t.Fatalf("markdown round-trip = %q, %v", out, err)
	}
	if parser.Supports("legacy.doc", "") {
		t.Fatal("a legacy .doc must not be advertised as supported")
	}
	// Bytes that are not text are refused for an extension no converter claims:
	// the reference reports exactly this for a binary upload it cannot read.
	_, err = parser.Convert([]byte{0x00, 0x01, 0x02, 0xff}, "legacy.doc")
	if !IsUnsupported(err) {
		t.Fatalf("converting a binary .doc should report unsupported, got %v", err)
	}
}

// The reference's markitdown reads an unrecognized extension as plain text when
// the bytes are text, and refuses only bytes that are not. Verified against the
// live reference: a .xyz upload of "plain words here" is retained verbatim,
// while a binary .bin upload fails its operation with "No converter attempted a
// conversion".
func TestMarkitdownReadsUnrecognizedTextFormats(t *testing.T) {
	parser := NewMarkitdownParser()
	cases := []struct{ name, filename, content string }{
		{"plain text under an unknown extension", "notes.xyz", "plain words here"},
		{"plain text with no extension", "README", "hello"},
		{"utf-8 text under a binary-looking extension", "data.bin", "héllo wörld"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			out, err := parser.Convert([]byte(testCase.content), testCase.filename)
			if err != nil {
				t.Fatalf("convert = %v, want the text back", err)
			}
			if out != testCase.content {
				t.Fatalf("convert = %q, want %q", out, testCase.content)
			}
		})
	}
	// A NUL byte or an invalid UTF-8 sequence marks a binary file; either is
	// refused rather than stored as memory text.
	for _, data := range [][]byte{{0x41, 0x00, 0x42}, {0x41, 0xff, 0x42}} {
		if _, err := parser.Convert(data, "blob.xyz"); !IsUnsupported(err) {
			t.Fatalf("convert(%v) = %v, want unsupported", data, err)
		}
	}
}

// stubParser is a Parser with scripted behaviour for chain tests.
type stubParser struct {
	name     string
	support  bool
	content  string
	failWith error
	calls    *int
}

func (s stubParser) Name() string { return s.name }

func (s stubParser) Supports(string, string) bool { return s.support }

func (s stubParser) Convert([]byte, string) (string, error) {
	if s.calls != nil {
		*s.calls++
	}
	if s.failWith != nil {
		return "", s.failWith
	}
	return s.content, nil
}

// A parser that declines the file falls through to the next, which converts.
func TestChainFallsThroughToNextParser(t *testing.T) {
	calls := 0
	registry := NewRegistry()
	registry.Register(stubParser{name: "first", support: true,
		failWith: &UnsupportedFileTypeError{Parser: "first", Filename: "x.bin", Reason: "no"}, calls: &calls})
	registry.Register(stubParser{name: "second", support: true, content: "converted body"})

	result, err := registry.ConvertWithFallback([]string{"first", "second"}, []byte("x"), "x.bin", "")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if result.ParserName != "second" || result.Content != "converted body" {
		t.Fatalf("chain result = %+v, want second/converted body", result)
	}
	if calls != 1 {
		t.Fatalf("first parser calls = %d, want 1", calls)
	}
}

// An empty result also falls through, and an exhausted chain propagates the
// last error.
func TestChainExhaustedPropagatesLastError(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubParser{name: "blank", support: true, content: "   "})
	registry.Register(stubParser{name: "broken", support: true, failWith: errHard})

	_, err := registry.ConvertWithFallback([]string{"blank", "broken"}, []byte("x"), "x.bin", "")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("exhausted chain error = %v, want the last failure", err)
	}
}

// An unregistered name is skipped in favour of a later working parser.
func TestChainUnknownNameSkipsToNext(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubParser{name: "ok", support: true, content: "body"})
	result, err := registry.ConvertWithFallback([]string{"missing", "ok"}, []byte("x"), "x.bin", "")
	if err != nil || result.ParserName != "ok" {
		t.Fatalf("chain with unknown name = %+v, %v", result, err)
	}
}

// GetParser auto-detects by Supports when no name is given.
func TestGetParserAutoDetect(t *testing.T) {
	registry := NewRegistry()
	parser := NewMarkitdownParser()
	registry.Register(parser)
	got, err := registry.GetParser("", "report.pdf", "application/pdf")
	if err != nil || got.Name() != "markitdown" {
		t.Fatalf("auto-detect = %v, %v", got, err)
	}
	if _, err := registry.GetParser("", "legacy.doc", ""); err == nil {
		t.Fatal("auto-detect of an unsupported file should fail")
	}
}

var errHard = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

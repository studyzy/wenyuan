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
	_, err = parser.Convert([]byte("binary"), "legacy.doc")
	if !IsUnsupported(err) {
		t.Fatalf("converting a .doc should report unsupported, got %v", err)
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

// Package fileparse converts an uploaded document into markdown before it is
// retained, mirroring the reference's pluggable file-parser chain: a configured
// order of parsers is walked and the first one that yields content wins.
//
// The build stays standard-library only (see AGENTS.md, "Standard library
// first"). The local parsers read PDF, DOCX, XLSX, PPTX, HTML, CSV/TSV,
// JSON/JSONL and plain text with nothing but archive/zip, encoding/xml,
// encoding/csv, encoding/json and compress/zlib, so no new module dependency
// is introduced. A format this build cannot read (legacy .doc/.ppt/.xls, audio,
// images without OCR) is reported as unsupported and the chain moves on, which
// is exactly how the reference treats a parser that declines a file.
package fileparse

import (
	"errors"
	"fmt"
	"strings"
)

// Parser converts one file's bytes to markdown.
//
// The method set mirrors the reference's FileParser: convert turns bytes into
// markdown, supports answers whether the parser can read a file (the default
// for a remote parser that defers the decision to its service is to accept
// everything and raise UnsupportedFileTypeError from convert), and name is the
// identifier a client requests the parser by.
type Parser interface {
	// Convert turns the file bytes into markdown. It returns an
	// UnsupportedFileTypeError when this parser cannot read the format, and any
	// other error when it can but failed.
	Convert(data []byte, filename string) (string, error)
	// Supports reports whether this parser handles the named file. A parser
	// that delegates the decision to a remote service returns true and lets
	// Convert raise UnsupportedFileTypeError instead.
	Supports(filename, contentType string) bool
	// Name is the parser's registered identifier.
	Name() string
}

// UnsupportedFileTypeError is raised by a parser when it does not support the
// given file type. The chain treats it as a reason to try the next parser.
type UnsupportedFileTypeError struct {
	// Parser is the name of the parser that declined the file.
	Parser string
	// Filename is the file the parser declined.
	Filename string
	// Reason is a short explanation for the operator.
	Reason string
}

func (e *UnsupportedFileTypeError) Error() string {
	if e.Reason != "" {
		return e.Parser + " does not support '" + e.Filename + "': " + e.Reason
	}
	return e.Parser + " does not support '" + e.Filename + "'"
}

// unsupported builds an UnsupportedFileTypeError for a named parser.
func unsupported(parser, filename, reason string) error {
	return &UnsupportedFileTypeError{Parser: parser, Filename: filename, Reason: reason}
}

// IsUnsupported reports whether err is (or wraps) an UnsupportedFileTypeError.
func IsUnsupported(err error) bool {
	var target *UnsupportedFileTypeError
	return errors.As(err, &target)
}

// Result is the outcome of a successful conversion.
type Result struct {
	// Content is the converted markdown.
	Content string
	// ParserName is the name of the parser that produced it.
	ParserName string
}

// Registry holds the parsers this build can run, keyed by name.
//
// Registration order is preserved so an explicit name lookup is exact and a
// name-less lookup walks the parsers in the order they were registered, which
// is the reference's behaviour.
type Registry struct {
	parsers map[string]Parser
	order   []string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{parsers: map[string]Parser{}}
}

// Register adds a parser, replacing any parser already registered under the
// same name. Registration order is preserved for auto-detection.
func (r *Registry) Register(p Parser) {
	name := p.Name()
	if _, exists := r.parsers[name]; !exists {
		r.order = append(r.order, name)
	}
	r.parsers[name] = p
}

// List returns the registered parser names in registration order.
func (r *Registry) List() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Get returns a parser by name.
func (r *Registry) Get(name string) (Parser, bool) {
	p, ok := r.parsers[name]
	return p, ok
}

// GetParser resolves a parser by name, or by auto-detection when the name is
// empty: the first registered parser whose Supports reports the file.
func (r *Registry) GetParser(name, filename, contentType string) (Parser, error) {
	if name != "" {
		if p, ok := r.parsers[name]; ok {
			return p, nil
		}
		return nil, fmt.Errorf("parser '%s' not found. Available: %v", name, r.order)
	}
	for _, registered := range r.order {
		if r.parsers[registered].Supports(filename, contentType) {
			return r.parsers[registered], nil
		}
	}
	return nil, fmt.Errorf("no parser found for %s. Available parsers: %v", filename, r.order)
}

// ConvertWithFallback walks the chain in order, returning the first parser's
// non-blank content. It mirrors the reference: an UnsupportedFileTypeError, an
// empty result, or any other error all move the chain to the next parser, and
// when the chain is exhausted the last error propagates.
func (r *Registry) ConvertWithFallback(chain []string, data []byte, filename, contentType string) (Result, error) {
	var lastErr error
	for _, name := range chain {
		parser, err := r.GetParser(name, filename, contentType)
		if err != nil {
			lastErr = err
			continue
		}
		content, err := parser.Convert(data, filename)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(content) == "" {
			lastErr = fmt.Errorf("parser '%s' returned no content for '%s'", name, filename)
			continue
		}
		return Result{Content: content, ParserName: name}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no parsers available for '%s'", filename)
	}
	return Result{}, lastErr
}

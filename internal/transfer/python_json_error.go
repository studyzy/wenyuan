package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// pythonJSONError renders a body that is not valid JSON the way the reference's
// `await request.json()` does, so the route's 400 carries Python's message
// rather than Go's.
//
// The reference reads the template manifest body with
// `raw_body = await request.json()` (`api/http.py:8755`). That call sits
// *outside* the handler's inner try, so a `JSONDecodeError` escapes to the
// route's outer handler, which answers `detail=str(e)` — CPython's message and
// nothing else:
//
//	Expecting value: line 1 column 1 (char 0)
//	Expecting property name enclosed in double quotes: line 1 column 2 (char 1)
//
// Python's decoder and Go's report the same facts in different words, so the
// message is rebuilt from the position Go found rather than passed through.
// `char` is CPython's 0-based offset of the offending byte and `column` its
// 1-based form, both of which follow from that same position.
//
// The empty body is a separate case: Python's `request.json()` on zero bytes
// raises the same "Expecting value" error at char 0, so an empty body renders
// through this path too rather than through a message of its own.
func pythonJSONError(data []byte) error {
	offset, kind := locateJSONError(data)
	column := offset + 1
	// CPython counts a line break as ending a line, so the reported line is the
	// number of newlines before the offset plus one, and the column restarts
	// after the last one.
	line := 1 + bytes.Count(data[:offset], []byte("\n"))
	if last := bytes.LastIndexByte(data[:offset], '\n'); last >= 0 {
		column = offset - last
	}
	at := ": line " + strconv.Itoa(line) + " column " + strconv.Itoa(column) +
		" (char " + strconv.Itoa(offset) + ")"
	return errors.New(kind + at)
}

// The CPython messages `json.loads` raises for the shapes a request body
// reaches. Their wording is Python's, including the capitalisation and the
// absence of a trailing period.
const (
	msgExpectingValue    = "Expecting value"
	msgPropertyName      = "Expecting property name enclosed in double quotes"
	msgColonDelimiter    = "Expecting ':' delimiter"
	msgCommaDelimiter    = "Expecting ',' delimiter"
	msgUnterminatedStr   = "Unterminated string starting at"
	msgExtraData         = "Extra data"
	msgInvalidControlChr = "Invalid control character at"
)

// locateJSONError reports the byte offset CPython would report for this body
// and the message it would give there.
//
// CPython's decoder reports the offset where the production it was reading
// *began*, not where the scan gave up: `tru` is "Expecting value" at char 0
// because it tried to read a value and the literal never completed, while Go
// names the byte it choked on. So the offset is re-derived from the document
// shape rather than taken from Go's error, and Go's message only picks the
// wording family.
func locateJSONError(data []byte) (int, string) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	err := decoder.Decode(&decoded)
	if err == nil {
		// The value parsed; anything after it is trailing input, which Python
		// reports as "Extra data" at the first byte past the document.
		if offset := decoder.InputOffset(); offset < int64(len(data)) {
			return int(offset), msgExtraData
		}
		return len(data), msgExpectingValue
	}

	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return classifySyntax(data, int(syntax.Offset)-1, syntax.Error())
	}
	// `unexpected EOF`: the document stopped mid-production.
	return classifyTruncated(data)
}

// classifySyntax maps a Go syntax error, which names the offending byte, onto
// the offset and wording CPython reports for the same document.
func classifySyntax(data []byte, offset int, goMessage string) (int, string) {
	switch {
	case strings.Contains(goMessage, "in literal"):
		// Go names the byte where a literal (`true`, `false`, `null`) stopped
		// matching; Python reports "Expecting value" at the literal's first
		// byte, because for it the value simply never began.
		return tokenStart(data, offset), msgExpectingValue
	case strings.Contains(goMessage, "looking for beginning of value"):
		return offset, msgExpectingValue
	case strings.Contains(goMessage, "looking for beginning of object key string"):
		return offset, msgPropertyName
	// The longer spelling is tested first: "after object key:value pair"
	// contains "after object key", and the two are different Python messages.
	case strings.Contains(goMessage, "after object key:value pair"):
		return offset, msgCommaDelimiter
	case strings.Contains(goMessage, "after object key"):
		return offset, msgColonDelimiter
	case strings.Contains(goMessage, "after array element"):
		return offset, msgCommaDelimiter
	case strings.Contains(goMessage, "after top-level value"):
		return offset, msgExtraData
	case strings.Contains(goMessage, "in string literal"),
		strings.Contains(goMessage, "in \\u"):
		if start := stringStartBefore(data, offset); start >= 0 {
			return start, msgUnterminatedStr
		}
		return tokenStart(data, offset), msgExpectingValue
	}
	return offset, msgExpectingValue
}

// classifyTruncated handles a document that ended before its value did.
//
// Inside an unfinished string, Python names the opening quote. Otherwise it
// names the position where the production it was reading starts: a bare `[`
// reports char 1 (a value was expected there), while `[1` reports char 2 (a
// comma or closing bracket was expected there).
func classifyTruncated(data []byte) (int, string) {
	if start := openStringStart(data); start >= 0 {
		return start, msgUnterminatedStr
	}
	// CPython's scanner consumes trailing whitespace before it discovers the
	// document ended, so the position it names is past it -- `"{\n"` reports
	// line 2 column 1, not line 1 column 2.
	position := len(data)
	for position > 0 && isJSONWhitespace(data[position-1]) {
		position--
	}
	if position == 0 {
		return 0, msgExpectingValue
	}
	if data[position-1] == '{' || data[position-1] == ',' ||
		data[position-1] == ':' || data[position-1] == '[' {
		// CPython consumes trailing whitespace before noticing the document
		// ended, so it names the document's end rather than the separator --
		// but *what* it was waiting for still depends on the separator.
		at := position
		if len(bytes.TrimRight(data, " \t\r\n")) != len(data) {
			at = len(data)
		}
		// An object's `{` or `,` starts a key rather than a value.
		if (data[position-1] == '{' || data[position-1] == ',') && innermostIsObject(data[:position]) {
			return at, msgPropertyName
		}
		return at, msgExpectingValue
	}
	if data[position-1] == '{' || data[position-1] == ',' || data[position-1] == ':' ||
		data[position-1] == '[' {
		// Python is about to read a value here, naming the position just past
		// the separator. An object's `{` or `,` starts a key instead, which
		// CPython names with its own message.
		if atValueStart := len(bytes.TrimRight(data, " \t\r\n")); atValueStart != len(data) {
			// Trailing whitespace was consumed by the scanner; the position it
			// reports is the end of the document.
			return len(data), msgExpectingValue
		}
		if data[position-1] == '{' || data[position-1] == ',' {
			if innermostIsObject(data[:position]) {
				return position, msgPropertyName
			}
		}
		return position, msgExpectingValue
	}
	// A document that stops inside a bare word is either a literal that never
	// completed -- Python reports "Expecting value" at the word's first byte --
	// or a number already consumed, after which a comma was due. What decides
	// is whether a value could still have been starting at that word: at the
	// head of the document or after a separator it could, inside an array
	// holding an element it could not.
	if start := tokenStart(data, position); isWordByte(data[position-1]) {
		if expectsValueAt(data, start) {
			return start, msgExpectingValue
		}
	}
	return position, msgCommaDelimiter
}

// innermostIsObject reports whether the innermost container open at the end of
// prefix is an object, which decides whether the position expects a key or a
// value.
func innermostIsObject(prefix []byte) bool {
	stack := make([]byte, 0, 8)
	inString, escaped := false, false
	for _, b := range prefix {
		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		case inString:
		case b == '{', b == '[':
			stack = append(stack, b)
		case b == '}', b == ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return len(stack) > 0 && stack[len(stack)-1] == '{'
}

// expectsValueAt reports whether a JSON value may begin at this offset: at the
// head of the document, or directly after a `[`, `{`, `,` or `:` (modulo
// whitespace). Anywhere else the scanner has already consumed a value.
func expectsValueAt(data []byte, offset int) bool {
	if offset > len(data) {
		offset = len(data)
	}
	for offset > 0 && isJSONWhitespace(data[offset-1]) {
		offset--
	}
	if offset == 0 {
		return true
	}
	// A separator always precedes a value. A container opener does not: by the
	// time the word after it has been scanned, that container already holds
	// an element, so the opener was the value's container rather than the
	// position the value began at.
	switch data[offset-1] {
	case ',', ':':
		return true
	}
	return false
}

// tokenStart walks back over a bare word to the byte that starts it, which is
// where CPython anchors "Expecting value" for a truncated literal.
func tokenStart(data []byte, offset int) int {
	if offset > len(data) {
		offset = len(data)
	}
	start := offset
	for start > 0 && isWordByte(data[start-1]) {
		start--
	}
	return start
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isJSONWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// openStringStart reports the offset of the opening quote of a string the
// document never closed, or -1 when the trailing input is not inside one.
func openStringStart(data []byte) int {
	inString, escaped, start := false, false, -1
	for i, b := range data {
		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
			if inString {
				start = i
			}
		}
	}
	if inString {
		return start
	}
	return -1
}

// stringStartBefore reports the opening quote of the string containing offset,
// or -1 when offset does not sit inside one.
func stringStartBefore(data []byte, offset int) int {
	if offset > len(data) {
		offset = len(data)
	}
	inString, escaped, start := false, false, -1
	for i := 0; i < offset; i++ {
		switch {
		case escaped:
			escaped = false
		case inString && data[i] == '\\':
			escaped = true
		case data[i] == '"':
			inString = !inString
			if inString {
				start = i
			}
		}
	}
	if inString {
		return start
	}
	return -1
}

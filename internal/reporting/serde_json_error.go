package reporting

import (
	"fmt"
	"strings"
)

// serde_json_error.go reproduces the failure text a request parameter carrying
// JSON reports in the reference when the document will not parse.
//
// The reference validates `tag_groups` through a Pydantic `TypeAdapter`, and its
// FastAPI layer decodes the parameter with a Rust-backed JSON parser. The
// `json_invalid` message therefore carries *that* parser's wording — "expected
// ident", "trailing characters", "EOF while parsing a list" — not the CPython
// json module's ("Expecting value", "Extra data", ...) that `httpx` reproduces
// for request bodies. Both reach a client as a 422 detail, so the two must not be
// confused: a body error and a query-parameter error are different parsers.
//
// Positions are 1-based line and column, and the column counts characters from
// the start of its line, which is how this parser reports them.

// serdeJSONError renders the failure of parsing raw, in the reference's wording.
func serdeJSONError(raw string) string {
	runes := []rune(raw)
	end, failure := serdeScanValue(runes, serdeSkipSpace(runes, 0))
	if failure != nil {
		return failure.render(runes)
	}
	if tail := serdeSkipSpace(runes, end); tail != len(runes) {
		return serdeFailure{at: tail, message: "trailing characters"}.render(runes)
	}
	return ""
}

// serdeFailure is one parse failure: the character index it was found at and the
// parser's bare message.
type serdeFailure struct {
	at      int
	message string
}

// render formats the failure the way the parser does: message, then 1-based line
// and column. An end-of-input failure names the last character present, so its
// position is clamped back onto it.
func (f serdeFailure) render(runes []rune) string {
	at := f.at
	if strings.HasPrefix(f.message, "EOF ") && at > 0 {
		at--
	}
	line, column := 1, 1
	for i := 0; i < at && i < len(runes); i++ {
		if runes[i] == '\n' {
			line, column = line+1, 1
			continue
		}
		column++
	}
	return fmt.Sprintf("%s at line %d column %d", f.message, line, column)
}

// serdeAtEnd reports whether idx is past the end of the document.
func serdeAtEnd(runes []rune, idx int) bool { return idx >= len(runes) }

// serdeScanValue scans one JSON value at idx.
func serdeScanValue(runes []rune, idx int) (int, *serdeFailure) {
	if serdeAtEnd(runes, idx) {
		return 0, &serdeFailure{at: idx, message: "EOF while parsing a value"}
	}
	switch runes[idx] {
	case '{':
		return serdeScanObject(runes, idx+1)
	case '[':
		return serdeScanArray(runes, idx+1)
	case '"':
		return serdeScanString(runes, idx+1)
	}
	if literal, ok := serdeMatchLiteral(runes, idx); ok {
		return idx + len(literal), nil
	}
	if end, ok, failure := serdeScanNumber(runes, idx); failure != nil {
		return 0, failure
	} else if ok {
		return end, nil
	}
	// A character that begins a bare literal is an identity match that failed:
	// the parser enters the literal scanner and gives up at the first character
	// that does not continue it, reporting the position one past that character.
	// Anything else starts no value at all.
	if serdeStartsLiteral(runes[idx]) {
		at := serdeIdentFailure(runes, idx)
		if at >= len(runes) {
			// The input ran out inside the literal, so the failure is the
			// missing text rather than a mismatched character.
			return 0, &serdeFailure{at: len(runes), message: "EOF while parsing a value"}
		}
		return 0, &serdeFailure{at: at, message: "expected ident"}
	}
	return 0, &serdeFailure{at: idx, message: "expected value"}
}

// serdeStartsLiteral reports whether c could begin one of the bare literals.
func serdeStartsLiteral(c rune) bool {
	return c == 't' || c == 'f' || c == 'n'
}

// serdeIdentFailure returns the index of the first character that breaks the
// literal starting at idx, or the position where the input ran out.
func serdeIdentFailure(runes []rune, idx int) int {
	literal := "null"
	switch runes[idx] {
	case 't':
		literal = "true"
	case 'f':
		literal = "false"
	}
	for i, c := range literal {
		if idx+i >= len(runes) || runes[idx+i] != c {
			return idx + i
		}
	}
	return idx
}

// serdeLiterals are the three bare words JSON allows, longest-prefix matched.
var serdeLiterals = []string{"true", "false", "null"}

// serdeMatchLiteral matches one of the bare literals at idx.
func serdeMatchLiteral(runes []rune, idx int) (string, bool) {
	for _, literal := range serdeLiterals {
		if !serdeHasWord(runes, idx, literal) {
			continue
		}
		// The literal must be followed by something that ends a value, or the
		// parser reports an identity failure at the first character past it.
		next := idx + len(literal)
		if serdeAtEnd(runes, next) || serdeIsValueTerminator(runes[next]) {
			return literal, true
		}
		return "", false
	}
	return "", false
}

// serdeHasWord reports whether literal sits at idx.
func serdeHasWord(runes []rune, idx int, literal string) bool {
	if idx+len(literal) > len(runes) {
		return false
	}
	for i, c := range literal {
		if runes[idx+i] != c {
			return false
		}
	}
	return true
}

// serdeIsValueTerminator reports whether c can follow a complete value.
func serdeIsValueTerminator(c rune) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', ']', '}', ':':
		return true
	}
	return false
}

// serdeScanNumber scans a number at idx. A malformed number reports
// "invalid number" at the index where the parser gives up.
func serdeScanNumber(runes []rune, idx int) (int, bool, *serdeFailure) {
	i := idx
	if i < len(runes) && runes[i] == '-' {
		i++
	}
	if serdeAtEnd(runes, i) {
		if i > idx {
			return 0, false, &serdeFailure{at: i, message: "invalid number"}
		}
		return 0, false, nil
	}
	switch {
	case runes[i] == '0':
		i++
		// A second leading digit is not part of the number.
		if i < len(runes) && serdeIsDigit(runes[i]) {
			return 0, false, &serdeFailure{at: i, message: "invalid number"}
		}
	case runes[i] >= '1' && runes[i] <= '9':
		for i < len(runes) && serdeIsDigit(runes[i]) {
			i++
		}
	default:
		if i > idx {
			return 0, false, &serdeFailure{at: i, message: "invalid number"}
		}
		return 0, false, nil
	}
	if i < len(runes) && runes[i] == '.' {
		i++
		if i >= len(runes) || !serdeIsDigit(runes[i]) {
			return 0, false, &serdeFailure{at: i, message: "invalid number"}
		}
		for i < len(runes) && serdeIsDigit(runes[i]) {
			i++
		}
	}
	if i < len(runes) && (runes[i] == 'e' || runes[i] == 'E') {
		i++
		if i < len(runes) && (runes[i] == '+' || runes[i] == '-') {
			i++
		}
		if i >= len(runes) || !serdeIsDigit(runes[i]) {
			return 0, false, &serdeFailure{at: i, message: "invalid number"}
		}
		for i < len(runes) && serdeIsDigit(runes[i]) {
			i++
		}
	}
	return i, true, nil
}

// serdeScanString scans a string whose body starts at end (just past the opening
// quote).
func serdeScanString(runes []rune, end int) (int, *serdeFailure) {
	for {
		if serdeAtEnd(runes, end) {
			return 0, &serdeFailure{at: end, message: "EOF while parsing a string"}
		}
		switch runes[end] {
		case '"':
			return end + 1, nil
		case '\\':
			end++
			if serdeAtEnd(runes, end) {
				return 0, &serdeFailure{at: end, message: "EOF while parsing a string"}
			}
			if !serdeIsEscape(runes[end]) {
				// A bad escape is reported at the escape character itself.
				return 0, &serdeFailure{at: end, message: "invalid escape"}
			}
			end++
		default:
			end++
		}
	}
}

// serdeIsEscape reports whether c is one of JSON's single-character escapes.
func serdeIsEscape(c rune) bool {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	}
	return false
}

// serdeScanObject scans an object whose body starts at end (just past '{').
func serdeScanObject(runes []rune, end int) (int, *serdeFailure) {
	end = serdeSkipSpace(runes, end)
	if serdeAtEnd(runes, end) {
		return 0, &serdeFailure{at: end, message: "EOF while parsing an object"}
	}
	if runes[end] == '}' {
		return end + 1, nil
	}
	for {
		if serdeAtEnd(runes, end) {
			return 0, &serdeFailure{at: end, message: "EOF while parsing an object"}
		}
		if runes[end] != '"' {
			return 0, &serdeFailure{at: end, message: "key must be a string"}
		}
		var failure *serdeFailure
		if end, failure = serdeScanString(runes, end+1); failure != nil {
			return 0, failure
		}
		end = serdeSkipSpace(runes, end)
		if serdeAtEnd(runes, end) {
			return 0, &serdeFailure{at: end, message: "EOF while parsing an object"}
		}
		if runes[end] != ':' {
			return 0, &serdeFailure{at: end, message: "expected `:`"}
		}
		end = serdeSkipSpace(runes, end+1)
		if end, failure = serdeScanValue(runes, end); failure != nil {
			return 0, failure
		}
		end = serdeSkipSpace(runes, end)
		if serdeAtEnd(runes, end) {
			return 0, &serdeFailure{at: end, message: "EOF while parsing an object"}
		}
		switch runes[end] {
		case '}':
			return end + 1, nil
		case ',':
			end = serdeSkipSpace(runes, end+1)
		default:
			return 0, &serdeFailure{at: end, message: "expected `,` or `}`"}
		}
	}
}

// serdeScanArray scans an array whose body starts at end (just past '[').
func serdeScanArray(runes []rune, end int) (int, *serdeFailure) {
	end = serdeSkipSpace(runes, end)
	if serdeAtEnd(runes, end) {
		return 0, &serdeFailure{at: end, message: "EOF while parsing a list"}
	}
	if runes[end] == ']' {
		return end + 1, nil
	}
	for {
		var failure *serdeFailure
		if end, failure = serdeScanValue(runes, end); failure != nil {
			return 0, failure
		}
		end = serdeSkipSpace(runes, end)
		if serdeAtEnd(runes, end) {
			return 0, &serdeFailure{at: end, message: "EOF while parsing a list"}
		}
		switch runes[end] {
		case ']':
			return end + 1, nil
		case ',':
			comma := end
			end = serdeSkipSpace(runes, end+1)
			if serdeAtEnd(runes, end) {
				// The comma promised another element, so the failure is the
				// missing value rather than the unclosed list.
				return 0, &serdeFailure{at: end, message: "EOF while parsing a value"}
			}
			if runes[end] == ']' {
				// A comma with nothing after it, before the close: reported at
				// the close, one past the comma.
				return 0, &serdeFailure{at: comma + 1, message: "trailing comma"}
			}
		default:
			return 0, &serdeFailure{at: end, message: "expected `,` or `]`"}
		}
	}
}

// serdeSkipSpace returns the first index at or after i that is not JSON
// whitespace.
func serdeSkipSpace(runes []rune, i int) int {
	for i < len(runes) {
		switch runes[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// serdeIsDigit reports whether c is an ASCII decimal digit.
func serdeIsDigit(c rune) bool { return c >= '0' && c <= '9' }

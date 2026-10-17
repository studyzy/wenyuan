package httpx

// jsonsyntax.go replicates the failure text and position CPython's json module
// reports for a malformed document. The reference's framework parses a request
// body with a parser whose messages and character positions are CPython's, so a
// body it rejects as `json_invalid` carries CPython's bare message and the
// 0-based character index of the failure — not the Go decoder's wording and
// byte offset. The scanner below walks the document the way CPython's scanner
// does, so both can be recovered without a dependency.

// jsonSyntaxError is one failure of the Python-style scan: the 0-based
// character position and the bare message CPython reports for it.
type jsonSyntaxError struct {
	pos int
	msg string
}

// pythonJSONError scans body the way CPython's json module does and returns the
// character position and bare message of the first failure it finds. The last
// result is false when the document scans cleanly, so a caller keeps its own
// rendering instead of reporting a failure that is not one (CPython accepts the
// NaN and Infinity literals the Go decoder rejects).
//
// Positions count characters, not bytes: a multi-byte character before the
// failure advances the position by one, which is what the reference reports for
// a UTF-8 body.
func pythonJSONError(body []byte) (int, string, bool) {
	runes := []rune(string(body))
	end, failure := scanOncePy(runes, skipSpacePy(runes, 0))
	if failure != nil {
		return failure.pos, failure.msg, true
	}
	if end = skipSpacePy(runes, end); end != len(runes) {
		// The document parsed, but trailing text followed it.
		return end, "Extra data", true
	}
	return 0, "", false
}

// scanOncePy scans one JSON value at idx, returning the index just past it or
// the failure CPython reports. It mirrors CPython's `scan_once`: a string,
// object or array recurses; the three literals match by prefix; a number is
// matched by the same prefix regex; NaN and Infinity are accepted; anything
// else is "Expecting value" at idx.
func scanOncePy(s []rune, idx int) (int, *jsonSyntaxError) {
	if idx >= len(s) {
		return 0, &jsonSyntaxError{idx, "Expecting value"}
	}
	switch c := s[idx]; {
	case c == '"':
		return scanStringPy(s, idx+1)
	case c == '{':
		return scanObjectPy(s, idx+1)
	case c == '[':
		return scanArrayPy(s, idx+1)
	case c == 'n' && hasLiteralPy(s, idx, "null"):
		return idx + 4, nil
	case c == 't' && hasLiteralPy(s, idx, "true"):
		return idx + 4, nil
	case c == 'f' && hasLiteralPy(s, idx, "false"):
		return idx + 5, nil
	}
	if end, ok := matchNumberPy(s, idx); ok {
		return end, nil
	}
	switch {
	case s[idx] == 'N' && hasLiteralPy(s, idx, "NaN"):
		return idx + 3, nil
	case s[idx] == 'I' && hasLiteralPy(s, idx, "Infinity"):
		return idx + 8, nil
	case s[idx] == '-' && hasLiteralPy(s, idx, "-Infinity"):
		return idx + 9, nil
	}
	return 0, &jsonSyntaxError{idx, "Expecting value"}
}

// scanStringPy scans a JSON string whose body starts at end (the index just
// past the opening quote, which begin names for an unterminated-string
// failure). The positions are CPython's: an unterminated string fails at its
// opening quote, a control character at the character itself, an invalid escape
// at the backslash, and an invalid unicode escape at its 'u'.
func scanStringPy(s []rune, end int) (int, *jsonSyntaxError) {
	begin := end - 1
	for {
		limit := end
		for limit < len(s) && s[limit] != '"' && s[limit] != '\\' && s[limit] >= 0x20 {
			limit++
		}
		if limit == len(s) {
			return 0, &jsonSyntaxError{begin, "Unterminated string starting at"}
		}
		terminator := s[limit]
		end = limit + 1
		switch {
		case terminator == '"':
			return end, nil
		case terminator != '\\':
			return 0, &jsonSyntaxError{limit, "Invalid control character at"}
		}
		// A backslash introduces an escape, which must be a member of the
		// escape table or a \uXXXX sequence.
		if end >= len(s) {
			return 0, &jsonSyntaxError{begin, "Unterminated string starting at"}
		}
		if s[end] != 'u' {
			if !isEscapePy(s[end]) {
				return 0, &jsonSyntaxError{end - 1, "Invalid \\escape"}
			}
			end++
			continue
		}
		unit, ok := decodeUXXXXPy(s, end)
		if !ok {
			return 0, &jsonSyntaxError{end, "Invalid \\uXXXX escape"}
		}
		end += 5
		if unit >= 0xd800 && unit <= 0xdbff && end+1 < len(s) && s[end] == '\\' && s[end+1] == 'u' {
			low, ok := decodeUXXXXPy(s, end+1)
			if !ok {
				return 0, &jsonSyntaxError{end + 1, "Invalid \\uXXXX escape"}
			}
			if low >= 0xdc00 && low <= 0xdfff {
				end += 6
			}
		}
	}
}

// decodeUXXXXPy reads the four hexadecimal digits of a \u escape whose 'u' sits
// at index u, reporting whether they are a valid sequence.
func decodeUXXXXPy(s []rune, u int) (int, bool) {
	if u+4 >= len(s) {
		return 0, false
	}
	value := 0
	for i := u + 1; i <= u+4; i++ {
		digit, ok := hexDigitPy(s[i])
		if !ok {
			return 0, false
		}
		value = value<<4 | digit
	}
	return value, true
}

// hexDigitPy reads one hexadecimal digit, the way CPython's escape decoder does.
func hexDigitPy(c rune) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

// isEscapePy reports whether c is one of the single-character escapes CPython's
// table accepts.
func isEscapePy(c rune) bool {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	}
	return false
}

// scanObjectPy scans a JSON object whose body starts at end (just past '{'),
// mirroring CPython's JSONObject: an empty object is allowed, then a sequence of
// string-key, colon, value, comma-or-close.
func scanObjectPy(s []rune, end int) (int, *jsonSyntaxError) {
	next := runeAtPy(s, end)
	if next != '"' {
		if isSpacePy(next) {
			end = skipSpacePy(s, end)
			next = runeAtPy(s, end)
		}
		if next == '}' {
			return end + 1, nil
		}
		if next != '"' {
			return 0, &jsonSyntaxError{end, "Expecting property name enclosed in double quotes"}
		}
	}
	end++
	for {
		var failure *jsonSyntaxError
		if end, failure = scanStringPy(s, end); failure != nil {
			return 0, failure
		}
		if runeAtPy(s, end) != ':' {
			end = skipSpacePy(s, end)
			if runeAtPy(s, end) != ':' {
				return 0, &jsonSyntaxError{end, "Expecting ':' delimiter"}
			}
		}
		end++
		if end < len(s) && isSpacePy(s[end]) {
			end++
			if end < len(s) && isSpacePy(s[end]) {
				end = skipSpacePy(s, end+1)
			}
		}
		if end, failure = scanOncePy(s, end); failure != nil {
			return 0, failure
		}
		var terminator rune
		if end < len(s) {
			terminator = s[end]
			if isSpacePy(terminator) {
				end = skipSpacePy(s, end+1)
				terminator = runeAtPy(s, end)
			}
		}
		end++
		if terminator == '}' {
			return end, nil
		}
		if terminator != ',' {
			return 0, &jsonSyntaxError{end - 1, "Expecting ',' delimiter"}
		}
		end = skipSpacePy(s, end)
		terminator = runeAtPy(s, end)
		end++
		if terminator != '"' {
			return 0, &jsonSyntaxError{end - 1, "Expecting property name enclosed in double quotes"}
		}
	}
}

// scanArrayPy scans a JSON array whose body starts at end (just past '['),
// mirroring CPython's JSONArray.
func scanArrayPy(s []rune, end int) (int, *jsonSyntaxError) {
	next := runeAtPy(s, end)
	if isSpacePy(next) {
		end = skipSpacePy(s, end+1)
		next = runeAtPy(s, end)
	}
	if next == ']' {
		return end + 1, nil
	}
	for {
		var failure *jsonSyntaxError
		if end, failure = scanOncePy(s, end); failure != nil {
			return 0, failure
		}
		next = runeAtPy(s, end)
		if isSpacePy(next) {
			end = skipSpacePy(s, end+1)
			next = runeAtPy(s, end)
		}
		end++
		if next == ']' {
			return end, nil
		}
		if next != ',' {
			return 0, &jsonSyntaxError{end - 1, "Expecting ',' delimiter"}
		}
		if end < len(s) && isSpacePy(s[end]) {
			end++
			if end < len(s) && isSpacePy(s[end]) {
				end = skipSpacePy(s, end+1)
			}
		}
	}
}

// matchNumberPy matches CPython's number regex as a prefix at idx:
// `-?(?:0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?`. It reports the index just past the
// match and whether one matched at all; a lone minus or a fraction with no
// digit after it does not.
func matchNumberPy(s []rune, idx int) (int, bool) {
	i := idx
	if i < len(s) && s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && s[i] >= '1' && s[i] <= '9':
		i++
		for i < len(s) && isDigitPy(s[i]) {
			i++
		}
	default:
		return 0, false
	}
	if i+1 < len(s) && s[i] == '.' && isDigitPy(s[i+1]) {
		i += 2
		for i < len(s) && isDigitPy(s[i]) {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && isDigitPy(s[j]) {
			j++
			for j < len(s) && isDigitPy(s[j]) {
				j++
			}
			i = j
		}
	}
	return i, true
}

// hasLiteralPy reports whether the literal sits at idx.
func hasLiteralPy(s []rune, idx int, literal string) bool {
	if idx+len(literal) > len(s) {
		return false
	}
	for i, c := range literal {
		if s[idx+i] != c {
			return false
		}
	}
	return true
}

// isSpacePy reports whether c is one of the four whitespace characters CPython's
// scanner skips.
func isSpacePy(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// isDigitPy reports whether c is an ASCII decimal digit, the digits CPython's
// number pattern matches for a JSON document.
func isDigitPy(c rune) bool {
	return c >= '0' && c <= '9'
}

// skipSpacePy returns the first index at or after i whose character is not
// whitespace.
func skipSpacePy(s []rune, i int) int {
	for i < len(s) && isSpacePy(s[i]) {
		i++
	}
	return i
}

// runeAtPy reads the character at i, or 0 when i is past the end. CPython's
// scanner treats a missing character as a value that matches neither a
// terminator nor a comma, which a zero rune does here as well.
func runeAtPy(s []rune, i int) rune {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

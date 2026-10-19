package directives

import (
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// The list route's query selectors follow the reference's request framework
// rather than Go's defaults: a scalar parameter repeated takes its last
// occurrence, a parameter present with no value is still validated, and the
// value's own type coercion is the framework's — its boolean word list and its
// integer grammar, which reads whitespace, underscores and a zero fraction.

// queryParam reads a scalar query parameter the way the reference's framework
// does: when a name is repeated the last occurrence wins, and a name that is
// present but carries no value reads as the empty string rather than as an
// absent parameter — the framework still runs the field's validator on it.
func queryParam(r *http.Request, name string) (string, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return values[len(values)-1], true
}

// parseActiveOnly reads the active_only selector, defaulting to true when the
// parameter is absent as the reference does. A present value goes through the
// framework's boolean coercion, which accepts a fixed word list
// case-insensitively and never trims: "Yes" is a boolean and " yes" is not.
func parseActiveOnly(r *http.Request) (bool, *httpx.ValidationFailure) {
	raw, present := queryParam(r, "active_only")
	if !present {
		return true, nil
	}
	if parsed, ok := boolWord(raw); ok {
		return parsed, nil
	}
	return false, queryItem(httpx.TypeMismatch("bool_parsing", []string{"query", "active_only"}, raw))
}

// boolWord reads the words the reference's boolean coercion accepts for a query
// parameter. The comparison is case-insensitive and the value is not trimmed,
// which is what the framework does.
func boolWord(text string) (bool, bool) {
	switch strings.ToLower(text) {
	case "true", "t", "yes", "y", "on", "1":
		return true, true
	case "false", "f", "no", "n", "off", "0":
		return false, true
	}
	return false, false
}

// parsePage reads limit and offset, validating them against the reference's
// query bounds. Both default when absent; a present value must read as an
// integer inside its bounds.
func parsePage(r *http.Request) (int, int, *httpx.ValidationFailure) {
	limit := defaultListLimit
	if raw, present := queryParam(r, "limit"); present {
		value, failure := boundedQueryInt(raw, "limit", 1, maxListLimit)
		if failure != nil {
			return 0, 0, failure
		}
		limit = value
	}
	offset := 0
	if raw, present := queryParam(r, "offset"); present {
		value, failure := boundedQueryInt(raw, "offset", 0, -1)
		if failure != nil {
			return 0, 0, failure
		}
		offset = value
	}
	return limit, offset, nil
}

// boundedQueryInt parses an integer query parameter the way the reference's
// integer coercion does — surrounding whitespace, an optional sign, underscores
// between digits and a fractional part only when every digit is zero — and
// reports the framework's item for a value it cannot read or one outside
// [min,max]. A max below min means no upper bound; a value too large for int64
// is then the storage layer's failure rather than a validation one, and is
// reported as the internal failure the reference turns it into. The item's
// input is the raw query string, as the framework reports it, not the parsed
// number.
func boundedQueryInt(raw, name string, min, max int) (int, *httpx.ValidationFailure) {
	loc := []string{"query", name}
	value, ok := httpx.LaxInteger(raw)
	if !ok {
		// A valid Python integer too large for int64 is still parsed losslessly
		// by the framework, so only its sign decides which bound it is outside
		// of; anything else is a value the parser refuses.
		sign, valid := pythonIntSign(raw)
		if !valid {
			return 0, queryItem(httpx.TypeMismatch("int_parsing", loc, raw))
		}
		if sign < 0 {
			return 0, queryItem(httpx.GreaterThanEqual(loc, raw, min))
		}
		if max < min {
			// No upper bound: the framework hands the value straight to the
			// storage layer, which cannot carry it and fails. The reference
			// answers that failure with a 500, not a validation item, so the
			// unrepresentable value is reported as the internal failure it is
			// there — the shared envelope's own-status form, since the caller
			// only ever reports a validation failure through it.
			return 0, &httpx.ValidationFailure{Status: httpx.KindInternal, Detail: httpx.DetailInternal}
		}
		return 0, queryItem(httpx.LessThanEqual(loc, raw, max))
	}
	if value < min {
		return 0, queryItem(httpx.GreaterThanEqual(loc, raw, min))
	}
	if max >= min && value > max {
		return 0, queryItem(httpx.LessThanEqual(loc, raw, max))
	}
	return value, nil
}

// pythonIntSign reports whether text is an integer literal Python's int()
// accepts but that does not fit int64, and the literal's sign (+1 or -1). It
// mirrors the parser's grammar: surrounding whitespace, an optional sign,
// digits with single underscores between them, and a fractional part only when
// every one of its digits is zero.
func pythonIntSign(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	sign := 1
	switch {
	case strings.HasPrefix(trimmed, "+"):
		trimmed = trimmed[1:]
	case strings.HasPrefix(trimmed, "-"):
		sign = -1
		trimmed = trimmed[1:]
	}
	whole := trimmed
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		whole = trimmed[:dot]
		fraction := trimmed[dot+1:]
		if fraction == "" {
			return 0, false
		}
		for i := range len(fraction) {
			if fraction[i] != '0' {
				return 0, false
			}
		}
	}
	if whole == "" {
		return 0, false
	}
	digits := 0
	for i := range len(whole) {
		switch {
		case whole[i] >= '0' && whole[i] <= '9':
			digits++
		case whole[i] == '_':
			if digits == 0 || i+1 == len(whole) || whole[i+1] == '_' {
				return 0, false
			}
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	return sign, true
}

package reporting

import (
	"math/big"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// observationScopeQuery is one declared query parameter of the observation
// scopes listing, with the default and bounds the reference declares for it.
type observationScopeQuery struct {
	name string
	def  int
	min  int
	max  int // a negative max means unbounded above
}

// observationScopeQueries are the endpoint's typed Query declarations in the
// order the reference declares them (`limit` then `offset`). The framework
// reports one item per rejected parameter in declaration order, so that order
// is part of the envelope.
var observationScopeQueries = []observationScopeQuery{
	{name: "limit", def: 100, min: 0, max: 1000},
	{name: "offset", def: 0, min: 0, max: -1},
}

// parseObservationScopeQuery validates the listing's typed query parameters the
// way the reference's request model does, and returns their bound values.
//
// Every rejected parameter contributes an item, collected in declaration order,
// so `limit=-1&offset=-1` answers with both items rather than only the first:
// the framework validates the whole model before the route body runs, and this
// handler only ever sees requests that passed it.
func parseObservationScopeQuery(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	var items []httpx.ValidationError
	bound := make([]int, len(observationScopeQueries))
	for i, param := range observationScopeQueries {
		value, item := observationScopeQueryValue(r, param)
		if item != nil {
			items = append(items, *item)
			continue
		}
		bound[i] = value
	}
	if len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return 0, 0, false
	}
	return bound[0], bound[1], true
}

// observationScopeQueryValue reads one typed integer query parameter. An absent
// parameter takes its default; a present one is parsed from its raw text the way
// the framework's string-to-integer conversion does, because "present but empty"
// and "absent" are different requests (`?limit=` is a 422, never the default).
// It returns the framework's item for a value it rejects.
func observationScopeQueryValue(r *http.Request, param observationScopeQuery) (int, *httpx.ValidationError) {
	loc := []string{"query", param.name}
	values, present := httpx.QueryPresent(r, param.name)
	if !present || len(values) == 0 {
		return param.def, nil
	}
	// A repeated scalar binds its LAST occurrence, as the reference's request
	// model reads a multimap.
	raw := values[len(values)-1]
	value, ok := frameworkIntFromString(raw)
	if !ok {
		item := httpx.TypeMismatch("int_parsing", loc, raw)
		return 0, &item
	}
	// Python integers are unbounded, so the bounds are compared before any
	// narrowing: a thousand-digit value is "too large", never "not an integer".
	if value.Cmp(big.NewInt(int64(param.min))) < 0 {
		item := httpx.GreaterThanEqual(loc, raw, param.min)
		return 0, &item
	}
	if param.max >= 0 && value.Cmp(big.NewInt(int64(param.max))) > 0 {
		item := httpx.LessThanEqual(loc, raw, param.max)
		return 0, &item
	}
	return int(value.Int64()), nil
}

// frameworkIntFromString converts a query string to an integer the way the
// reference's framework does when it coerces a string into a declared `int`.
// The conversion is not strconv.Atoi:
//
//   - the text is stripped of whitespace on both ends first, Python's
//     `str.strip()` semantics, so " 5", "5\t" and "5\u00a0" are all 5;
//   - an integer literal is an optional sign and ASCII digits with single
//     underscores allowed *between* digits ("1_000" is 1000, "1__0" is not);
//   - failing that, a decimal float whose fractional part is all zeros is
//     accepted, which is why "5.0" and "5_0.0" are integers while "5.5", "5."
//     and ".5" are not.
//
// Anything else — hex, exponents, non-ASCII digits — reports false. The value
// is returned unbounded, because the framework's model validates against the
// declared bounds before anything narrows it.
func frameworkIntFromString(text string) (*big.Int, bool) {
	stripped := pythonStrip(text)
	if stripped == "" {
		return nil, false
	}
	negative := stripped[0] == '-'
	body := stripped
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if digits, ok := pythonIntDigits(body, negative); ok {
		return digits, true
	}
	whole, fraction, found := strings.Cut(body, ".")
	if !found {
		return nil, false
	}
	// Both sides must be present and well formed: float("5.") and float(".5")
	// parse in Python, but the framework's integer check then refuses a
	// non-zero fractional part, and an empty one is not zero.
	if fraction == "" || strings.Trim(fraction, "0") != "" {
		return nil, false
	}
	return pythonIntDigits(whole, negative)
}

// pythonIntDigits converts an ASCII digit run with single underscores between
// digits to its value, applying the sign. Leading and trailing underscores and
// doubled ones are refused, as Python's `int()` refuses them. The value is
// unbounded (Python integers are), so a caller can compare it with its declared
// bounds before it is ever narrowed.
func pythonIntDigits(digits string, negative bool) (*big.Int, bool) {
	if digits == "" || digits[0] == '_' || digits[len(digits)-1] == '_' || strings.Contains(digits, "__") {
		return nil, false
	}
	cleaned := make([]byte, 0, len(digits))
	for i := 0; i < len(digits); i++ {
		switch c := digits[i]; {
		case c >= '0' && c <= '9':
			cleaned = append(cleaned, c)
		case c == '_':
		default:
			return nil, false
		}
	}
	value, ok := new(big.Int).SetString(string(cleaned), 10)
	if !ok {
		return nil, false
	}
	if negative {
		value.Neg(value)
	}
	return value, true
}

// pythonStrip trims the characters Python's argument-less `str.strip()` trims:
// whitespace, the Unicode spaces included (U+00A0 and U+3000 among them). Go's
// strings.TrimSpace already covers most of them; the extra runes here are the
// ones it does not treat as space.
func pythonStrip(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', '\u0085', '\u00a0':
			return true
		}
		// The Unicode space separators Python's str.isspace() reports and Go's
		// unicode.IsSpace also reports: U+2000–U+200A, U+2028, U+2029, U+202F,
		// U+205F and U+3000. The rest of the 2000-block is punctuation (the
		// em dash U+2014, say), which neither trims.
		return r >= 0x2000 && r <= 0x200b || r == 0x2028 || r == 0x2029 ||
			r == 0x202f || r == 0x205f || r == 0x3000
	})
}

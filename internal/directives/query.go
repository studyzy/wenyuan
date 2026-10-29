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

// listSelectors is the list route's resolved query selectors.
type listSelectors struct {
	activeOnly bool
	mode       string
	limit      int
	offset     int
}

// parseListSelectors reads every query selector the list route declares and
// returns the resolved values alongside the items the framework would reject.
// Each selector is validated whether or not an earlier one failed, because the
// reference's framework validates the whole request in one pass; the items come
// back in declaration order, which is the order the endpoint's parameters have.
func parseListSelectors(r *http.Request) (listSelectors, []httpx.ValidationError, *httpx.ValidationFailure) {
	resolved := listSelectors{activeOnly: true, mode: "any", limit: defaultListLimit}
	var items []httpx.ValidationError
	if raw, present := queryParam(r, "tags_match"); present {
		// A literal's members compare by exact string: the framework does not
		// trim, so " any" is no more a member than "bogus" is.
		if !containsString(directiveTagsMatch, raw) {
			items = append(items, httpx.Literal(
				[]string{"query", "tags_match"}, raw, directiveTagsMatch))
		} else {
			resolved.mode = raw
		}
	}
	if raw, present := queryParam(r, "active_only"); present {
		if parsed, ok := boolWord(raw); ok {
			resolved.activeOnly = parsed
		} else {
			items = append(items, httpx.TypeMismatch("bool_parsing", []string{"query", "active_only"}, raw))
		}
	}
	if raw, present := queryParam(r, "limit"); present {
		if value, failure := boundedQueryInt(raw, "limit", 1, maxListLimit); failure != nil {
			items = append(items, failure.Errors...)
		} else {
			resolved.limit = value
		}
	}
	if raw, present := queryParam(r, "offset"); present {
		if value, failure := boundedQueryInt(raw, "offset", 0, -1); failure != nil {
			// An integer past int64 has no bound to violate: the framework hands it
			// to the storage layer, which cannot carry it, and the reference answers
			// that failure with a 500 rather than an item. It therefore cannot be
			// collected alongside other items — it is the whole answer, and the
			// items gathered so far go with it. An ordinary bound violation is one
			// item among the rest.
			if len(failure.Errors) == 0 {
				return resolved, nil, failure
			}
			items = append(items, failure.Errors...)
		} else {
			resolved.offset = value
		}
	}
	return resolved, items, nil
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
		sign, valid := httpx.BigintSign(raw)
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

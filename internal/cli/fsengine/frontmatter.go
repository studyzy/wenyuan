package fsengine

import (
	"encoding/json"
	"strconv"
	"strings"
)

// FMField is one front-matter entry; Value is a string, int, []string or
// nil, serialized by FrontMatter in insertion order.
type FMField struct {
	Key   string
	Value any
}

// FrontMatter renders the YAML front-matter block: an opening `---`, one
// `key: value` line per field, and a closing `---` without a trailing
// newline; callers append blank lines and body text as needed.
func FrontMatter(fields []FMField) string {
	var b strings.Builder
	b.WriteString("---\n")
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(f.Key)
		b.WriteString(": ")
		b.WriteString(fmValue(f.Value))
	}
	b.WriteString("\n---")
	return b.String()
}

// fmValue renders one field value: scalars plain or quoted, lists in flow
// style, numbers in decimal, and null as the literal null.
func fmValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return fmScalar(x)
	case int:
		return strconv.Itoa(x)
	case []string:
		if len(x) == 0 {
			return "[]"
		}
		items := make([]string, len(x))
		for i, s := range x {
			items[i] = fmScalar(s)
		}
		return "[" + strings.Join(items, ", ") + "]"
	default:
		return fmScalar(jsonString(v))
	}
}

// jsonString renders any value as its JSON string form, a safe fallback.
func jsonString(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// fmScalar emits a string bare when it is plain-safe and double-quoted with
// JSON escaping otherwise, which is a valid YAML subset.
func fmScalar(s string) string {
	if fmPlainSafe(s) {
		return s
	}
	return jsonString(s)
}

// fmPlainSafe decides whether a string may be emitted without quotes. A
// quoted value parses as a string; a plain one could be read as a bool,
// number or null, so the predicate must match the reference exactly.
func fmPlainSafe(s string) bool {
	if s == "" || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		switch r {
		case ':', '#', '[', ']', '{', '}', '&', '*', '!', '|', '>', '\'',
			'"', '%', '@', '`', ',', '\n', '\r', '\t':
			return false
		}
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "?") {
		return false
	}
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return false
	}
	return !fmLooksNumeric(s)
}

// fmLooksNumeric reports whether the string starts like a number: an
// optional sign followed by a digit.
func fmLooksNumeric(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	return i < len(s) && s[i] >= '0' && s[i] <= '9'
}

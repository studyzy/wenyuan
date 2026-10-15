package objectstore

import (
	"encoding/hex"
	"sort"
	"strings"
)

// unreserved reports whether a byte survives URI-escaping unchanged under the
// SigV4 rules: the alphanumerics and "-", "_", ".", "~". Everything else is
// percent-encoded, including "/" when the caller is escaping a value rather
// than a path.
func unreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c == '.', c == '~':
		return true
	default:
		return false
	}
}

// uriEscape percent-encodes a value for a signed query string with the SigV4
// rules: every byte outside the unreserved set becomes %XX with uppercase hex,
// and a space is %20 rather than the "+" net/url would produce. encodeSlash
// keeps "/" as a literal separator, which a resource path needs and a query
// value does not.
func uriEscape(value string, encodeSlash bool) string {
	var out strings.Builder
	for i := range len(value) {
		c := value[i]
		if unreserved(c) || (c == '/' && !encodeSlash) {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
	}
	return out.String()
}

// queryParam is one name/value pair of a signed query string.
type queryParam struct{ name, value string }

// canonicalQueryString renders sorted, escaped name=value pairs for a
// signature. Names and values are escaped for a query (a "/" inside a value
// becomes %2F), which is what the canonical query of a presigned URL covers.
func canonicalQueryString(params []queryParam) string {
	sorted := make([]queryParam, len(params))
	copy(sorted, params)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	parts := make([]string, 0, len(sorted))
	for _, param := range sorted {
		parts = append(parts, uriEscape(param.name, true)+"="+uriEscape(param.value, true))
	}
	return strings.Join(parts, "&")
}

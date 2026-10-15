package storage

import (
	"net/url"
	"strings"
)

// postgresSchemes are the spellings a PostgreSQL URL may carry. The asyncpg
// variants are what the reference's own engine uses, so an operator copying a
// deployment's URL here would otherwise be handing the driver a scheme it does
// not know.
var postgresSchemes = map[string]bool{
	"postgres":           true,
	"postgresql":         true,
	"postgres+asyncpg":   true,
	"postgresql+asyncpg": true,
}

// NormalizeDatabaseURL rewrites a PostgreSQL URL into the form a libpq-style
// consumer expects.
//
// Two transformations, both of which the reference performs:
//   - a SQLAlchemy-style scheme (`postgresql+asyncpg`, `postgres+asyncpg`,
//     `postgres`) becomes `postgresql`;
//   - the asyncpg dialect's `ssl=<mode>` query parameter becomes libpq's
//     `sslmode=<mode>`.
//
// It is idempotent, and a URL that is not a PostgreSQL URL (an embedded marker,
// another scheme, or an empty string) is returned unchanged.
func NormalizeDatabaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.Contains(trimmed, "://") {
		return raw
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		// An unparseable URL is handed on unchanged: the driver's own error
		// says more about what is wrong with it than a rewrite would.
		return raw
	}
	if !postgresSchemes[strings.ToLower(parsed.Scheme)] {
		return raw
	}
	if parsed.Scheme == "postgresql" && !strings.Contains(parsed.RawQuery, "ssl=") {
		return raw
	}
	parsed.Scheme = "postgresql"
	if parsed.RawQuery != "" {
		parsed.RawQuery = rewriteSSLParameter(parsed.RawQuery)
	}
	return parsed.String()
}

// rewriteSSLParameter renames the `ssl` query key to `sslmode`, preserving the
// other parameters and their order.
func rewriteSSLParameter(rawQuery string) string {
	segments := strings.Split(rawQuery, "&")
	for index, segment := range segments {
		key, value, found := strings.Cut(segment, "=")
		if !found || key != "ssl" {
			continue
		}
		segments[index] = "sslmode=" + value
	}
	return strings.Join(segments, "&")
}

// Package timefmt renders the timestamp strings the reference implementation
// emits on the wire.
//
// It is a leaf package with no internal dependencies, so the storage layer —
// which cannot import internal/httpx because httpx pulls in the audit logger,
// which imports storage — can render archive timestamps through the same
// implementation the HTTP surfaces use. Every other package should reach these
// through internal/httpx, which re-exports them as its shared formatters.
//
// Three shapes appear in the reference, and which one a field carries depends
// on how the reference produced it:
//
//   - FormatTimestamp: Python datetime.isoformat() — the dominant HTTP form.
//   - FormatPostgresTimestamp: a timestamptz selected as text in SQL.
//   - FormatZuluTimestamp: a pydantic model's JSON output.
package timefmt

import (
	"strconv"
	"strings"
	"time"
)

// FormatTimestamp renders a time the way the reference reports almost every
// response timestamp: Python's datetime.isoformat() on an aware UTC value.
//
//	2026-10-16T15:02:10.622005+00:00
//	2026-10-16T15:02:10+00:00        (fraction dropped when the microsecond is 0)
//
// Three details are observable on the wire and therefore fixed here: the 'T'
// separator, the '+00:00' offset (never 'Z'), and a fractional part that is
// present exactly when the microsecond is non-zero — always six digits, never
// trailing-zero trimmed. The value is truncated to microseconds first, the
// resolution PostgreSQL and Python both carry.
//
// A zero time renders as the empty string, for the callers whose DTO declares a
// non-optional string; a genuinely absent column is handled by
// FormatTimestampPtr so it stays null.
func FormatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatPythonISO(t)
}

// FormatTimestampPtr is FormatTimestamp for an optional time. A nil pointer and
// the zero time both render as nil, so a column that is NULL on the wire stays
// JSON null rather than becoming "".
func FormatTimestampPtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	formatted := formatPythonISO(*t)
	return &formatted
}

// FormatPostgresTimestamp renders a time the way PostgreSQL prints a
// timestamptz selected as text (created_at::text):
//
//	2026-10-16 15:26:20.390069+00
//	2026-10-16 15:26:20+00           (fraction dropped, and no colon in the offset)
//
// This is the exception rather than the rule. The reference's webhook reads cast
// the timestamp columns to text in SQL and hand the resulting string straight to
// the response, bypassing Python's isoformat entirely, so those routes emit this
// space-separated form with a bare '+00' offset. Unlike isoformat, PostgreSQL
// trims trailing zeros from the fractional part and omits it when the
// microsecond is zero.
func FormatPostgresTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatPostgresText(t)
}

// FormatZuluTimestamp renders a time the way the reference's pydantic models
// serialize a datetime in their JSON output:
//
//	2026-10-16T15:26:20.622005Z
//	2026-10-16T15:26:20Z           (fraction dropped when the microsecond is 0)
//
// It differs from FormatTimestamp in the offset spelling ('Z', not '+00:00'),
// because it is produced by a different serializer: pydantic's JSON encoder
// rather than Python's datetime.isoformat(). Like isoformat — and unlike Go's
// encoding/json, which trims trailing zeros — it keeps all six fractional
// digits whenever the microsecond is non-zero. The archive sections written by
// pydantic (documents, observations, manifest, knowledge pages) and the
// outbound webhook event body carry this form.
func FormatZuluTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatZulu(t)
}

// FormatZuluTimestampPtr is FormatZuluTimestamp for an optional time.
func FormatZuluTimestampPtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	formatted := formatZulu(*t)
	return &formatted
}

// formatPythonISO is the shared body of FormatTimestamp, without the zero-value
// guard so pointer callers do not pay for it twice.
func formatPythonISO(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05") + "+00:00"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "+00:00"
}

// formatZulu is the shared body of FormatZuluTimestamp.
func formatZulu(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05") + "Z"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "Z"
}

// formatPostgresText mirrors PostgreSQL's timestamptz text output.
func formatPostgresText(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	base := t.Format("2006-01-02 15:04:05")
	micro := t.Nanosecond() / 1000
	if micro == 0 {
		return base + "+00"
	}
	frac := strings.TrimRight(strconv.Itoa(micro + 1000000)[1:], "0")
	return base + "." + frac + "+00"
}

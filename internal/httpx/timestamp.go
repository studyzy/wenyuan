package httpx

import (
	"time"

	"github.com/studyzy/wenyuan/internal/timefmt"
)

// The shared timestamp formatters for every HTTP response body. They live here
// so a response DTO reaches for httpx.FormatTimestamp rather than re-deriving
// the reference's wire shape; the implementation is in internal/timefmt, a leaf
// package the storage layer can also reach (it cannot import httpx, because
// httpx imports the audit logger, which imports storage).
//
// Which formatter a field uses is decided by how the reference produced that
// field, never by convenience:
//
//   - FormatTimestamp — Python datetime.isoformat() on an aware UTC value:
//     2026-10-16T15:02:10.622005+00:00 (or ...T15:02:10+00:00 when the
//     microsecond is zero). This is the dominant form: bank, mental-model,
//     directive, document, chunk, entity, memory, operation, audit, LLM-request
//     and statistics fields all carry it.
//   - FormatPostgresTimestamp — a timestamptz column selected as text in SQL
//     (created_at::text): 2026-10-16 15:26:20.390069+00, with a space separator
//     and a bare '+00'. The reference uses it only where its SQL casts the
//     column, which is the webhook family (webhook created_at/updated_at, and a
//     delivery's next_retry_at/created_at/updated_at, including the delivery
//     pagination cursor).
//   - FormatZuluTimestamp — a pydantic model's JSON output: 2026-10-16T15:26:20.622005Z.
//     The outbound webhook event body and the archive sections written by
//     pydantic use it.
//
// The zero time renders as "" (or nil for the pointer forms), so a nullable
// column stays JSON null rather than becoming a placeholder date.

// FormatTimestamp see timefmt.FormatTimestamp.
func FormatTimestamp(t time.Time) string { return timefmt.FormatTimestamp(t) }

// FormatTimestampPtr see timefmt.FormatTimestampPtr.
func FormatTimestampPtr(t *time.Time) *string { return timefmt.FormatTimestampPtr(t) }

// FormatPostgresTimestamp see timefmt.FormatPostgresTimestamp.
func FormatPostgresTimestamp(t time.Time) string { return timefmt.FormatPostgresTimestamp(t) }

// FormatZuluTimestamp see timefmt.FormatZuluTimestamp.
func FormatZuluTimestamp(t time.Time) string { return timefmt.FormatZuluTimestamp(t) }

// FormatZuluTimestampPtr see timefmt.FormatZuluTimestampPtr.
func FormatZuluTimestampPtr(t *time.Time) *string { return timefmt.FormatZuluTimestampPtr(t) }

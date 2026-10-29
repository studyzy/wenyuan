package transferarchive

import (
	"testing"
	"time"
)

// An archive from a bank that stored an open bound as infinity carries it as
// year 1 / year 9999; the reference's schema validators drop those values on
// import rather than recreate the row that breaks recall
// (engine/transfer/schema.py:176-177, :225-226 via engine/stored_dates.py).
func TestParseTimeDropsPlaceholderDates(t *testing.T) {
	for _, value := range []string{
		"0001-01-01T00:00:00Z",
		"9999-12-31T23:59:59.999999Z",
	} {
		if parsed := ParseTime(&value); parsed != nil {
			t.Fatalf("ParseTime(%q) = %v, want nil for a placeholder bound", value, parsed)
		}
	}
	value := "2026-10-16T15:26:20.622005Z"
	parsed := ParseTime(&value)
	if parsed == nil || parsed.Year() != 2026 || parsed.Location() != time.UTC {
		t.Fatalf("ParseTime(%q) = %v, want the 2026 instant in UTC", value, parsed)
	}
	if ParseTime(nil) != nil {
		t.Fatal("ParseTime(nil) must read as nil")
	}
}

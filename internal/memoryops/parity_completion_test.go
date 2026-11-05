package memoryops

import (
	"testing"
	"time"
)

// A named day seals at 23:59:59.999999, so a timestamp in the last microsecond
// of the day still falls inside the window.
func TestDayRangeSealsTheLastMicrosecond(t *testing.T) {
	day := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	window := dayRange(day, day)
	want := time.Date(2026, 3, 5, 23, 59, 59, 999999000, time.UTC)
	if !window.End.Equal(want) {
		t.Fatalf("end = %s, want %s", window.End, want)
	}
}

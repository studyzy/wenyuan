package timefmt

import (
	"testing"
	"time"
)

func at(micro int) time.Time {
	return time.Date(2026, 10, 16, 15, 2, 10, micro*1000, time.UTC)
}

func TestFormatTimestampMatchesPythonIsoformat(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"microseconds kept", at(622005), "2026-10-16T15:02:10.622005+00:00"},
		{"trailing zeros kept", at(390000), "2026-10-16T15:02:10.390000+00:00"},
		{"fraction dropped when zero", at(0), "2026-10-16T15:02:10+00:00"},
		{"zero time is empty", time.Time{}, ""},
	}
	for _, tc := range cases {
		if got := FormatTimestamp(tc.in); got != tc.want {
			t.Errorf("%s: FormatTimestamp = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFormatTimestampNormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("plus2", 2*60*60)
	// 17:02:10+02:00 is 15:02:10Z; the reference's values are always UTC-aware.
	in := time.Date(2026, 10, 16, 17, 2, 10, 622005000, zone)
	if got, want := FormatTimestamp(in), "2026-10-16T15:02:10.622005+00:00"; got != want {
		t.Errorf("FormatTimestamp = %q, want %q", got, want)
	}
}

func TestFormatTimestampPtr(t *testing.T) {
	if got := FormatTimestampPtr(nil); got != nil {
		t.Errorf("nil pointer = %v, want nil", *got)
	}
	zero := time.Time{}
	if got := FormatTimestampPtr(&zero); got != nil {
		t.Errorf("zero time = %v, want nil", *got)
	}
	in := at(1)
	if got := FormatTimestampPtr(&in); got == nil || *got != "2026-10-16T15:02:10.000001+00:00" {
		t.Errorf("FormatTimestampPtr = %v", got)
	}
}

func TestFormatPostgresTimestamp(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"microseconds kept", at(390069), "2026-10-16 15:02:10.390069+00"},
		{"trailing zeros trimmed", at(390000), "2026-10-16 15:02:10.39+00"},
		{"fraction dropped when zero", at(0), "2026-10-16 15:02:10+00"},
		{"zero time is empty", time.Time{}, ""},
	}
	for _, tc := range cases {
		if got := FormatPostgresTimestamp(tc.in); got != tc.want {
			t.Errorf("%s: FormatPostgresTimestamp = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFormatZuluTimestamp(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"microseconds kept", at(622005), "2026-10-16T15:02:10.622005Z"},
		{"trailing zeros kept", at(390000), "2026-10-16T15:02:10.390000Z"},
		{"fraction dropped when zero", at(0), "2026-10-16T15:02:10Z"},
		{"zero time is empty", time.Time{}, ""},
	}
	for _, tc := range cases {
		if got := FormatZuluTimestamp(tc.in); got != tc.want {
			t.Errorf("%s: FormatZuluTimestamp = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := FormatZuluTimestampPtr(nil); got != nil {
		t.Errorf("nil pointer = %v, want nil", *got)
	}
}

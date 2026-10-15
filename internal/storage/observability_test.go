package storage

import (
	"context"
	"testing"
	"time"
)

// The series windows are the reference's own: each period name covers a specific
// number of buckets of a specific width, an unknown name resolves to 7d rather
// than being rejected, and a row older than the window is not counted.
func TestMemoriesTimeseriesWindows(t *testing.T) {
	cases := []struct {
		period string
		trunc  string
		step   time.Duration
		count  int
	}{
		{"1h", "minute", time.Minute, 60},
		{"12h", "hour", time.Hour, 12},
		{"1d", "hour", time.Hour, 24},
		{"7d", "day", 24 * time.Hour, 7},
		{"30d", "day", 24 * time.Hour, 30},
		{"90d", "day", 24 * time.Hour, 90},
		{"nonsense", "day", 24 * time.Hour, 7},
	}
	for _, tc := range cases {
		window := ResolveTimeseriesWindow(tc.period)
		if window.Trunc != tc.trunc || window.Step != tc.step || window.Count != tc.count {
			t.Fatalf("%s resolved to %+v, want trunc=%s step=%s count=%d", tc.period, window, tc.trunc, tc.step, tc.count)
		}
	}
	if got := ResolveTimeseriesTimeField("occurred_start"); got != "occurred_start" {
		t.Fatalf("occurred_start = %q", got)
	}
	if got := ResolveTimeseriesTimeField("nonsense"); got != "created_at" {
		t.Fatalf("an unknown axis must resolve to created_at, got %q", got)
	}

	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "ts-bank", Name: "ts-bank"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	occurred := now.Add(-2 * time.Minute)
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: "unit-now", BankID: "ts-bank", Text: "recent", FactType: TypeWorld,
		OccurredStart: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: "unit-old", BankID: "ts-bank", Text: "old", FactType: TypeWorld,
		OccurredStart: &occurred,
	}); err != nil {
		t.Fatal(err)
	}
	series, err := store.MemoriesTimeseries(ctx, "ts-bank", "1h", "occurred_start")
	if err != nil {
		t.Fatal(err)
	}
	if series.Trunc != "minute" || series.Period != "1h" || series.TimeField != "occurred_start" {
		t.Fatalf("series = %+v", series)
	}
	// Two units two minutes apart fall in two minute-wide buckets.
	if len(series.Buckets) != 2 {
		t.Fatalf("buckets = %d, want one per minute: %+v", len(series.Buckets), series.Buckets)
	}
	if !series.Buckets[0].Time.Before(series.Buckets[1].Time) {
		t.Fatalf("buckets must be ordered: %+v", series.Buckets)
	}
	// A day-wide period drops nothing that a minute-wide one kept, and the stale
	// row still counts: it is inside a 7-day window.
	if series, err := store.MemoriesTimeseries(ctx, "ts-bank", "7d", "occurred_start"); err != nil {
		t.Fatal(err)
	} else if len(series.Buckets) != 1 {
		t.Fatalf("a 7d window bucketed by day must hold one bucket, got %+v", series.Buckets)
	}
}

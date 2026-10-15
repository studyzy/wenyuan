package storage

import (
	"testing"
	"time"
)

// The three decay shapes: linear reaches its floor at the window, exponential is
// exactly neutral at the half-life, and none is neutral everywhere.
func TestRecencyDecayShapes(t *testing.T) {
	linear := RecencyDecayConfig{Function: RecencyDecayLinear, LinearWindowDays: 100}
	if got := RecencyDecay(0, linear); got != 1 {
		t.Fatalf("linear at zero age = %v, want 1", got)
	}
	if got := RecencyDecay(365, RecencyDecayConfig{Function: RecencyDecayLinear}); got != 0.1 {
		t.Fatalf("linear at its default window = %v, want the 0.1 floor", got)
	}
	if got := RecencyDecay(200, linear); got != 0.1 {
		t.Fatalf("linear past its window = %v, want the floor", got)
	}

	exponential := RecencyDecayConfig{Function: RecencyDecayExponential, HalflifeDays: 90}
	if got := RecencyDecay(90, exponential); got != 0.5 {
		t.Fatalf("exponential at the half-life = %v, want the neutral 0.5", got)
	}
	if got := RecencyDecay(180, exponential); got != 0.25 {
		t.Fatalf("exponential at two half-lives = %v, want 0.25", got)
	}

	if got := RecencyDecay(1000, RecencyDecayConfig{Function: RecencyDecayNone}); got != 0.5 {
		t.Fatalf("none = %v, want neutral", got)
	}
}

// A future-dated memory is never penalised for being ahead of the clock.
func TestRecencyDecayClampsFutureDates(t *testing.T) {
	for _, function := range []string{RecencyDecayLinear, RecencyDecayExponential} {
		if got := RecencyDecay(-5, RecencyDecayConfig{Function: function}); got != 1 {
			t.Fatalf("%s with a future date = %v, want 1", function, got)
		}
	}
}

// A span covering exactly one calendar month or year is a coarse date; a longer
// or shorter one is a genuine interval.
func TestSpansCalendarPeriod(t *testing.T) {
	year := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	if !SpansCalendarPeriod(year, time.Date(2015, 12, 31, 23, 59, 59, 0, time.UTC)) {
		t.Fatal("a full year is a coarse date")
	}
	if !SpansCalendarPeriod(year, time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("the exclusive first instant of the next year is a coarse date")
	}
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !SpansCalendarPeriod(march, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a full month is a coarse date")
	}
	// A genuine interval: an employment that really did span its range.
	if SpansCalendarPeriod(time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2018, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a multi-year span is an interval, not a coarse date")
	}
	if SpansCalendarPeriod(march, march) {
		t.Fatal("a zero-length span is not a period")
	}
	// Forty-five days is neither a month nor a year.
	if SpansCalendarPeriod(march, march.AddDate(0, 0, 45)) {
		t.Fatal("forty-five days is not a calendar period")
	}
}

// A coarse date ages from the end of its period and never reads as fresher than
// neutral; a genuine interval still ages from its start.
func TestRecencyForUnitCoarseDates(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	metrics := RecencyDecayConfig{Function: RecencyDecayLinear, LinearWindowDays: 365}

	// "the 2026 summit", recorded as the whole year: aged from Dec 31, not Jan 1.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	fromEnd := min(0.5, RecencyDecay(now.Sub(end).Hours()/24, metrics))
	if got := RecencyForUnit(&start, &end, nil, now, metrics); got != fromEnd {
		t.Fatalf("coarse date = %v, want the capped end-of-period signal %v", got, fromEnd)
	}
	// Aging from the period end is strictly kinder than from its start, which is
	// the point: "the 2026 summit" must not read as eight months stale.
	fromStart := RecencyDecay(now.Sub(start).Hours()/24, metrics)
	if fromStart >= fromEnd {
		t.Fatalf("aging from the period end must not look staler: end %v start %v", fromEnd, fromStart)
	}

	// A period still in progress ends in the future, which would clamp to 1;
	// the coarse-date cap holds it at neutral instead.
	inProgressStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inProgressEnd := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	future := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := RecencyForUnit(&inProgressStart, &inProgressEnd, nil, future, metrics); got > 0.5 {
		t.Fatalf("a period in progress = %v, want no more than neutral", got)
	}

	// A genuine interval ages from its start.
	intervalStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	intervalEnd := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := RecencyForUnit(&intervalStart, &intervalEnd, nil, now, metrics); got != 0.1 {
		t.Fatalf("an interval old enough to floor = %v, want 0.1", got)
	}

	// Only a mention time: it ages from there.
	mentioned := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	want := RecencyDecay(now.Sub(mentioned).Hours()/24, metrics)
	if got := RecencyForUnit(nil, nil, &mentioned, now, metrics); got != want {
		t.Fatalf("mention-time signal = %v, want %v", got, want)
	}
	if got := RecencyForUnit(nil, nil, nil, now, metrics); got != 0.5 {
		t.Fatalf("an undated unit = %v, want neutral", got)
	}
}

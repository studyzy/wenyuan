package storage

import (
	"math"
	"time"
)

// RecencyDecay maps a memory's age onto a freshness signal in [0, 1] whose
// neutral point is 0.5, so the boost that consumes it can never overpower the
// relevance score it modulates.
//
// The shape is a deployment's choice because the right trade is workload
// specific: a news-like bank wants a hard recency penalty, an archival one wants
// none at all.
const (
	// RecencyDecayLinear decays in a straight line from 1.0 (today) to a 0.1
	// floor over the window. The default, and the historical behaviour.
	RecencyDecayLinear = "linear"
	// RecencyDecayExponential halves the signal every half-life, so the age at
	// which the signal is neutral is the half-life: younger memories are
	// boosted, older ones penalised, with a smooth asymptote and no hard cutoff.
	RecencyDecayExponential = "exponential"
	// RecencyDecayNone always reports neutral, disabling the signal entirely.
	RecencyDecayNone = "none"
)

// Default recency decay tunings, matching the reference.
const (
	DefaultRecencyDecayLinearWindowDays = 365.0
	DefaultRecencyDecayHalflifeDays     = 90.0
	// calendarPeriodToleranceSeconds is how far short of an exact calendar
	// month or year a span may fall and still be read as that period.
	// Extraction spells the last instant of a period three ways — period minus
	// one second, period minus one day, or the exclusive first instant of the
	// next period — so the window accepts all three. It is deliberately
	// one-sided: a span longer than the calendar period is a genuine interval,
	// not a coarse date.
	calendarPeriodToleranceSeconds = 86400.0
)

// RecencyDecayConfig is the resolved decay tuning for one recall.
type RecencyDecayConfig struct {
	// Function is one of RecencyDecayLinear, RecencyDecayExponential or
	// RecencyDecayNone. Empty means linear.
	Function string
	// LinearWindowDays is the age at which the linear curve reaches its floor.
	// Zero or less means the default.
	LinearWindowDays float64
	// HalflifeDays is the exponential curve's half-life. Zero or less means the
	// default.
	HalflifeDays float64
}

// normalized fills the defaults, so a caller that set nothing keeps the
// historical shape.
func (c RecencyDecayConfig) normalized() RecencyDecayConfig {
	if c.Function == "" {
		c.Function = RecencyDecayLinear
	}
	if c.LinearWindowDays <= 0 {
		c.LinearWindowDays = DefaultRecencyDecayLinearWindowDays
	}
	if c.HalflifeDays <= 0 {
		c.HalflifeDays = DefaultRecencyDecayHalflifeDays
	}
	return c
}

// RecencyDecay maps an age in days onto a freshness signal in [0, 1] with a
// neutral 0.5. A future-dated memory clamps to the maximum, so it is never
// penalised for being ahead of the clock.
func RecencyDecay(daysAgo float64, cfg RecencyDecayConfig) float64 {
	cfg = cfg.normalized()
	switch cfg.Function {
	case RecencyDecayNone:
		return 0.5
	case RecencyDecayExponential:
		if daysAgo <= 0 {
			// Clamped ahead of the power: 0.5 raised to a large negative
			// exponent overflows.
			return 1
		}
		return math.Pow(0.5, daysAgo/cfg.HalflifeDays)
	default:
		if daysAgo <= 0 {
			return 1
		}
		return math.Max(0.1, math.Min(1, 1-daysAgo/cfg.LinearWindowDays))
	}
}

// SpansCalendarPeriod reports whether [start, end] covers exactly one calendar
// month or year.
//
// This distinguishes a *coarse date* — "sometime in 2015", where the whole
// period is one unit of uncertainty — from a *genuine interval* such as an
// employment or a multi-day conference, which really did span its whole range.
// The two are scored differently, below.
func SpansCalendarPeriod(start, end time.Time) bool {
	span := end.Sub(start).Seconds()
	if span <= 0 {
		return false
	}
	month := daysInMonth(start.Year(), start.Month())
	year := 365.0
	if isLeapYear(start.Year()) {
		year = 366.0
	}
	for _, period := range []float64{month * 86400, year * 86400} {
		if span <= period && span >= period-calendarPeriodToleranceSeconds {
			return true
		}
	}
	return false
}

// daysInMonth returns the number of days in a calendar month.
func daysInMonth(year int, month time.Month) float64 {
	return float64(time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Day())
}

// isLeapYear reports whether a year has 366 days.
func isLeapYear(year int) bool {
	return daysInMonth(year, time.February) == 29
}

// RecencyForUnit maps one memory unit's dates onto its freshness signal.
//
// A unit carrying a *coarse* date is scored from the END of the period — the
// latest the event could have happened — instead of its start. Scoring it from
// the start invents an age the memory never had: "the 2026 summit" stored as
// 2026-01-01 reads as months stale in August 2026 and loses to any newer but
// unrelated fact. The signal is additionally capped at neutral for a coarse
// date, because the end of the period is a bound rather than an observation: a
// period still in progress would otherwise end in the future, clamp to 1, and
// swap an invented staleness penalty for an equally invented freshness boost.
// A placeholder date (year 1 or 9999, an open bound nobody means as a date)
// reads as unknown, like the unset columns.
func RecencyForUnit(start, end, mentioned *time.Time, now time.Time, cfg RecencyDecayConfig) float64 {
	start, end, mentioned = finiteUTC(start), finiteUTC(end), finiteUTC(mentioned)
	if start != nil && end != nil && SpansCalendarPeriod(*start, *end) {
		signal := RecencyDecay(now.Sub(*end).Hours()/24, cfg)
		return math.Min(0.5, signal)
	}
	// A genuine interval still ages from its start: aging it from its end is a
	// separate ranking change, not a fix for coarse dates.
	effective := start
	if effective == nil {
		effective = mentioned
	}
	if effective == nil {
		effective = end
	}
	if effective == nil {
		return 0.5
	}
	return RecencyDecay(now.Sub(*effective).Hours()/24, cfg)
}

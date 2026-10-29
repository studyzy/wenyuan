package app

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/studyzy/wenyuan/internal/storage"
)

// cronParser reads the schedule forms a stored trigger may carry.
//
// The five-field standard form is what the reference accepts, and a stored
// expression that does not parse is skipped rather than treated as "always
// due": a typo in a schedule must not cause a refresh on every tick.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// cronLookback bounds the search for a schedule's previous fire time. A
// schedule that has fired within a year is found well inside it; one that has
// not is treated as not due, which is the safe direction.
const cronLookback = 400 * 24 * time.Hour

// cronPreviousFire returns the most recent time a schedule fired at or before
// now, or the zero time when no fire falls inside the lookback.
//
// The library only walks forward, so the previous fire is found by stepping
// forward from a point far enough back to bracket it: the first fire after
// that point is the one preceding now. This is exact for a parsed schedule
// rather than an approximation, because the sequence step yields the next fire
// strictly after its argument.
func cronPreviousFire(schedule cron.Schedule, now time.Time) time.Time {
	start := now.Add(-cronLookback)
	var previous time.Time
	for cursor := start; ; {
		next := schedule.Next(cursor)
		if next.IsZero() || next.After(now) {
			return previous
		}
		previous = next
		cursor = next
	}
}

// cronDue reports whether a schedule's most recent fire time is later than the
// last refresh, which is what makes a schedule due.
//
// The comparison is between two points in time rather than a count of elapsed
// intervals, because that is what survives a restart or a pause: a model whose
// schedule fired while the process was down is due on the first tick back, and
// one that was refreshed after the fire is not.
func cronDue(expression string, lastRefreshedAt, now time.Time) bool {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return false
	}
	schedule, err := cronParser.Parse(trimmed)
	if err != nil {
		return false
	}
	previous := cronPreviousFire(schedule, now)
	if previous.IsZero() {
		return false
	}
	// A model never refreshed is due as soon as its schedule has a fire time
	// behind it.
	if lastRefreshedAt.IsZero() {
		return true
	}
	return previous.After(lastRefreshedAt)
}

// dueScheduledRefresh reports whether a scheduled model has a refresh to run now,
// and whether that refresh must run in full mode.
//
// The two schedules answer to different clocks and different gates:
//
//   - refresh_cron comes due against the last refresh of any mode. It is gated on
//     staleness by the caller, so a tick that finds nothing new costs a cheap
//     query rather than a model call.
//   - full_refresh_cron comes due against last_full_refreshed_at, not
//     last_refreshed_at, which every delta refresh moves — keyed off that, a
//     weekly full refresh would never come due on a model that deltas daily.
//     Its gate is different on purpose: a refresh must have run since the last
//     full one, because a full refresh exists to clear the drift past delta edits
//     left, which a quiet bank still carries. A model that has never had a full
//     refresh, or one never refreshed at all, is due.
//
// When both are due one refresh is submitted, in full mode: it covers what the
// plain refresh would have done.
func dueScheduledRefresh(entry storage.ScheduledMentalModel, now time.Time) (due, full bool) {
	fullDue := cronDue(entry.FullRefreshCron, entry.LastFullRefreshedAt, now) &&
		refreshedSinceLastFull(entry)
	if fullDue || cronDue(entry.RefreshCron, entry.LastRefreshedAt, now) {
		return true, fullDue
	}
	return false, false
}

// refreshedSinceLastFull reports whether any refresh has run since the last full
// one, which is what gives a full_refresh_cron tick something to correct.
func refreshedSinceLastFull(entry storage.ScheduledMentalModel) bool {
	if entry.LastFullRefreshedAt.IsZero() || entry.LastRefreshedAt.IsZero() {
		return true
	}
	return entry.LastRefreshedAt.After(entry.LastFullRefreshedAt)
}

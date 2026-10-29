package validation

import "time"

// The reference refuses a year-1 or year-9999 value wherever a date enters a
// memory (#5820): those years are how a model or an export writes "since
// forever" / "still ongoing", and stored they become Postgres -infinity /
// infinity that every other date's arithmetic cannot mix with. The predicate
// lives here so the route packages that check a memory's dates cannot drift
// into two answers for the same value, the way the timestamp rules do.

// IsPlaceholderDate reports whether t is a year-1 / year-9999 value — an open
// bound, not a date (engine/stored_dates.py is_placeholder_date).
func IsPlaceholderDate(t time.Time) bool {
	return t.Year() == 1 || t.Year() == 9999
}

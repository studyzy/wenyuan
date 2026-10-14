package app

import (
	"testing"
	"time"
)

// cronDue compares the schedule's most recent fire against the last refresh,
// which is what makes it survive a restart or a pause.
func TestCronDue(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name       string
		expression string
		refreshed  time.Time
		want       bool
	}{
		{
			name:       "never refreshed is due",
			expression: "0 * * * *",
			want:       true,
		},
		{
			name:       "refreshed after the last fire is not due",
			expression: "0 * * * *",
			refreshed:  time.Date(2026, 10, 14, 12, 15, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "refreshed before the last fire is due",
			expression: "0 * * * *",
			refreshed:  time.Date(2026, 10, 14, 11, 30, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "a schedule that has not fired is not due",
			expression: "0 0 1 1 *",
			refreshed:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "an empty expression is never due",
			expression: "",
			want:       false,
		},
		{
			name:       "an unparseable expression is never due",
			expression: "not a cron",
			want:       false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cronDue(tc.expression, tc.refreshed, now); got != tc.want {
				t.Errorf("cronDue(%q, %v) = %v, want %v", tc.expression, tc.refreshed, got, tc.want)
			}
		})
	}
}

// A schedule that fires hourly is due once an hour has passed since the last
// refresh, which is the behaviour a scheduled refresh is configured for.
func TestCronDueAcrossHours(t *testing.T) {
	expression := "0 * * * *"
	refreshed := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if cronDue(expression, refreshed, refreshed.Add(30*time.Minute)) {
		t.Error("half an hour after a refresh is not due for an hourly schedule")
	}
	if !cronDue(expression, refreshed, refreshed.Add(61*time.Minute)) {
		t.Error("just over an hour after a refresh is due for an hourly schedule")
	}
}

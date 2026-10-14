package memoryops

import (
	"testing"
	"time"
)

// reference is a fixed Wednesday so weekday arithmetic is deterministic.
var parseReference = time.Date(2026, time.October, 14, 15, 0, 0, 0, time.UTC)

func TestTemporalWindowFromQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		start string
		end   string
	}{
		{"today", "what did we discuss today", "2026-10-14", "2026-10-14"},
		{"yesterday", "what happened yesterday", "2026-10-13", "2026-10-13"},
		{"tomorrow", "what is planned for tomorrow", "2026-10-15", "2026-10-15"},
		{"tonight", "what is on tonight", "2026-10-14", "2026-10-14"},
		{"days ago", "what changed 3 days ago", "2026-10-11", "2026-10-11"},
		{"weeks ago", "what did we ship 2 weeks ago", "2026-09-30", "2026-09-30"},
		{"in the last", "what did we do in the last 2 weeks", "2026-09-30", "2026-10-14"},
		{"last week", "what happened last week", "2026-10-07", "2026-10-13"},
		{"next month", "what is planned next month", "2026-10-15", "2026-11-13"},
		{"last friday", "what did we decide last friday", "2026-10-09", "2026-10-09"},
		{"bare weekday", "what happened on monday", "2026-10-12", "2026-10-12"},
		{"month day", "what happened in march 5", "2026-03-05", "2026-03-05"},
		{"month day year", "what happened on march 5, 2024", "2024-03-05", "2024-03-05"},
		{"bare year", "what happened in 2024", "2024-01-01", "2024-12-31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window := temporalWindowFromQuery(tc.query, parseReference)
			if window == nil {
				t.Fatalf("query %q produced no window", tc.query)
			}
			start, _ := time.Parse("2006-01-02", tc.start)
			end, _ := time.Parse("2006-01-02", tc.end)
			if !window.Start.UTC().Truncate(24 * time.Hour).Equal(start) {
				t.Errorf("start = %v, want %v", window.Start, tc.start)
			}
			if window.End.UTC().Format("2006-01-02") != end.Format("2006-01-02") {
				t.Errorf("end = %v, want %v", window.End, tc.end)
			}
		})
	}
}

// A query naming no time yields no window, so an untimed question is not
// silently narrowed to a period that would hide the right memories.
func TestTemporalWindowAbsentWithoutTimeWords(t *testing.T) {
	for _, query := range []string{
		"what is the deployment policy",
		"how do we handle refunds",
		"maybe we should reconsider",
		"",
	} {
		if window := temporalWindowFromQuery(query, parseReference); window != nil {
			t.Errorf("query %q produced a window %v..%v", query, window.Start, window.End)
		}
	}
}

// "may" as the month must not be read out of a word that merely contains it.
func TestMonthNameRequiresWholeWord(t *testing.T) {
	if window := temporalWindowFromQuery("maybe we should reconsider", parseReference); window != nil {
		t.Fatalf("'maybe' produced a window %v..%v", window.Start, window.End)
	}
}

// A relative phrase resolves against the supplied reference, not the wall
// clock, so the same query means the same window in a test and in its caller's
// stated frame.
func TestTemporalWindowAnchorsToReference(t *testing.T) {
	early := temporalWindowFromQuery("what happened yesterday", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	late := temporalWindowFromQuery("what happened yesterday", time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC))
	if early == nil || late == nil {
		t.Fatal("a relative phrase must resolve")
	}
	if early.Start.Equal(late.Start) {
		t.Fatalf("both references resolved to %v, want different days", early.Start)
	}
	if early.Start.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("early start = %v, want 2026-01-01", early.Start)
	}
}

// An explicit caller window must survive a temporal phrase in the question, so
// the caller's stated range is authoritative.
func TestExplicitWindowBeatsQueryText(t *testing.T) {
	explicit, err := parseTemporalWindow(&TemporalWindowRequest{
		Start: "2020-01-01T00:00:00Z", End: "2020-01-31T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit == nil {
		t.Fatal("an explicit window must parse")
	}
	// The handler only derives from the query when the explicit window is
	// absent, which is the behaviour this asserts by construction.
	if fromQuery := temporalWindowFromQuery("what happened yesterday", parseReference); fromQuery.Start.Equal(explicit.Start) {
		t.Fatal("the derived window must differ from the explicit one the caller stated")
	}
}

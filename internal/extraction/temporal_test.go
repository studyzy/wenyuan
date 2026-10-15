package extraction

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A year named "in 2015" becomes a coarse span covering the whole year, which
// is the shape storage reads as "sometime in this period".
func TestInferYearSpan(t *testing.T) {
	ref := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	start, end, ok := InferPeriod("She moved to Berlin in 2015.", ref)
	if !ok {
		t.Fatal("a named year must be recognised")
	}
	if got := start.Format(time.RFC3339); got != "2015-01-01T00:00:00Z" {
		t.Fatalf("year start = %s", got)
	}
	if got := end.Format(time.RFC3339); got != "2015-12-31T00:00:00Z" {
		t.Fatalf("year end = %s", got)
	}
	if !storage.SpansCalendarPeriod(start, end) {
		t.Fatal("a year span must read as one calendar period")
	}
}

// A month with a year becomes the month's first..last day.
func TestInferMonthSpan(t *testing.T) {
	ref := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		text             string
		wantStart        string
		wantEnd          string
		wantCalendarSpan bool
	}{
		{"The summit is in March 2026.", "2026-03-01T00:00:00Z", "2026-03-31T00:00:00Z", true},
		{"We launched in February 2026.", "2026-02-01T00:00:00Z", "2026-02-28T00:00:00Z", true},
		{"Born in Feb 2024.", "2024-02-01T00:00:00Z", "2024-02-29T00:00:00Z", true},
	} {
		start, end, ok := InferPeriod(tc.text, ref)
		if !ok {
			t.Fatalf("%q: month must be recognised", tc.text)
		}
		if got := start.Format(time.RFC3339); got != tc.wantStart {
			t.Fatalf("%q: start = %s, want %s", tc.text, got, tc.wantStart)
		}
		if got := end.Format(time.RFC3339); got != tc.wantEnd {
			t.Fatalf("%q: end = %s, want %s", tc.text, got, tc.wantEnd)
		}
		if got := storage.SpansCalendarPeriod(start, end); got != tc.wantCalendarSpan {
			t.Fatalf("%q: SpansCalendarPeriod = %v, want %v", tc.text, got, tc.wantCalendarSpan)
		}
	}
}

// A relative phrase resolves against the supplied reference, so the same text
// yields a different day under a different reference.
func TestRelativePhraseUsesReference(t *testing.T) {
	first := time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC)
	start, end, ok := InferPeriod("We deployed the fix last week.", first)
	if !ok {
		t.Fatal("a relative phrase must be recognised")
	}
	want := "2026-10-07T00:00:00Z"
	if got := start.Format(time.RFC3339); got != want {
		t.Fatalf("start = %s, want %s", got, want)
	}
	if got := end.Format(time.RFC3339); got != want {
		t.Fatalf("end = %s, want %s", got, want)
	}

	second := time.Date(2026, time.December, 1, 9, 0, 0, 0, time.UTC)
	start, _, ok = InferPeriod("We deployed the fix last week.", second)
	if !ok {
		t.Fatal("a relative phrase must be recognised")
	}
	if got := start.Format(time.RFC3339); got != "2026-11-24T00:00:00Z" {
		t.Fatalf("a different reference must move the day, got %s", got)
	}
}

// Without a reference time a relative phrase stays unresolved rather than being
// guessed.
func TestRelativePhraseWithoutReferenceIsUnresolved(t *testing.T) {
	if _, _, ok := InferPeriod("We deployed last week.", time.Time{}); ok {
		t.Fatal("a zero reference must not resolve a relative phrase")
	}
}

// The deterministic fallback now infers dates from the text instead of always
// reporting none.
func TestDeterministicFallbackInfersDates(t *testing.T) {
	opts := Options{ReferenceTime: time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)}
	result, err := New(nil).ExtractWith(context.Background(), "The office opened in 2015.", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %+v", result.Facts)
	}
	if got := result.Facts[0].OccurredStart; got != "2015-01-01T00:00:00Z" {
		t.Fatalf("fallback did not infer a date: %q", got)
	}
	if got := result.Facts[0].OccurredEnd; got != "2015-12-31T00:00:00Z" {
		t.Fatalf("fallback end = %q", got)
	}
}

// The model path fills a missing date from the fact's own text, but never
// overrides an explicit one.
func TestModelPathInfersMissingDateOnly(t *testing.T) {
	ref := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	structured := map[string]any{"facts": []any{
		map[string]any{"text": "The summit happened in March 2026."},
		map[string]any{"text": "An explicit fact.", "occurred_start": "2020-01-02T00:00:00Z"},
	}}
	stub := &stubProvider{name: "openai", structured: structured}
	result, err := New(stub).ExtractWith(context.Background(), "text", Options{ReferenceTime: ref})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Facts[0].OccurredStart; got != "2026-03-01T00:00:00Z" {
		t.Fatalf("missing date not inferred: %q", got)
	}
	if got := result.Facts[1].OccurredStart; got != "2020-01-02T00:00:00Z" {
		t.Fatalf("an explicit date must be kept: %q", got)
	}
}

// A zero reference disables inference, so a caller that supplies none keeps the
// historical behaviour.
func TestNoReferenceDisablesInference(t *testing.T) {
	structured := map[string]any{"facts": []any{
		map[string]any{"text": "The summit happened in March 2026."},
	}}
	stub := &stubProvider{name: "openai", structured: structured}
	result, err := New(stub).ExtractWith(context.Background(), "text", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Facts[0].OccurredStart; got != "" {
		t.Fatalf("a zero reference must not infer a date, got %q", got)
	}
}

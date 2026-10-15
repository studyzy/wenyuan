package extraction

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Date inference for the deterministic shapes a model may miss.
//
// The model path already reads ISO-8601 timestamps, so this covers only the
// phrasings a model is most likely to leave unresolved: a bare year, a month
// with a year, and the relative expressions ("last week") that only make sense
// against a reference time the caller supplies. Everything here is
// dependency-free and deliberately conservative: a phrase it does not
// recognise yields no date rather than a guessed one.

// monthYearRe matches "March 2026" and "in March 2026" (full or abbreviated
// month names). It is checked before the bare-year form so the month is not
// lost to the year.
var monthYearRe = regexp.MustCompile(
	`(?i)\b(?:in\s+)?(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sep|sept|oct|nov|dec)\s+(\d{4})\b`,
)

// yearInRe matches "in 2015": a four-digit year introduced by "in", which keeps
// the match from firing on an unrelated number.
var yearInRe = regexp.MustCompile(`(?i)\bin\s+(\d{4})\b`)

// relativePhrase maps a relative expression to the whole days it shifts the
// reference date by. Longer phrases come first so "this week" is not read as a
// shorter one; each pattern is anchored on a word boundary.
type relativePhrase struct {
	pattern *regexp.Regexp
	offset  int
}

var relativePhrases = []relativePhrase{
	{regexp.MustCompile(`(?i)\blast night\b`), -1},
	{regexp.MustCompile(`(?i)\byesterday\b`), -1},
	{regexp.MustCompile(`(?i)\btoday\b`), 0},
	{regexp.MustCompile(`(?i)\bthis morning\b`), 0},
	{regexp.MustCompile(`(?i)\bthis afternoon\b`), 0},
	{regexp.MustCompile(`(?i)\bthis evening\b`), 0},
	{regexp.MustCompile(`(?i)\btonight\b`), 0},
	{regexp.MustCompile(`(?i)\btomorrow\b`), 1},
	{regexp.MustCompile(`(?i)\blast week\b`), -7},
	{regexp.MustCompile(`(?i)\bthis week\b`), 0},
	{regexp.MustCompile(`(?i)\bnext week\b`), 7},
	{regexp.MustCompile(`(?i)\blast month\b`), -30},
	{regexp.MustCompile(`(?i)\bthis month\b`), 0},
	{regexp.MustCompile(`(?i)\bnext month\b`), 30},
}

// monthNumbers maps a month name (full or abbreviated, lower-cased) to its
// calendar month.
var monthNumbers = map[string]time.Month{
	"january": time.January, "jan": time.January,
	"february": time.February, "feb": time.February,
	"march": time.March, "mar": time.March,
	"april": time.April, "apr": time.April,
	"may":  time.May,
	"june": time.June, "jun": time.June,
	"july": time.July, "jul": time.July,
	"august": time.August, "aug": time.August,
	"september": time.September, "sep": time.September, "sept": time.September,
	"october": time.October, "oct": time.October,
	"november": time.November, "nov": time.November,
	"december": time.December, "dec": time.December,
}

// InferPeriod infers the calendar period a fact's text names, resolved against
// ref, and reports whether it recognised one.
//
// A year or month becomes a *coarse* span covering the whole period — the year
// first day to last, the month first day to last — which is the shape
// storage.SpansCalendarPeriod reads as "sometime in this period" rather than a
// genuine interval. A relative phrase ("last week") resolves to the single day
// it names. Without a reference time nothing is inferred, because a relative
// phrase has no absolute meaning on its own.
func InferPeriod(text string, ref time.Time) (start, end time.Time, ok bool) {
	if ref.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	if match := monthYearRe.FindStringSubmatch(text); match != nil {
		month, found := monthNumbers[strings.ToLower(match[1])]
		year, err := strconv.Atoi(match[2])
		if !found || err != nil {
			return time.Time{}, time.Time{}, false
		}
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		// Day zero of the next month is the last day of this one, so the span
		// is correct for every month length including February.
		last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
		return first, last, true
	}
	if match := yearInRe.FindStringSubmatch(text); match != nil {
		year, err := strconv.Atoi(match[1])
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
			time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC), true
	}
	for _, phrase := range relativePhrases {
		if !phrase.pattern.MatchString(text) {
			continue
		}
		shifted := ref.AddDate(0, 0, phrase.offset)
		day := time.Date(shifted.Year(), shifted.Month(), shifted.Day(), 0, 0, 0, 0, time.UTC)
		return day, day, true
	}
	return time.Time{}, time.Time{}, false
}

// applyDateInference fills in a fact's dates from its own text when the model
// left them empty. An explicit date the model supplied is never overridden.
func applyDateInference(facts []Fact, ref time.Time) {
	if ref.IsZero() {
		return
	}
	for i := range facts {
		if strings.TrimSpace(facts[i].OccurredStart) != "" {
			continue
		}
		start, end, ok := InferPeriod(facts[i].Text, ref)
		if !ok {
			continue
		}
		facts[i].OccurredStart = start.Format(time.RFC3339)
		facts[i].OccurredEnd = end.Format(time.RFC3339)
	}
}

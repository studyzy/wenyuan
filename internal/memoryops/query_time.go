package memoryops

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// This file derives a temporal window from the query text when the caller did
// not supply one. Explicit windows always win: the caller who names a range has
// stated what they mean, and a phrase in the question must not override it.
//
// The parser covers the forms a query actually uses — relative words, weekday
// and month names, and "N <unit> ago" / "last|next|this <unit>" spans. It is
// deliberately narrow: returning a wrong window ranks unrelated memories ahead
// of the right ones, which is worse than returning none and leaving the query
// untimed.

// monthNames maps a lowercased English month name to its number.
var monthNames = map[string]time.Month{
	"january": time.January, "february": time.February, "march": time.March,
	"april": time.April, "may": time.May, "june": time.June,
	"july": time.July, "august": time.August, "september": time.September,
	"october": time.October, "november": time.November, "december": time.December,
}

// weekdayNames maps a lowercased English weekday name to its number, Sunday
// being zero.
var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
}

// relativeOffsets maps a bare relative word to the day offset it names from the
// reference date.
var relativeOffsets = map[string]int{
	"today": 0, "tonight": 0, "now": 0, "yesterday": -1, "tomorrow": 1,
}

// unitDays is how many days each named period unit spans, for the "N units ago"
// and quantified forms.
var unitDays = map[string]int{
	"day": 1, "days": 1,
	"week": 7, "weeks": 7,
	"month": 30, "months": 30,
	"year": 365, "years": 365,
	"quarter": 91, "quarters": 91,
	"weekend": 2,
}

// agoPattern matches "<n> <unit> ago" and "<unit> ago".
var agoPattern = regexp.MustCompile(`\b(\d+\s+)?(day|days|week|weeks|month|months|year|years|quarter|quarters)\s+ago\b`)

// relativeSpanPattern matches "last|next|past|this|coming <unit>", where the
// unit may be a period word or a weekday.
var relativeSpanPattern = regexp.MustCompile(`\b(last|next|past|this|coming|previous)\s+(day|days|week|weeks|month|months|year|years|quarter|quarters|weekend|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)

// inSpanPattern matches "in the last <n> <unit>" and "in the past <n> <unit>".
var inSpanPattern = regexp.MustCompile(`\bin\s+the\s+(?:last|past)\s+(\d+)\s+(day|days|week|weeks|month|months|year|years)\b`)

// monthDayPattern matches "<month> <day>" and "<month> <day>, <year>".
var monthDayPattern = regexp.MustCompile(`\b(january|february|march|april|may|june|july|august|september|october|november|december)\s+(\d{1,2})(?:\s*,?\s*(\d{4}))?\b`)

// bareYearPattern matches a four-digit year on its own.
var bareYearPattern = regexp.MustCompile(`\b(1\d{3}|2\d{3})\b`)

// temporalWindowFromQuery derives the window a query's text names, or nil when
// it names none. The reference date anchors every relative phrase, so the same
// query resolves differently a year apart, which is what a relative phrase
// means.
func temporalWindowFromQuery(query string, reference time.Time) *storage.TemporalWindow {
	lowered := strings.ToLower(query)
	ref := reference.UTC()
	day := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)

	// "in the last N units" is the most specific span, so it is tried first.
	if match := inSpanPattern.FindStringSubmatch(lowered); match != nil {
		count, _ := strconv.Atoi(match[1])
		if days, ok := unitDays[match[2]]; ok && count > 0 {
			return dayRange(day.AddDate(0, 0, -count*days), day)
		}
	}
	// "<n> units ago" names a single day, some distance back.
	if match := agoPattern.FindStringSubmatch(lowered); match != nil {
		days, ok := unitDays[match[2]]
		if !ok {
			return nil
		}
		count := 1
		if strings.TrimSpace(match[1]) != "" {
			count, _ = strconv.Atoi(strings.TrimSpace(match[1]))
		}
		if count <= 0 {
			count = 1
		}
		target := day.AddDate(0, 0, -count*days)
		return dayRange(target, target)
	}
	// "last week" and friends name a span, or a weekday.
	if match := relativeSpanPattern.FindStringSubmatch(lowered); match != nil {
		if window := relativeSpanWindow(match[1], match[2], day); window != nil {
			return window
		}
	}
	// A named month with a day fixes an absolute date.
	if match := monthDayPattern.FindStringSubmatch(lowered); match != nil {
		if window := monthDayWindow(match, day); window != nil {
			return window
		}
	}
	// A bare relative word means that single day.
	for word, offset := range relativeOffsets {
		if containsWord(lowered, word) {
			target := day.AddDate(0, 0, offset)
			return dayRange(target, target)
		}
	}
	// A bare weekday means the most recent one, which is what "on Friday"
	// asks for in a question about the past.
	for word, weekday := range weekdayNames {
		if containsWord(lowered, word) {
			target := mostRecentWeekday(day, weekday)
			return dayRange(target, target)
		}
	}
	// A lone four-digit year means that whole year.
	if match := bareYearPattern.FindStringSubmatch(lowered); match != nil {
		year, _ := strconv.Atoi(match[1])
		start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC)
		return dayRange(start, end)
	}
	return nil
}

// relativeSpanWindow resolves "last|next|this <unit>" to a span. A weekday unit
// resolves to that weekday in the named direction rather than a span.
func relativeSpanWindow(direction, unit string, day time.Time) *storage.TemporalWindow {
	if weekday, ok := weekdayNames[unit]; ok {
		target := mostRecentWeekday(day, weekday)
		switch direction {
		case "next", "coming":
			target = target.AddDate(0, 0, 7)
		case "this":
			target = day.AddDate(0, 0, int((weekday-day.Weekday()+7)%7))
		}
		return dayRange(target, target)
	}
	days, ok := unitDays[unit]
	if !ok {
		if unit == "weekend" {
			days = 2
		} else {
			return nil
		}
	}
	switch direction {
	case "next", "coming":
		return dayRange(day.AddDate(0, 0, 1), day.AddDate(0, 0, days))
	case "this":
		return dayRange(day.AddDate(0, 0, -(days-1)), day)
	default:
		// "last", "past" and "previous" all look back.
		return dayRange(day.AddDate(0, 0, -days), day.AddDate(0, 0, -1))
	}
}

// monthDayWindow resolves a named month and day to that date, using the
// current year unless one was named.
func monthDayWindow(match []string, day time.Time) *storage.TemporalWindow {
	month, ok := monthNames[match[1]]
	if !ok {
		return nil
	}
	dayOfMonth, err := strconv.Atoi(match[2])
	if err != nil || dayOfMonth < 1 || dayOfMonth > 31 {
		return nil
	}
	year := day.Year()
	if match[3] != "" {
		if parsed, err := strconv.Atoi(match[3]); err == nil {
			year = parsed
		}
	}
	target := time.Date(year, month, dayOfMonth, 0, 0, 0, 0, time.UTC)
	return dayRange(target, target)
}

// mostRecentWeekday returns the most recent occurrence of a weekday on or
// before the reference day.
func mostRecentWeekday(day time.Time, weekday time.Weekday) time.Time {
	delta := int(day.Weekday()-weekday) % 7
	if delta < 0 {
		delta += 7
	}
	return day.AddDate(0, 0, -delta)
}

// dayRange builds an inclusive window covering whole days, so a memory dated
// anywhere within the named day matches rather than only at midnight.
func dayRange(start, end time.Time) *storage.TemporalWindow {
	return &storage.TemporalWindow{
		HasStart: true,
		HasEnd:   true,
		Start:    time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC),
		End:      time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 0, time.UTC),
	}
}

// containsWord reports whether text contains word as a whole word, so "may" in
// "maybe" is not read as the month.
func containsWord(text, word string) bool {
	index := strings.Index(text, word)
	for index >= 0 {
		beforeOK := index == 0 || !isWordChar(rune(text[index-1]))
		after := index + len(word)
		afterOK := after >= len(text) || !isWordChar(rune(text[after]))
		if beforeOK && afterOK {
			return true
		}
		next := strings.Index(text[index+1:], word)
		if next < 0 {
			return false
		}
		index += 1 + next
	}
	return false
}

// isWordChar reports whether a rune can be part of a word.
func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

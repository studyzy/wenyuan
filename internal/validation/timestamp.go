package validation

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// The reference types the dry-run body's timestamp as a plain `datetime`, so the
// framework's own datetime parser — not a lenient `datetime | str` validator —
// decides what the field accepts. This file reproduces that parser's decisions:
// whether a value is a datetime at all, and when it is not, the exact message the
// reference refuses it with.
//
// The parser reads a value in four ways, in order, and the first failure names
// the answer while the first success accepts it:
//
//  1. as a number of seconds (a number, or an all-digit string short enough);
//  2. as a date and then a time, separated by `T`, `t`, `_` or a space;
//  3. as a date whose time digits are chopped off, when the value is too short
//     to be a date;
//  4. as a time, then as the date of a time, then as a date and time, at each
//     position of the value in turn.
//
// The order is observable, which is why it is reproduced rather than approximated:
// `2024-1-1` is "too short" because reading 3 takes it apart, while a stray
// character is an "invalid character in year" because only reading 4 reaches it.
// The messages are the parser's own; the framework carries them through as a
// `datetime_from_date_parsing` failure with the message in its context.

// TimestampFailure reports why a string is not a datetime the reference accepts,
// as the framework's `datetime_from_date_parsing` item, or nil when it is.
func TimestampFailure(value string) *httpx.ValidationError {
	message := timestampParseError(value)
	if message == "" {
		return nil
	}
	// A date that parses but cannot become a datetime fails one step later, in
	// the framework's own conversion, which reports it as a different kind.
	if strings.HasSuffix(message, "is out of range") {
		item := httpx.ValidationItem("datetime_parsing", nil,
			"Input should be a valid datetime, "+message, value,
			map[string]any{"error": message})
		return &item
	}
	item := httpx.ValidationItem("datetime_from_date_parsing", nil,
		"Input should be a valid datetime or date, "+message, value,
		map[string]any{"error": message})
	return &item
}

// timestampParseError returns the parser's message for the first reading that
// refuses value, or "" when a reading accepts it.
func timestampParseError(value string) string {
	// A number of seconds: any numeric spelling, at any length, and nothing else
	// has to be true of it.
	if durationSeconds(value) {
		return ""
	}
	_, rest, message := parseStrictDate(value)
	if message == "" {
		switch {
		case rest == "":
			// The date is the whole value.
			return ""
		case rest[0] == 'Z' || rest[0] == 'z':
			return ""
		case isTimeSeparator(rest[0]):
			if len(rest) > 1 {
				switch rest[1] {
				case 'z', 'Z':
					return ""
				case '+', '-':
					// A bare sign opens a timezone whose minutes may be absent.
					return parseOffsetTail(rest[2:])
				}
			}
			if message := parseStrictTime(rest[1:]); message != "" {
				if message == "input is too short" {
					return "unexpected extra characters at the end of the input"
				}
				return message
			}
			return ""
		}
		// Characters follow the date that are not a time: the value may still be
		// a shorter date the caller truncated, in which case that reading says
		// why, and only otherwise does the date reading's own message stand.
		if shorter := parseIntStringLaxDigits(value); shorter != "" && shorter != "invalid date separator, expected `-`" {
			return shorter
		}
		return "unexpected extra characters at the end of the input"
	}
	// The value is not a whole date. A value shorter than a date and its
	// separator is refused as too short however it is shaped — `2024-01` and
	// `2024-1-1` alike — so only a longer one reaches the readings that can
	// name a different failure.
	if len(value) >= 10 && startsWithDigits(value, 4) && value[4] == '-' && (!isDigit(value[5]) || !isDigit(value[6])) {
		// The year and its separator are sound and the month is not written with
		// digits, in a value long enough for the parser to reach it.
		return "invalid character in month"
	}
	if len(value) < 10 {
		if parseIntStringLaxDigits(value) == "" {
			return ""
		}
		return "input is too short"
	}
	lax := parseIntStringLaxDigits(value)
	switch {
	case lax == "":
		return ""
	case lax != "invalid character in year":
		return lax
	case isDateShaped(value):
		if strings.HasPrefix(message, "month value is outside expected range") {
			// A month already refused is reported even when what follows it is not
			// the separator the day would need.
			return message
		}
		if len(value) >= 10 && isDigit(value[6]) && value[7] == '-' {
			// A date-shaped value with a day in place is refused for its own date.
			return message
		}
		return "invalid date separator, expected `-`"
	}
	if startsWithDigits(value, 4) {
		// Four digits open the value where its year belongs, and what follows them
		// is not a date separator: the separator is what the parser refuses.
		return message
	}
	// Nothing but digits where the year should be, in a value too long to be a
	// number of seconds: the year's own characters are what the parser refuses.
	return "invalid character in year"
}

// durationSeconds reports whether the whole value is a number of seconds, which
// the parser accepts however long or short the number is, with or without a
// decimal point and a sign.
func durationSeconds(value string) bool {
	if value == "" {
		return false
	}
	digits := 0
	points := 0
	for index := 0; index < len(value); index++ {
		switch character := value[index]; {
		case isDigit(character):
			digits++
		case character == '.':
			points++
		case (character == '+' || character == '-') && index == 0:
		default:
			return false
		}
	}
	return digits > 0 && points <= 1
}

// isDateShaped reports whether the value begins with the digits of a year and a
// date separator, which is what makes the date reading's message the one the
// caller needs: a value that looks like a date but is not one says why.
func isDateShaped(value string) bool {
	if len(value) < 5 {
		return false
	}
	for index := 0; index < 4; index++ {
		if !isDigit(value[index]) {
			return false
		}
	}
	return value[4] == '-'
}

// parseIntStringLaxDigits is reading 3: a date whose time digits are chopped off
// two characters at a time, from the front, while the value is too short to be a
// date.
func parseIntStringLaxDigits(value string) string {
	if value == "" {
		return "input is too short"
	}
	if _, rest, message := parseStrictDate(value); message == "" {
		return parseStrictTime(rest)
	}
	if !allDigits(value) {
		return "invalid character in year"
	}
	return parseIntStringLaxDigits(trimFirstChunk(value))
}

// trimFirstChunk drops the first two characters of value.
func trimFirstChunk(value string) string {
	if len(value) < 2 {
		return ""
	}
	return value[2:]
}

// parseOffsetTail reads what follows a bare timezone sign: nothing, an hour-only
// offset, or an hour and a minute. The reference's datetime type constrains no
// offset, so a readable tail is accepted.
// parseStrictOffset reads the offset a full time ends with, which is written with
// both its hour and its minutes.
func parseStrictOffset(s string) string {
	_, rest, message := parseStrictDigits(s, 2, 23, fieldTimezoneHour)
	switch {
	case message == "input is too short":
		// The hour alone is not an offset where the minutes are expected.
		return "unexpected extra characters at the end of the input"
	case message != "":
		return "unexpected extra characters at the end of the input"
	}
	if rest == "" {
		return "unexpected extra characters at the end of the input"
	}
	if rest[0] == ':' || rest[0] == '_' {
		_, rest, message = parseStrictDigits(rest[1:], 2, 59, fieldTimezoneMinute)
		if message != "" {
			return message
		}
	}
	if rest != "" {
		return "unexpected extra characters at the end of the input"
	}
	return ""
}

func parseOffsetTail(s string) string {
	if s == "" {
		return ""
	}
	_, rest, message := parseStrictDigits(s, 2, 23, fieldTimezoneHour)
	switch {
	case message == "input is too short":
		// An offset may be written with its hour alone.
		return ""
	case message != "":
		return message
	}
	if rest == "" {
		return ""
	}
	if rest[0] != ':' && rest[0] != '_' {
		return "unexpected extra characters at the end of the input"
	}
	_, tail, message := parseStrictDigits(rest[1:], 2, 59, fieldTimezoneMinute)
	if message != "" {
		return message
	}
	if tail != "" {
		return "unexpected extra characters at the end of the input"
	}
	return ""
}

// parseStrictTime reads a time: a two-digit hour, `:` or `_`, a two-digit
// minute, then optionally `:`, a second, and a fraction.
func parseStrictTime(s string) string {
	consumed, ok := parseHour(s)
	if !ok {
		return "unexpected extra characters at the end of the input"
	}
	s = s[consumed:]
	if s == "" {
		// The hour is the whole value; a time needs at least its minutes, and
		// what is missing is the rest of the value.
		return "unexpected extra characters at the end of the input"
	}
	if s[0] != ':' && s[0] != '_' {
		return "invalid time separator, expected `:`"
	}
	_, s, message := parseStrictDigits(s[1:], 2, 59, fieldMinute)
	if message != "" {
		// A minute past its range is not a time at all, and the value's
		// trailing characters are what says so.
		if message == fieldMinute.overRange {
			return "unexpected extra characters at the end of the input"
		}
		return message
	}
	if s == "" {
		return ""
	}
	// What follows the minutes decides how the value ends: seconds (optionally
	// with a fraction) and then a timezone, or a timezone straight away, or a
	// fractional second alone.
	var hasSeconds bool
	switch s[0] {
	case ':', '_':
		hasSeconds = true
		var ok bool
		s, ok = parseSecond(s[1:])
		if !ok {
			return "unexpected extra characters at the end of the input"
		}
	case '.':
		if message := parseFraction(s[1:]); message != "" {
			return message
		}
		return ""
	case ',':
		if message := parseFraction(s[1:]); message != "" {
			return message
		}
		return ""
	}
	if s == "" {
		return ""
	}
	if s[0] == '.' || s[0] == ',' {
		if message := parseFraction(s[1:]); message != "" {
			if message == "second fraction digits missing after `.`" {
				return "unexpected extra characters at the end of the input"
			}
			return message
		}
		return ""
	}
	if s[0] == '+' || s[0] == '-' {
		// A timezone ends the value, and one written after a full time always
		// carries its minutes; without seconds there is nothing to adjust.
		if !hasSeconds {
			return "unexpected extra characters at the end of the input"
		}
		return parseStrictOffset(s[1:])
	}
	return ""
}

// parseFraction reads a fractional second: one to six digits, then whatever
// follows, which has to be empty for the time to be the whole value.
func parseFraction(s string) string {
	if s == "" {
		return "second fraction digits missing after `.`"
	}
	start := 0
	for start < 6 && start < len(s) && isDigit(s[start]) {
		start++
	}
	if start == 0 {
		return "unexpected extra characters at the end of the input"
	}
	return ""
}

// parseHour reads the two-digit hour. The parser reads exactly two characters
// and refuses only when both exceed 13, so the twelve-hour spellings such as
// " 9" are the only way to reach 14 through 19 and 21 through 23 — which is why
// an hour of 25 is refused as too short and an hour of 19 is accepted.
func parseHour(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	if isDigit(s[0]) && isDigit(s[1]) {
		if s[0] > '2' || (s[0] == '2' && s[1] > '3') {
			return 0, false
		}
		return 2, true
	}
	if s[0] == ' ' && s[1] >= '1' && s[1] <= '9' {
		return 2, true
	}
	return 0, false
}

// parseSecond reads the two-digit second, with the same twelve-hour rule as the
// hour.
func parseSecond(s string) (string, bool) {
	if len(s) < 2 {
		return "", false
	}
	if isDigit(s[0]) && isDigit(s[1]) {
		if s[0] > '5' {
			return "", false
		}
		return s[2:], true
	}
	if s[0] == ' ' && s[1] >= '1' && s[1] <= '9' {
		return s[2:], true
	}
	return "", false
}

// parseStrictDate reads `year-month-day` and returns the value of the day and
// what follows it. The day is checked against 31 without a calendar, which is
// the parser's own looseness: 2024-02-30 is a date there.
func parseStrictDate(input string) (int, string, string) {
	year, rest, message := parseStrictDigits(input, 4, 9999, fieldYear)
	if message != "" {
		return 0, "", message
	}
	if rest == "" || rest[0] != '-' {
		return 0, "", "invalid date separator, expected `-`"
	}
	monthSource := rest[1:]
	month, rest, message := parseStrictDigits(monthSource, 2, 12, fieldMonth)
	if message != "" {
		if message == fieldMonth.overRange && !monthNamesAValue(monthSource) {
			// A month out of range is only the answer once the day it needs is
			// there to read; otherwise the missing separator comes first.
			return 0, "", "invalid date separator, expected `-`"
		}
		return 0, "", message
	}
	if rest == "" || rest[0] != '-' {
		// A month that cannot name one is refused for that reason when the day it
		// would need is in place; otherwise the separator is what is missing.
		if month < 1 || month > 12 && rest != "" && isDigit(rest[1]) {
			return 0, "", fieldMonth.overRange
		}
		return 0, "", "invalid date separator, expected `-`"
	}
	day, rest, message := parseStrictDigits(rest[1:], 2, 31, fieldDay)
	if message != "" {
		return 0, "", message
	}
	if month < 1 || month > 12 {
		return 0, "", fieldMonth.overRange
	}
	if day < 1 || day > daysInMonth(year, month) {
		return 0, "", fieldDay.overRange
	}
	if year == 0 {
		// The framework cannot build a timestamp for year zero.
		return 0, "", "year 0 is out of range"
	}
	return year*10000 + month*100 + day, rest, ""
}

// digitField is one numeric field of a date or time: the message its over-long
// digit produces.
type digitField struct {
	overRange string
}

// The numeric fields the parser reads, each with the message its over-long digit
// produces.
var (
	fieldYear           = digitField{overRange: "year value is outside expected range"}
	fieldMonth          = digitField{overRange: "month value is outside expected range of 1-12"}
	fieldDay            = digitField{overRange: "day value is outside expected range"}
	fieldMinute         = digitField{overRange: "minute value is outside expected range of 0-59"}
	fieldTimezoneHour   = digitField{overRange: "timezone offset must be less than 24 hours"}
	fieldTimezoneMinute = digitField{overRange: "timezone minute value is outside expected range of 0-59"}
)

// parseStrictDigits reads exactly count ASCII digits, each within maxDigit, and
// returns what follows them.
func parseStrictDigits(s string, count, maxDigit int, field digitField) (int, string, string) {
	value := 0
	for index := 0; index < count; index++ {
		if index >= len(s) || !isDigit(s[index]) {
			return 0, "", "input is too short"
		}
		value = value*10 + int(s[index]-'0')
	}
	if value > maxDigit {
		return 0, "", field.overRange
	}
	return value, s[count:], ""
}

// isTimeSeparator reports whether b separates a date from a time.
func isTimeSeparator(b byte) bool {
	switch b {
	case 'T', 't', '_', ' ':
		return true
	}
	return false
}

// startsWithDigits reports whether the value opens with at least count ASCII
// digits, which is how the parser recognizes where the year ends.
func startsWithDigits(value string, count int) bool {
	if len(value) < count {
		return false
	}
	for index := 0; index < count; index++ {
		if !isDigit(value[index]) {
			return false
		}
	}
	return true
}

// allDigits reports whether every character is an ASCII digit. An empty value has
// no digits, and the parser's digit walk is 1-based, so it reports one.
func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if !isDigit(value[index]) {
			return false
		}
	}
	return true
}

// isDigit reports whether b is an ASCII digit.
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// monthNamesAValue reports whether a month field is followed by the separator the
// day would need, i.e. two digits and then a dash.
func monthNamesAValue(source string) bool {
	return len(source) > 2 && isDigit(source[0]) && isDigit(source[1]) && source[2] == '-'
}

// daysInMonth is the last day of a month, with the Gregorian leap-year rule.
func daysInMonth(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

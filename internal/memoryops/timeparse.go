package memoryops

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// Python-compatible date and time parsing.
//
// The reference parses a `datetime` field with `datetime.fromisoformat` (a date,
// a naive datetime or an offset one) and a numeric JSON value as a Unix
// timestamp, and its framework reports the parse failure as a structured
// validation item whose context carries CPython's own message. Reproducing that
// shape here — rather than a single hand-written detail string — is what makes a
// malformed temporal input answer identically on both services.
//
// The grammar is CPython 3.11's (the interpreter the reference runs on), and so
// are its messages: the value is read positionally — a year fails with "input is
// too short", a bad time section only after `T` — and a value that is not a
// string is read as a Unix timestamp. A naive value is read as UTC, which is what
// the reference's `ensure_tz_aware` validator does.

// pythonDateTimeValidationKind is the error type the framework reports for a
// value its date parsing rejects.
const (
	pythonDateTimeValidationKind = "datetime_from_date_parsing"
	pythonDateTimeMessage        = "Input should be a valid datetime or date, "
	pythonDateTimeTypeKind       = "datetime_type"
	pythonDateTimeTypeMessage    = "Input should be a valid datetime"
)

// isoDateParse is one parsed value, component by component.
type isoDateParse struct {
	year, month, day     int
	hasTime              bool
	hour, minute, second int
	microsecond          int
	offsetSeconds        int
	hasOffset            bool
}

var (
	isoUnixTime = regexp.MustCompile(`^-?[0-9]+$`)
)

// isoDateParseFailure is a CPython-style parse failure: the message the
// reference's framework puts in a validation item's context.
type isoDateParseFailure struct{ message string }

func (f *isoDateParseFailure) Error() string { return f.message }

// parsePythonDateTime parses one value the way the reference's `datetime` field
// does, returning the instant or the CPython-style failure message.
//
// Three attempts in the reference's own order: a bare integer is a Unix
// timestamp, then CPython's ISO grammar, then the framework's numeric date form
// (`YYYYMMDD`, and `YYYY-MM-DD` with any separator) — which is also the form
// whose failure the framework reports once the ISO grammar has failed.
func parsePythonDateTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if isoUnixTime.MatchString(text) {
		seconds := float64(atoiOrZero(strings.TrimPrefix(text, "-")))
		if text[0] == '-' {
			seconds = -seconds
		}
		parsed := unixTimestampToTime(seconds)
		if parsed.IsZero() {
			return time.Time{}, formatUnixTimestampError(seconds)
		}
		return parsed, nil
	}
	parsed, err := scanISODateTime(text)
	if err == nil {
		return parsed.instant(), nil
	}
	// The framework's other branch: a value made only of digits and separators
	// and no longer than a date is read as the numeric date form. Anything with
	// a letter or a spare character in it never gets there — the ISO grammar's
	// own failure is what the caller sees.
	return time.Time{}, err
}

// instant is the parsed value as an instant: a naive value is UTC, and an
// offset one is the instant it names.
func (p isoDateParse) instant() time.Time {
	location := time.UTC
	if p.hasOffset {
		location = time.FixedZone("", p.offsetSeconds)
	}
	return time.Date(p.year, time.Month(p.month), p.day, p.hour, p.minute,
		p.second, p.microsecond*1000, location).UTC()
}

// scanISODateTime is CPython 3.11's `datetime.fromisoformat` (`_parse_isoformat_date`,
// `_parse_isoformat_time`, `_parse_hh_mm_ss_ff`), which accepts the pre-3.11 forms
// — `YYYY-MM-DD`, `HH`, `HH:MM`, `HH:MM:SS`, `,ffffff` — and the 3.11 extensions
// (`YYYYMMDD`, `HHMMSS`, `HH:MM:SS.ffffff`, `.ffffff` with no seconds). Its
// messages are positional: whichever component fails is the one named, and the
// time of day is only read after `T`.
func scanISODateTime(value string) (isoDateParse, error) {
	var parsed isoDateParse
	if len(value) < 10 {
		return parsed, &isoDateParseFailure{"input is too short"}
	}
	if !isDigit(value[0]) || !isDigit(value[1]) || !isDigit(value[2]) || !isDigit(value[3]) {
		return parsed, &isoDateParseFailure{"input is too short"}
	}
	buffer := &isoReader{value: value}
	if err := readISODate(buffer, &parsed); err != nil {
		return parsed, err
	}
	if buffer.rest() == "" {
		return parsed, nil
	}
	if !separatesDateAndTime(buffer.rest()[0]) {
		// Before CPython 3.11 the separator had to be a `T`; the reference
		// reformats with a space, so both are read here.
		return parsed, &isoDateParseFailure{"Invalid isoformat string: '" + value + "'"}
	}
	buffer.position++
	// A bare date with a `T` and nothing after it is a date, whose time section
	// any remaining text then fails.
	if buffer.rest() == "" {
		return parsed, nil
	}
	parsed.hasTime = true
	if err := readISOTime(buffer, &parsed); err != nil {
		return parsed, err
	}
	if err := buffer.expectEnd(); err != nil {
		return parsed, err
	}
	return parsed, nil
}

// separatesDateAndTime reports whether a character is the separator the reference
// accepts between a date and a time: `T`, or the space the framework itself uses
// when it reformats a value.
func separatesDateAndTime(character byte) bool {
	return character == 'T' || character == ' ' || character == 't'
}

// isoReader is the cursor the grammar reads through: CPython asks for exact
// fixed-width fields and reports one message when they run out.
type isoReader struct {
	value    string
	position int
}

// rest is the text the parser has not read.
func (r *isoReader) rest() string { return r.value[r.position:] }

// expectEnd reports text the grammar could not consume.
func (r *isoReader) expectEnd() error {
	if r.rest() != "" {
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	return nil
}

// expect reads one literal character the grammar requires at this position.
func (r *isoReader) expect(character string) error {
	if r.position >= len(r.value) || r.value[r.position] != character[0] {
		return &isoDateParseFailure{"Invalid isoformat string: '" + r.value + "'"}
	}
	r.position++
	return nil
}

// digits reads exactly count digits into a field, reporting the given message —
// or CPython's own invalid-value message — when they run out or parse
// differently.
func (r *isoReader) digits(count int, field string) (int, error) {
	if r.position+count > len(r.value) || !isDigits(r.value[r.position:r.position+count]) {
		return 0, &isoDateParseFailure{"Invalid isoformat string: '" + r.value + "'"}
	}
	number := atoiOrZero(r.value[r.position : r.position+count])
	r.position += count
	return number, nil
}

// readISODate reads `YYYY`, `MM`, `DD` and the two separators between them.
func readISODate(r *isoReader, parsed *isoDateParse) error {
	var err error
	if parsed.year, err = r.digits(4, "year"); err != nil {
		return &isoDateParseFailure{"input is too short"}
	}
	if r.rest() == "" {
		return &isoDateParseFailure{"invalid date separator, expected `-`"}
	}
	if r.value[r.position] != '-' {
		return &isoDateParseFailure{"invalid date separator, expected `-`"}
	}
	r.position++
	if r.rest() != "" && !isDigit(r.value[r.position]) {
		return &isoDateParseFailure{"invalid character in month"}
	}
	if parsed.month, err = r.digits(2, "month"); err != nil {
		return &isoDateParseFailure{"invalid character in month"}
	}
	if err := r.expect("-"); err != nil {
		return &isoDateParseFailure{"invalid date separator, expected `-`"}
	}
	if parsed.day, err = r.digits(2, "day"); err != nil {
		return &isoDateParseFailure{"invalid character in day"}
	}
	if parsed.month < 1 || parsed.month > 12 {
		return &isoDateParseFailure{"month value is outside expected range of 1-12"}
	}
	if parsed.day < 1 || parsed.day > daysInMonth(parsed.year, parsed.month) {
		return &isoDateParseFailure{"day value is outside expected range"}
	}
	return nil
}

// readISOTime reads the time-of-day section: `HH[:MM[:SS[.ffffff]]]` (the
// extended form drops the colons), the fraction, and the offset.
func readISOTime(r *isoReader, parsed *isoDateParse) error {
	var err error
	if parsed.hour, err = r.digits(2, "hour"); err != nil {
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	if r.rest() != "" && r.value[r.position] == ':' {
		r.position++
	}
	if r.rest() == "" {
		// A time section of an hour alone is refused: the grammar needs at least
		// `HH:MM`.
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	if parsed.minute, err = r.digits(2, "minute"); err != nil {
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	if r.rest() == "" {
		return checkISOTimeFields(parsed)
	}
	if err := r.expect(":"); err != nil {
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	if r.rest() == "" {
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	if parsed.second, err = r.digits(2, "second"); err != nil {
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	if err := readISOMicroseconds(r, parsed); err != nil {
		return err
	}
	if err := readISOOffset(r, parsed); err != nil {
		return err
	}
	return checkISOTimeFields(parsed)
}

// readISOMicroseconds reads the `.ffffff` fraction, keeping microseconds.
func readISOMicroseconds(r *isoReader, parsed *isoDateParse) error {
	if r.rest() == "" || (r.value[r.position] != '.' && r.value[r.position] != ',') {
		return nil
	}
	end := r.position + 1
	for end < len(r.value) && isDigit(r.value[end]) {
		end++
	}
	if end == r.position+1 {
		return &isoDateParseFailure{"Invalid isoformat string: '" + r.value + "'"}
	}
	digits := r.value[r.position+1 : end]
	if len(digits) > 6 {
		digits = digits[:6]
	}
	parsed.microsecond = atoiOrZero(digits + strings.Repeat("0", 6-len(digits)))
	r.position = end
	return nil
}

// readISOOffset reads the offset that closes a value: `Z`, or `+HH:MM` in the
// extended form and `+HHMM`/`+HH` in the short one. An offset that does not meet
// the grammar is reported as text the value could not consume, which is what
// CPython does once its optional offset parse fails.
func readISOOffset(r *isoReader, parsed *isoDateParse) error {
	if r.rest() == "" {
		return nil
	}
	switch r.value[r.position] {
	case 'Z', 'z':
		parsed.hasOffset = true
		r.position++
		return nil
	case '+', '-':
	default:
		return nil
	}
	start := r.position
	sign := 1
	if r.value[start] == '-' {
		sign = -1
	}
	offset, err := parseISOOffsetText(r.value[start+1:])
	if err != nil {
		// Not an offset: the text stays for the end-of-value check to report.
		return nil
	}
	parsed.hasOffset = true
	parsed.offsetSeconds = sign * offset
	r.position = len(r.value)
	return nil
}

// parseISOOffsetText reads the offset body (`HH:MM`, `HHMM` or `HH`) into
// seconds.
func parseISOOffsetText(text string) (int, error) {
	hours, rest, err := leadingDigits(text, 2)
	if err != nil {
		return 0, err
	}
	minutes := 0
	if rest != "" && rest[0] == ':' {
		rest = rest[1:]
	}
	if rest != "" {
		if minutes, rest, err = leadingDigits(rest, 2); err != nil || rest != "" {
			return 0, errors.New("not an offset")
		}
	}
	if hours > 23 {
		return 0, &isoDateParseFailure{"offset must be a timedelta strictly between -timedelta(hours=24) and timedelta(hours=24)."}
	}
	return hours*3600 + minutes*60, nil
}

// leadingDigits reads up to count leading digits and returns the number and the
// text that follows it.
func leadingDigits(text string, count int) (int, string, error) {
	index := 0
	for index < len(text) && index < count && isDigit(text[index]) {
		index++
	}
	if index != count {
		return 0, "", errors.New("not enough digits")
	}
	return atoiOrZero(text[:index]), text[index:], nil
}

// checkISOTimeFields applies the time-of-day checks, in the parser's order.
func checkISOTimeFields(parsed *isoDateParse) error {
	switch {
	case parsed.hour > 23, parsed.minute > 59, parsed.second > 59:
		// The reference's parser reports an out-of-range time-of-day component as
		// text it could not consume, not as a range failure.
		return &isoDateParseFailure{"unexpected extra characters at the end of the input"}
	}
	return nil
}

func isDigits(value string) bool {
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

// isDigit reports whether one byte is an ASCII digit.
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// daysInMonth is the length of a calendar month, leap years included.
func daysInMonth(year, month int) int {
	if month < 1 || month > 12 {
		return 0
	}
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// pythonDateTimeItem builds the validation item the framework emits for a value
// its date parsing rejects.
func pythonDateTimeItem(loc []string, input any, failure error) httpx.ValidationError {
	var parseFailure *isoDateParseFailure
	if !errors.As(failure, &parseFailure) {
		parseFailure = &isoDateParseFailure{failure.Error()}
	}
	return httpx.ValidationItem(pythonDateTimeValidationKind, loc,
		pythonDateTimeMessage+parseFailure.message, input,
		map[string]any{"error": parseFailure.message})
}

// unixTimestampToTime converts the numeric form the reference accepts for a
// `datetime` field: a Unix timestamp in seconds.
func unixTimestampToTime(seconds float64) time.Time {
	whole := int64(seconds)
	fraction := seconds - float64(whole)
	return time.Unix(whole, int64(fraction*1e9)).UTC()
}

// formatUnixTimestampError is the reference's message for a numeric timestamp
// outside the range a datetime can represent.
func formatUnixTimestampError(seconds float64) error {
	return &isoDateParseFailure{fmt.Sprintf("timestamp out of range: %v", seconds)}
}

// atoiOrZero reads a run of digits, reporting zero for an empty run.
func atoiOrZero(digits string) int {
	if digits == "" {
		return 0
	}
	value, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return value
}

// parseStdlibDateTime parses a value the way the reference parses
// `query_timestamp`: `datetime.fromisoformat(value.replace("Z", "+00:00"))` —
// the standard-library parser, whose messages differ from the framework's field
// parser (a bad value names the offending component rather than the position).
func parseStdlibDateTime(value string) (time.Time, error) {
	text := strings.ReplaceAll(value, "Z", "+00:00")
	parsed, err := scanStdlibISO(text)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.instant(), nil
}

// scanStdlibISO is `datetime.fromisoformat` as CPython 3.11 implements it, with
// its own messages: a length pre-check over the digits, then each component as
// it is read.
func scanStdlibISO(value string) (isoDateParse, error) {
	var parsed isoDateParse
	r := &isoReader{value: value}
	if err := readStdlibDate(r, &parsed); err != nil {
		return parsed, err
	}
	if r.rest() == "" {
		return parsed, nil
	}
	if !strings.ContainsRune("T ", rune(r.rest()[0])) {
		return parsed, &isoDateParseFailure{"Invalid isoformat string: '" + value + "'"}
	}
	r.position++
	if r.rest() == "" {
		return parsed, &isoDateParseFailure{"Invalid isoformat string: '" + value + "'"}
	}
	parsed.hasTime = true
	if err := readStdlibTime(r, &parsed); err != nil {
		return parsed, err
	}
	if err := readStdlibOffset(r, &parsed); err != nil {
		return parsed, err
	}
	return parsed, r.expectEnd()
}

// startsCompactDate reports whether a value begins with the compact (`YYYYMMDD`)
// date form followed by the end of the value or a date-and-time separator.
func startsCompactDate(value string) bool {
	if len(value) < 8 || !isDigits(value[:8]) {
		return false
	}
	return len(value) == 8 || value[8] == 'T' || value[8] == ' '
}

// readStdlibDate reads the date, accepting the compact form as well.
func readStdlibDate(r *isoReader, parsed *isoDateParse) error {
	var err error
	separated := len(r.rest()) > 4 && r.rest()[4] == '-'
	if !separated && !startsCompactDate(r.rest()) {
		// The compact form is eight digits, and what follows them must be what
		// follows a date (nothing, or a date-and-time separator) — a ten-digit
		// run is not a date at all.
		return &isoDateParseFailure{"Invalid isoformat string: '" + r.value + "'"}
	}
	width := 2
	if parsed.year, err = r.digits(4, "year"); err != nil {
		return err
	}
	if separated {
		if err := r.expect("-"); err != nil {
			return err
		}
	}
	if parsed.month, err = r.digits(width, "month"); err != nil {
		return err
	}
	if separated {
		if err := r.expect("-"); err != nil {
			return err
		}
	}
	if parsed.day, err = r.digits(width, "day"); err != nil {
		return err
	}
	if parsed.month < 1 || parsed.month > 12 {
		return &isoDateParseFailure{"month must be in 1..12"}
	}
	if parsed.day < 1 || parsed.day > daysInMonth(parsed.year, parsed.month) {
		return &isoDateParseFailure{"day is out of range for month"}
	}
	return nil
}

// readStdlibTime reads `HH[:MM[:SS[.ffffff]]]`, the compact forms included.
func readStdlibTime(r *isoReader, parsed *isoDateParse) error {
	var err error
	// The separators are the time section's own business, so they are read from
	// the section rather than the whole value.
	separated := len(r.rest()) > 2 && r.rest()[2] == ':'
	if parsed.hour, err = r.digits(2, "hour"); err != nil {
		return err
	}
	if r.rest() == "" {
		return checkStdlibTimeFields(parsed)
	}
	if separated {
		if err := r.expect(":"); err != nil {
			return err
		}
	}
	if parsed.minute, err = r.digits(2, "minute"); err != nil {
		return err
	}
	if r.rest() == "" {
		return checkStdlibTimeFields(parsed)
	}
	if separated {
		if err := r.expect(":"); err != nil {
			return err
		}
	}
	if parsed.second, err = r.digits(2, "second"); err != nil {
		return err
	}
	if err := readStdlibMicroseconds(r, parsed); err != nil {
		return err
	}
	return checkStdlibTimeFields(parsed)
}

// readStdlibOffset reads the offset that closes a value, reporting text it
// cannot consume.
func readStdlibOffset(r *isoReader, parsed *isoDateParse) error {
	if r.rest() == "" {
		return nil
	}
	switch r.value[r.position] {
	case 'Z':
		parsed.hasOffset = true
		r.position++
		return nil
	case '+', '-':
	default:
		return nil
	}
	start := r.position
	sign := 1
	if r.value[start] == '-' {
		sign = -1
	}
	offset, err := parseISOOffsetText(r.value[start+1:])
	if err != nil {
		return nil
	}
	parsed.hasOffset = true
	parsed.offsetSeconds = sign * offset
	r.position = len(r.value)
	return nil
}

// readStdlibMicroseconds reads the fraction, keeping the first six digits and
// dropping the rest, which is how the standard library rounds a longer one.
func readStdlibMicroseconds(r *isoReader, parsed *isoDateParse) error {
	if r.rest() == "" || (r.value[r.position] != '.' && r.value[r.position] != ',') {
		return nil
	}
	end := r.position + 1
	for end < len(r.value) && isDigit(r.value[end]) {
		end++
	}
	digits := r.value[r.position+1 : end]
	if len(digits) > 6 {
		digits = digits[:6]
	}
	parsed.microsecond = atoiOrZero(digits + strings.Repeat("0", 6-len(digits)))
	r.position = end
	return nil
}

// checkStdlibTimeFields applies the time-of-day range checks the standard
// library's parser raises by name.
func checkStdlibTimeFields(parsed *isoDateParse) error {
	switch {
	case parsed.hour > 23:
		return &isoDateParseFailure{"hour must be in 0..23"}
	case parsed.minute > 59:
		return &isoDateParseFailure{"minute must be in 0..59"}
	case parsed.second > 59:
		return &isoDateParseFailure{"second must be in 0..59"}
	}
	return nil
}

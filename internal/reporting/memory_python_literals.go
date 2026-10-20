package reporting

import (
	"strconv"
	"strings"
	"time"
)

// The reference validates its query parameters in Python, so the values it
// accepts are the ones CPython's own parsers accept — not the ones a Go parser
// does. The date parser lives here, beside the memory listing that is its
// caller, because a query parameter's contract is the reference's parser rather
// than "an ISO datetime" in the abstract. The integer coercion is
// frameworkIntFromString in observation_params.go, which the listing uses too.

// pythonISODateTime parses an ISO-8601 value the way CPython's
// `datetime.fromisoformat` (3.11+) does, which is what the reference's query
// parameters are validated with. Compared with RFC 3339 it also accepts:
//
//   - a basic (separator-free) date, optionally followed by a `T`/space time:
//     `20240101`, `20240101T000000`, `20240101 00:00`;
//   - a hyphenated date with a basic clock: `2024-01-01T0000`;
//   - an hour alone (`2024-01-01T00`) or an hour and minute (`…T00:00`);
//   - a fractional part introduced by either `.` or `,`;
//   - a numeric offset of the forms ±HH, ±HHMM, ±HH:MM, and the extended
//     ±HH:MM:SS[.ffffff].
//
// The caller reads a value with no offset as UTC. The returned error is
// advisory: the reference answers the same 400 for every rejected value.
func pythonISODateTime(value string) (time.Time, error) {
	s := value
	// A trailing Z is rewritten rather than passed through, keeping the accepted
	// syntax pinned to this function.
	if strings.HasSuffix(s, "Z") {
		s = s[:len(s)-1] + "+00:00"
	}
	if !strings.ContainsRune(s, 'T') && !strings.ContainsRune(s, ' ') {
		if parsed, ok := parseISODatePart(s, "2006-01-02"); ok {
			return parsed, nil
		}
		if parsed, ok := parseISODatePart(s, "20060102"); ok {
			return parsed, nil
		}
		return time.Time{}, errNotISODateTime
	}
	datePart, timePart, found := strings.Cut(s, "T")
	if !found {
		datePart, timePart, _ = strings.Cut(strings.Replace(s, " ", "T", 1), "T")
	}
	date, ok := parseISODatePart(datePart, "2006-01-02")
	if !ok {
		if date, ok = parseISODatePart(datePart, "20060102"); !ok {
			return time.Time{}, errNotISODateTime
		}
	}
	clock, fraction, offset := splitISOTimePart(timePart)
	hour, minute, second, ok := splitISOClock(clock)
	if !ok {
		return time.Time{}, errNotISODateTime
	}
	nanos := int64(0)
	if fraction != "" {
		digits := fraction
		if len(digits) > 9 {
			// A sub-nanosecond digit is below this clock's resolution; the
			// instant is otherwise the same.
			digits = digits[:9]
		}
		padded := digits + strings.Repeat("0", 9-len(digits))
		parsed, err := strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return time.Time{}, errNotISODateTime
		}
		nanos = parsed
	}
	location := time.UTC
	if offset != "" {
		zone, ok := parseISOOffset(offset)
		if !ok {
			return time.Time{}, errNotISODateTime
		}
		location = zone
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hour, minute, second, int(nanos), location), nil
}

// errNotISODateTime is the uniform failure of pythonISODateTime; the reference
// renders every rejected value with the same sentence.
var errNotISODateTime = errISO("value is not an ISO-8601 datetime")

type errISO string

func (e errISO) Error() string { return string(e) }

// parseISODatePart parses a date in one exact layout.
func parseISODatePart(value, layout string) (time.Time, bool) {
	parsed, err := time.ParseInLocation(layout, value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// splitISOTimePart separates the clock, the fractional seconds and the offset
// of a time-of-day value. The fraction is introduced by "." or ","; the offset
// by "Z" (already rewritten by the caller), "+" or "-".
func splitISOTimePart(value string) (clock, fraction, offset string) {
	rest := value
	if i := strings.IndexAny(rest, "+-"); i >= 0 {
		offset = rest[i:]
		rest = rest[:i]
	}
	if i := strings.IndexAny(rest, ".,"); i >= 0 {
		fraction = rest[i+1:]
		rest = rest[:i]
	}
	return rest, fraction, offset
}

// splitISOClock reads an hour, an hour and minute, or an hour, minute and
// second, in either the extended (HH:MM[:SS]) or basic (HHMM[SS]) spelling.
func splitISOClock(clock string) (hour, minute, second int, ok bool) {
	var fields []string
	if strings.ContainsRune(clock, ':') {
		fields = strings.Split(clock, ":")
		if len(fields) > 3 {
			return 0, 0, 0, false
		}
	} else {
		switch len(clock) {
		case 2:
			fields = []string{clock}
		case 4:
			fields = []string{clock[:2], clock[2:]}
		case 6:
			fields = []string{clock[:2], clock[2:4], clock[4:]}
		default:
			return 0, 0, 0, false
		}
	}
	parts := make([]int, len(fields))
	for i, field := range fields {
		if len(field) != 2 {
			return 0, 0, 0, false
		}
		parsed, err := strconv.Atoi(field)
		if err != nil {
			return 0, 0, 0, false
		}
		parts[i] = parsed
	}
	hour = parts[0]
	if len(parts) > 1 {
		minute = parts[1]
	}
	if len(parts) > 2 {
		second = parts[2]
	}
	// The bounds are the model's, so an out-of-range clock is a malformed value
	// here just as it is a ValueError there.
	if hour > 23 || minute > 59 || second > 59 {
		return 0, 0, 0, false
	}
	return hour, minute, second, true
}

// parseISOOffset reads a UTC offset: ±HH, ±HHMM, ±HH:MM, ±HH:MM:SS or
// ±HH:MM:SS.ffffff.
func parseISOOffset(value string) (*time.Location, bool) {
	if value == "" || (value[0] != '+' && value[0] != '-') {
		return nil, false
	}
	sign := 1
	if value[0] == '-' {
		sign = -1
	}
	rest := value[1:]
	var hh, mm, ss int
	if strings.ContainsRune(rest, ':') {
		fields := strings.Split(rest, ":")
		if len(fields) < 2 || len(fields) > 3 {
			return nil, false
		}
		for _, field := range fields {
			if len(field) != 2 {
				return nil, false
			}
		}
		hh, _ = strconv.Atoi(fields[0])
		mm, _ = strconv.Atoi(fields[1])
		if len(fields) == 3 {
			ss, _ = strconv.Atoi(fields[2])
		}
		// A fraction on the seconds is below this clock's resolution for a
		// zone offset and is discarded, as Python's fixed-offset zone keeps it
		// only for display.
	} else {
		switch len(rest) {
		case 2:
			hh, _ = strconv.Atoi(rest)
		case 4:
			hh, _ = strconv.Atoi(rest[:2])
			mm, _ = strconv.Atoi(rest[2:])
		default:
			return nil, false
		}
	}
	if hh > 23 || mm > 59 || ss > 59 {
		return nil, false
	}
	offset := sign * (hh*3600 + mm*60 + ss)
	return time.FixedZone("", offset), true
}

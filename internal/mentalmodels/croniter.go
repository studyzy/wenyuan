package mentalmodels

import (
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"
)

// The reference validates a trigger's cron members with croniter.is_valid
// (api/http.py:3486-3497), whose grammar is wider than the robfig standard
// parser this service schedules with: a sixth seconds column, day-of-week 7
// (Sunday), "L" in day-of-month and "#nth" day-of-week all bind where robfig
// refuses them. A trigger the reference accepts must not be refused here, so
// validCron falls back to this checker, which reads the expression the way
// croniter 6.2.2 does:
//
//   - exactly five or six whitespace-separated columns; on the six-column form
//     the LAST column is the seconds field (croniter moves a leading second to
//     the front only when the caller asks for it, which the reference does not),
//     and a seventh column — croniter's year field — is not accepted.
//   - the extended fields (seconds, day-of-month, day-of-week) are validated
//     against croniter's own field grammar: a comma list of elements, each a
//     wildcard (with an optional step), a bare value, an open-ended
//     "value/step" running to the maximum, or a "low-high" range with an
//     optional "/step". A bare value must sit inside the field's bounds; a
//     range checks its upper bound only, as croniter's guard does. A range
//     whose bounds are equal covers the whole cycle, and a wrapped range (the
//     low bound past the high one, which the day-of-week aliases produce)
//     covers the cycle end to start, resuming the step from the bottom as
//     croniter's X-Sun handling does. Names bind where croniter allows them
//     (weekdays on day-of-week), and croniter's legacy aliases apply per
//     column count: day-of-week 7 reads as 0 on the five-field form,
//     day-of-month 0 reads as 1 on the six-field one, and neither alias
//     applies on the other spelling (croniter's value_alias).
//   - the day-of-month extensions: "L" as the whole field is the last day of
//     the month, and "L" as a range's high bound reads as the maximum (31).
//   - the day-of-week extensions: "weekday#n" and "low-high#n" name the nth
//     weekday of the month, with n in 1..5, and a #nth element may not mix
//     with literal weekdays unless the field expands to the whole week —
//     croniter files that mix as unsupported syntax, which is invalid.
//   - the standard fields (minute, hour, month) stay on robfig's grammar: each
//     is probed through cron.ParseStandard with the other columns fixed, so a
//     croniter-only form those fields do not declare (a wrapped range such as
//     "59-0") is still refused.
//
// The checker answers validity only: the stored trigger keeps the expression
// the client sent, and the scheduling loop still parses with robfig (a stored
// expression robfig cannot expand never comes due — see internal/app/cron.go).
func croniterValid(expression string) bool {
	fields := strings.Fields(expression)
	columns := len(fields)
	switch columns {
	case 5:
	case 6:
		if !croniterFieldValid(fields[5], secondsField) {
			return false
		}
		fields = fields[:5]
	default:
		return false
	}
	// Minute, hour and month declare no croniter extension, so their grammar is
	// the standard parser's: probe each through robfig in its own position.
	for _, position := range [...]int{0, 1, 3} {
		if !robfigFieldValid(fields[position], position) {
			return false
		}
	}
	// The day fields carry the extensions, each with the aliases its column
	// count allows.
	return croniterFieldValid(fields[2], dayOfMonthField(columns)) &&
		croniterFieldValid(fields[4], dayOfWeekField(columns))
}

// cronFieldSpec is one field's croniter grammar: the inclusive bounds, the
// legacy value aliases (croniter's LOWMAP), the names it binds, and the
// extensions it declares.
type cronFieldSpec struct {
	min, max int
	lowmap   map[int]int
	names    map[string]int
	// questionMark allows a lone "?" as the whole field, which croniter trades
	// for the wildcard on the two day fields only.
	questionMark bool
	// lastDay allows "L" as the whole field or a range's high bound
	// (day-of-month only).
	lastDay bool
	// nthWeekday allows "weekday#n" elements (day-of-week only).
	nthWeekday bool
}

// secondsField is the six-column form's last column: numeric only, 0..59, no
// names, no aliases and no day-field extensions.
var secondsField = cronFieldSpec{min: 0, max: 59}

// dayOfMonthField builds the day-of-month grammar. croniter's LOWMAP maps 0 to
// 1 here only on the six-column form ("do not support 0 as a month either for
// classical 5 fields cron, 6fields second repeat form or 7 fields year form").
func dayOfMonthField(columns int) cronFieldSpec {
	spec := cronFieldSpec{min: 1, max: 31, questionMark: true, lastDay: true}
	if columns == 6 {
		spec.lowmap = map[int]int{0: 1}
	}
	return spec
}

// dayOfWeekField builds the day-of-week grammar. The 7-as-Sunday alias applies
// only on the five-column form; on the six-column one a 7 is out of bands.
func dayOfWeekField(columns int) cronFieldSpec {
	spec := cronFieldSpec{min: 0, max: 6, questionMark: true, nthWeekday: true,
		names: map[string]int{
			"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
		}}
	if columns == 5 {
		spec.lowmap = map[int]int{7: 0}
	}
	return spec
}

// robfigFieldValid probes one standard column through cron.ParseStandard with
// the remaining columns fixed at valid values, so the field keeps robfig's own
// grammar (bounds, names, ranges, steps and its "*" and "?" wildcards).
func robfigFieldValid(field string, position int) bool {
	probe := [...]string{"0", "0", "1", "1", "0"}
	probe[position] = field
	_, err := cron.ParseStandard(probe[0] + " " + probe[1] + " " + probe[2] + " " + probe[3] + " " + probe[4])
	return err == nil
}

// croniterFieldValid reports whether one field is a comma list of elements the
// spec's grammar binds, with the day-of-week nth-mixing rule applied.
func croniterFieldValid(field string, spec cronFieldSpec) bool {
	field = strings.ToLower(field)
	if field == "?" {
		return spec.questionMark
	}
	// nth holds, per covered weekday, the occurrences a "#n" element names; it
	// stays nil on a field with none.
	var nth map[int]map[int]bool
	values := map[int]bool{}
	for _, element := range strings.Split(field, ",") {
		if spec.nthWeekday {
			if index := strings.Index(element, "#"); index >= 0 {
				occurrence, ok := cronDigits(element[index+1:])
				// croniter asserts 5 >= nth >= 1 and refuses the rest.
				if !ok || occurrence < 1 || occurrence > 5 {
					return false
				}
				// The prefix is croniter's weekday, weekday range or bare word
				// alternative: a wildcard or any other non-word character is
				// not a prefix at all, and the expansion refuses what no name
				// or number binds.
				if !croniterWordPrefix(element[:index]) {
					return false
				}
				covered, ok := expandCronElement(element[:index], spec)
				if !ok {
					return false
				}
				if nth == nil {
					nth = map[int]map[int]bool{}
				}
				for value := range covered {
					if nth[value] == nil {
						nth[value] = map[int]bool{}
					}
					nth[value][occurrence] = true
					values[value] = true
				}
				continue
			}
		}
		covered, ok := expandCronElement(element, spec)
		if !ok {
			return false
		}
		for value := range covered {
			values[value] = true
		}
	}
	// Mixing literal weekdays with an nth weekday is unsupported syntax unless
	// the field expands to the whole week (croniter's
	// CroniterUnsupportedSyntaxError, which is_valid counts as invalid).
	if nth != nil {
		literals := 0
		for value := range values {
			if _, carried := nth[value]; !carried {
				literals++
			}
		}
		if literals > 0 && len(values) != spec.max-spec.min+1 {
			return false
		}
	}
	return true
}

// expandCronElement expands one element to the cycle values it covers,
// reporting whether the element is valid. A day-of-month "L" covers no value:
// it is the last-day marker itself, which only the field's grammar has to
// accept.
func expandCronElement(element string, spec cronFieldSpec) (map[int]bool, bool) {
	step := 1
	hasStep := false
	if parts := strings.Split(element, "/"); len(parts) == 2 {
		parsed, ok := cronDigits(parts[1])
		if !ok || parsed < 1 {
			// croniter refuses a non-numeric step; a zero step makes its
			// expansion's range call fail.
			return nil, false
		}
		step, hasStep = parsed, true
		element = parts[0]
	} else if len(parts) > 2 {
		return nil, false
	}
	if element == "*" {
		return stepRange(spec.min, spec.max, step, spec), true
	}
	if element == "l" {
		// A bare "l" is valid only where the grammar declares it (day-of-month);
		// elsewhere no name or number binds it, as in croniter's alphaconv.
		if spec.lastDay && !hasStep {
			return map[int]bool{}, true
		}
		return nil, false
	}
	lowAndHigh := strings.Split(element, "-")
	switch len(lowAndHigh) {
	case 1:
		value, ok := cronValue(lowAndHigh[0], spec)
		if !ok {
			return nil, false
		}
		if !hasStep {
			// A bare value sits inside the field's bounds, after the aliases.
			if value < spec.min || value > spec.max {
				return nil, false
			}
			return map[int]bool{value: true}, true
		}
		// An open-ended "value/step" runs from the value to the maximum, the
		// way croniter rewrites "10/5" to "10-59/5"; its guard is the range's
		// upper bound alone.
		if value > spec.max {
			return nil, false
		}
		return stepRange(value, spec.max, step, spec), true
	case 2:
		low, ok := cronValue(lowAndHigh[0], spec)
		if !ok {
			return nil, false
		}
		high := lowAndHigh[1]
		if high == "l" && spec.lastDay {
			// croniter reads a range's "l" high bound as the maximum day.
			high = strconv.Itoa(spec.max)
		}
		highValue, ok := cronValue(high, spec)
		if !ok {
			return nil, false
		}
		if low > spec.max || highValue > spec.max {
			// croniter's range guard checks the upper bound only.
			return nil, false
		}
		switch {
		case low == highValue:
			// A range bound to itself covers the whole cycle ("Sun-Sun").
			return stepRange(spec.min, spec.max, step, spec), true
		case low > highValue:
			return wrappedRange(low, highValue, step, spec), true
		default:
			return stepRange(low, highValue, step, spec), true
		}
	default:
		return nil, false
	}
}

// croniterWordPrefix reports whether a "#n" element's prefix is one croniter's
// special_dow_re can bind: a bare word, a number, or a name range. The wildcard
// and the question mark are not words, and the expansion refuses whatever no
// name or number binds.
func croniterWordPrefix(prefix string) bool {
	if prefix == "" {
		return false
	}
	for _, r := range prefix {
		if r == '-' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// cronValue reads one bound: a name the field binds, or a plain non-negative
// integer, mapped through the field's legacy aliases before the bounds are
// checked (croniter's value_alias).
func cronValue(text string, spec cronFieldSpec) (int, bool) {
	if value, ok := spec.names[text]; ok {
		return value, true
	}
	value, ok := cronDigits(text)
	if !ok {
		return 0, false
	}
	if mapped, aliased := spec.lowmap[value]; aliased {
		value = mapped
	}
	return value, true
}

// cronDigits parses a bare decimal count; croniter's only_int_re admits digits
// alone, so a sign or an empty text is refused even where an integer parse
// would succeed.
func cronDigits(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return value, true
}

// stepRange covers from..to stepping by step, skipping values below the
// field's minimum (croniter's range expansion checks no lower bound, so a
// range may start below it).
func stepRange(from, to, step int, spec cronFieldSpec) map[int]bool {
	out := map[int]bool{}
	for value := from; value <= to; value += step {
		if value < spec.min {
			continue
		}
		out[value] = true
	}
	return out
}

// wrappedRange covers a range whose low bound passed its high one: the cycle
// from the low bound to its end, then from the bottom to the high bound. The
// second leg resumes the step where the first left off, which is what keeps
// "5-7/3" a single value while "5-7/2" lands on Friday and Sunday (croniter's
// backtracking-range handling).
func wrappedRange(low, high, step int, spec cronFieldSpec) map[int]bool {
	out := stepRange(low, spec.max, step, spec)
	if len(out) == 0 {
		return stepRange(spec.min, high, step, spec)
	}
	whole := spec.max - spec.min + 1
	last := low + (len(out)-1)*step
	alreadySkipped := spec.max - last
	cursor := last - spec.min
	toSkip := 0
	if cursor+step > whole && alreadySkipped < step {
		toSkip = step - alreadySkipped
	}
	for value := spec.min + toSkip; value <= high; value += step {
		out[value] = true
	}
	return out
}

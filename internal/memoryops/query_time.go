package memoryops

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// monthNameOrder lists the month names in calendar order so a query naming one
// resolves the same way every run; ranging the map would not.
var monthNameOrder = []string{
	"january", "february", "march", "april", "may", "june",
	"july", "august", "september", "october", "november", "december",
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

// spelledNumbers maps an English number word to its value so a query that
// writes "three days ago" resolves like "3 days ago". The list stops at twelve
// because larger counts are written as digits in practice.
var spelledNumbers = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
}

// numberFragment is the regex alternation of every count the quantified
// patterns accept, digits or spelled out.
const numberFragment = `\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve`

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

// agoPattern matches "<n> <unit> ago" and "<unit> ago", where the count may be
// digits or a spelled-out number word.
var agoPattern = regexp.MustCompile(`\b(` + numberFragment + `)?\s*(day|days|week|weeks|month|months|year|years|quarter|quarters)\s+ago\b`)

// relativeSpanPattern matches "last|next|past|this|coming <unit>", where the
// unit may be a period word or a weekday.
var relativeSpanPattern = regexp.MustCompile(`\b(last|next|past|this|coming|previous)\s+(day|days|week|weeks|month|months|year|years|quarter|quarters|weekend|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)

// inSpanPattern matches "in the last <n> <unit>" and "in the past <n> <unit>".
var inSpanPattern = regexp.MustCompile(`\bin\s+the\s+(?:last|past)\s+(` + numberFragment + `)\s+(day|days|week|weeks|month|months|year|years)\b`)

// futureSpanPattern matches "in <n> <unit>", which names the window running
// forward from the reference day.
var futureSpanPattern = regexp.MustCompile(`\bin\s+(` + numberFragment + `)\s+(day|days|week|weeks|month|months|year|years)\b`)

// monthNameFragment is the alternation of English month names, shared by the
// patterns that key on one.
const monthNameFragment = `january|february|march|april|may|june|july|august|september|october|november|december`

// monthDayPattern matches "<month> <day>" and "<month> <day>, <year>".
var monthDayPattern = regexp.MustCompile(`\b(` + monthNameFragment + `)\s+(\d{1,2})(?:\s*,?\s*(\d{4}))?\b`)

// dayMonthPattern matches the day-first date form "<day> <month>[ <year>]",
// which names one exact date rather than the whole month.
var dayMonthPattern = regexp.MustCompile(`\b(\d{1,2})\s+(` + monthNameFragment + `)(?:\s*,?\s*(\d{4}))?\b`)

// monthYearPattern matches "<month> <year>": a month named with a year and no
// day, which names that whole calendar month.
var monthYearPattern = regexp.MustCompile(`\b(` + monthNameFragment + `)\s+(\d{4})\b`)

// ordinalMonthPattern matches "<n>th of <month>", an ordinal that names a day
// inside a stated month.
var ordinalMonthPattern = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)\s+of\s+(` + monthNameFragment + `)\b`)

// ordinalPattern matches "the <n>th" with no month named.
var ordinalPattern = regexp.MustCompile(`\bthe\s+(\d{1,2})(?:st|nd|rd|th)\b`)

// yearIntroPattern matches a four-digit year introduced by a word that makes it
// a year. Without one, an isolated integer is a port or a ticket number far
// more often than it is a date, so it is not read as a year.
var yearIntroPattern = regexp.MustCompile(`\b(?:in|during|throughout|year|en|durante|año|nel|anno|dans|pendant|année|im|während|jahr|в)\s+(\d{4})\b`)

// Chinese temporal expressions need their own rules. Chinese has no whitespace
// word boundaries for the English patterns to key on, and the named periods
// (weeks, months, years) mean calendar periods rather than the rolling windows
// "last week" produces. Only the shapes a query actually uses are recognised;
// anything else yields no window rather than a wrong one.

// chineseDayWordPattern matches the fixed day words, longer forms first so
// 大前天 is not read as 前天.
var chineseDayWordPattern = regexp.MustCompile(`(大大前天|大大后天|大前天|大后天|前天|后天|昨天|昨日|今天|今日|本日|明天|明日)`)

// chineseDayOffsets maps a fixed day word to its offset from the reference day.
var chineseDayOffsets = map[string]int{
	"大大前天": -4, "大前天": -3, "前天": -2, "昨天": -1, "昨日": -1,
	"今天": 0, "今日": 0, "本日": 0, "明天": 1, "明日": 1,
	"后天": 2, "大后天": 3, "大大后天": 4,
}

// chineseWeekPattern matches the calendar-week words. The captured 末 marks a
// weekend phrase, which is a two-day window the English side has no shape for.
var chineseWeekPattern = regexp.MustCompile(`(上上周|下下周|本周|这周|当周|上周|下周)(末?)`)

// chineseWeekOffsets maps a week word to how many weeks it is from the
// reference week.
var chineseWeekOffsets = map[string]int{
	"上上周": -2, "下下周": 2,
	"本周": 0, "这周": 0, "当周": 0, "上周": -1, "下周": 1,
}

// chineseMonthWordPattern matches the calendar-month words, longer forms first.
var chineseMonthWordPattern = regexp.MustCompile(`(上上个月|下下个月|上个月|下个月|上上月|下下月|本月|这月|当月|上月|下月)份?`)

// chineseMonthOffsets maps a month word to how many months it is from the
// reference month.
var chineseMonthOffsets = map[string]int{
	"上上个月": -2, "下下个月": 2, "上个月": -1, "下个月": 1,
	"上上月": -2, "下下月": 2, "本月": 0, "这月": 0, "当月": 0,
	"上月": -1, "下月": 1,
}

// chineseYearWordPattern matches the calendar-year words.
var chineseYearWordPattern = regexp.MustCompile(`(今年|本年|当年|去年|上年|明年)`)

// chineseYearOffsets maps a year word to how many years it is from the
// reference year.
var chineseYearOffsets = map[string]int{
	"今年": 0, "本年": 0, "当年": 0, "去年": -1, "上年": -1, "明年": 1,
}

// Chinese temporal expressions write counts as numerals, so the phrases below
// read them with parseChineseNumber rather than assuming digits.

// chineseNumberFragment matches a count written in digits or in Chinese
// numerals.
const chineseNumberFragment = `[0-9]+|[零〇○一二两俩三四五六七八九十百千万]+`

// chineseMonthFragment matches a month number written in digits or Chinese
// numerals, as it appears in "三月" or "12月".
const chineseMonthFragment = `十二|十一|十|[一二三四五六七八九]|1[0-2]|0?[1-9]`

// chinesePhaseFragment matches the words that name early, middle or late in a
// month. These stand alone, so the short forms are left out: a bare 中 or 末 in
// an ordinary sentence is not a month phase.
const chinesePhaseFragment = `上旬|中旬|下旬|月初|月中|月末|月底|月尾`

// chineseMonthPhaseFragment matches a phase immediately after a stated month,
// where the short forms are unambiguous: "3月末" is late March.
const chineseMonthPhaseFragment = chinesePhaseFragment + `|初|中|末|底|尾`

// chineseYearWordFragment matches the relative year words whose offsets are in
// chineseYearOffsets.
const chineseYearWordFragment = `今年|本年|当年|去年|上年|明年`

// chineseUnitFragment matches the period units the counted forms quantify.
const chineseUnitFragment = `天|日|周|星期|礼拜|月|年`

// chineseMeasureFragment matches the optional 个 between a count and its unit;
// "三个星期" and "3星期" mean the same thing.
const chineseMeasureFragment = `(?:一个|个|一)?`

// chineseDigitValues maps a Chinese numeral character to its value.
var chineseDigitValues = map[rune]int{
	'零': 0, '〇': 0, '○': 0,
	'一': 1, '两': 2, '俩': 2, '二': 2,
	'三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
}

// chinesePlaceValues maps a Chinese place unit to its value.
var chinesePlaceValues = map[rune]int{'十': 10, '百': 100, '千': 1000}

// chineseSinceGuard matches a count, a unit and a suffix that opens an interval
// running to the present ("三天以前"). The API models a closed range only, so
// these name no window rather than a wrong one.
var chineseSinceGuard = regexp.MustCompile(`(?:` + chineseNumberFragment + `)个?(?:` + chineseUnitFragment + `)(?:以前|之前)`)

// chineseStartGuard matches a future point followed by 起 or 开始 ("三天后开始"),
// which opens an interval rather than naming a day. It only counts when nothing
// follows the phrase, which is how the reference parser reads it: "三天后开始"
// is an open interval, while "三天后开始执行" is the point three days out with
// something happening there.
var chineseStartGuard = regexp.MustCompile(`(?:` + chineseNumberFragment + `)个?(?:` + chineseUnitFragment + `)(?:以?后|之后)(?:起|开始)$`)

// chinesePointOffset matches "<n> <unit> 前/后" (also written 以前/以后), an
// exact count of periods before or after the reference date, which names a
// single day. The leading group holds the character before the count, so a
// digit inside a longer number is not read as the count.
var chinesePointOffset = regexp.MustCompile(`(^|[^零〇○一二两俩三四五六七八九十百千万0-9])(` + chineseNumberFragment + `)个?(` + chineseUnitFragment + `)(以后|之后|前|后|後)`)

// Half a period is written 半, sometimes after a whole count ("一年半前").
var (
	chineseYearHalfPoint  = regexp.MustCompile(`(` + chineseNumberFragment + `)年半(前|后|後)`)
	chineseCountHalfPoint = regexp.MustCompile(`(` + chineseNumberFragment + `)个?半月(前|后|後)`)
	chineseHalfMonthPoint = regexp.MustCompile(`半个?月(前|后|後)`)
	chineseHalfYearPoint  = regexp.MustCompile(`半年(前|后|後)`)
)

// Chinese colloquialisms are approximate, so they cover a range instead of a
// point: "前几天" is not the same day as "三天前".
var (
	chineseFuzzyCoupleDays   = regexp.MustCompile(`前[两二](?:天|日)|一两(?:天|日)前|[两二]三(?:天|日)前|三两(?:天|日)前`)
	chineseFuzzyFewDays      = regexp.MustCompile(`前[几数](?:天|日)|[几数](?:天|日)前`)
	chineseFuzzyCoupleWeeks  = regexp.MustCompile(`一两个?(?:周|星期|礼拜)前|[两二]三(?:周|星期|礼拜)前|三两(?:周|星期|礼拜)前`)
	chineseFuzzyFewWeeks     = regexp.MustCompile(`前[几数]个?(?:周|星期|礼拜)|[几数]个?(?:周|星期|礼拜)前`)
	chineseFuzzyCoupleMonths = regexp.MustCompile(`一两个?月前|前一两个?月|[两二]三个月前|三两个月前`)
	chineseFuzzyFewMonths    = regexp.MustCompile(`前[几数]个?月|[几数]个?月前`)
	chineseFuzzyYears        = regexp.MustCompile(`一两年前|[两二]三年前|三两年前`)
)

// Rolling and bounded windows run either back from the reference day or forward
// from it.
var (
	chineseThisFew     = regexp.MustCompile(`这几个?(` + chineseUnitFragment + `)`)
	chineseThisCount   = regexp.MustCompile(`这(` + chineseNumberFragment + `)个?(` + chineseUnitFragment + `)`)
	chineseFrontDays   = regexp.MustCompile(`前(` + chineseNumberFragment + `)个?(天|日)`)
	chineseFrontPeriod = regexp.MustCompile(`前(` + chineseNumberFragment + `)个?(周|星期|礼拜|月|年)`)
	chinesePastFew     = regexp.MustCompile(`(?:过去|近|最近)几个?(` + chineseUnitFragment + `)`)
	chinesePastCount   = regexp.MustCompile(`(?:过去|近|最近)(` + chineseNumberFragment + `)个?(` + chineseUnitFragment + `)`)
	chinesePastHalf    = regexp.MustCompile(`(?:过去|近|最近)半个?(月|年)`)
	chineseFutureFew   = regexp.MustCompile(`(?:未来|接下来|往后)几个?(` + chineseUnitFragment + `)`)
	chineseFutureCount = regexp.MustCompile(`(?:未来|接下来|往后)(` + chineseNumberFragment + `)个?(` + chineseUnitFragment + `)`)
	chineseFutureHalf  = regexp.MustCompile(`(?:未来|接下来|往后)半个?(月|年)`)
	chineseWithinHalf  = regexp.MustCompile(`半个?(月|年)(?:以内|之内|内)`)
	chineseWithinCount = regexp.MustCompile(`(` + chineseNumberFragment + `)(个?)(` + chineseUnitFragment + `)(?:以内|之内|内)`)
)

// Month phases name a slice of a month: 上旬/月初 is the first ten days, 中旬/
// 月中 the second ten, and 下旬/月末 the rest. A phase with no month attached is
// the reference month's; a qualifier or a bare month number names another.
var (
	chineseAbsoluteMonthPhase = regexp.MustCompile(`(\d{4})年(` + chineseMonthFragment + `)月份?(` + chineseMonthPhaseFragment + `)`)
	chineseRelativeMonthPhase = regexp.MustCompile(`(` + chineseYearWordFragment + `)(` + chineseMonthFragment + `)月份?(` + chineseMonthPhaseFragment + `)`)
	chineseCurrentMonthPhase  = regexp.MustCompile(`(?:这|本|当)` + chineseMeasureFragment + `月份?(` + chineseMonthPhaseFragment + `)`)
	chineseNextMonthPhase     = regexp.MustCompile(`(?:下下|下)` + chineseMeasureFragment + `月份?(` + chineseMonthPhaseFragment + `)`)
	chinesePrevMonthPhase     = regexp.MustCompile(`(?:上上|上)` + chineseMeasureFragment + `月份?(` + chineseMonthPhaseFragment + `)`)
	chineseBareMonthPhase     = regexp.MustCompile(`(` + chineseMonthFragment + `)月份?(` + chineseMonthPhaseFragment + `)`)
	chineseBarePhase          = regexp.MustCompile(`(` + chinesePhaseFragment + `)`)
)

// chineseBareMonthPattern matches a month number standing on its own ("3月"),
// which names that whole calendar month.
var chineseBareMonthPattern = regexp.MustCompile(`(` + chineseMonthFragment + `)月份?`)

// Chinese absolute dates. The longer year-month-day form is matched first so
// "2026年3月15日" is not read as "2026年3月" or as the bare year 2026.
var (
	chineseYearMonthDayPattern = regexp.MustCompile(`(\d{4})年(\d{1,2})月(\d{1,2})日`)
	chineseYearMonthPattern    = regexp.MustCompile(`(\d{4})年(\d{1,2})月`)
	chineseMonthDayPattern     = regexp.MustCompile(`(\d{1,2})月(\d{1,2})日`)
)

// temporalWindowFromQuery derives the window a query's text names, or nil when
// it names none. The reference date anchors every relative phrase, so the same
// query resolves differently a year apart, which is what a relative phrase
// means.
func temporalWindowFromQuery(query string, reference time.Time) *storage.TemporalWindow {
	lowered := strings.ToLower(query)
	ref := reference.UTC()
	day := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)

	// Chinese expressions are explicit and calendar-based, and the English
	// word-boundary patterns cannot see them, so they are tried first.
	if window := chineseWindowFromQuery(query, day); window != nil {
		return window
	}
	// "in the last N units" is the most specific span, so it is tried first.
	if match := inSpanPattern.FindStringSubmatch(lowered); match != nil {
		if count, ok := countValue(match[1]); ok && count > 0 {
			if days, ok := unitDays[match[2]]; ok {
				return dayRange(day.AddDate(0, 0, -count*days), day)
			}
		}
	}
	// "<n> units ago" names a single day, some distance back.
	if match := agoPattern.FindStringSubmatch(lowered); match != nil {
		days, ok := unitDays[match[2]]
		if !ok {
			return nil
		}
		count, ok := countValue(match[1])
		if !ok || count <= 0 {
			count = 1
		}
		target := day.AddDate(0, 0, -count*days)
		return dayRange(target, target)
	}
	// "in N units" names a window running forward from the reference day.
	if match := futureSpanPattern.FindStringSubmatch(lowered); match != nil {
		if count, ok := countValue(match[1]); ok && count > 0 {
			if days, ok := unitDays[match[2]]; ok {
				return dayRange(day, day.AddDate(0, 0, count*days))
			}
		}
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
	// The day-first form names one exact date, so it is tried before the
	// month-only form that would otherwise collapse it to the whole month.
	if match := dayMonthPattern.FindStringSubmatch(lowered); match != nil {
		if window := dayMonthWindow(match, day); window != nil {
			return window
		}
	}
	if match := ordinalMonthPattern.FindStringSubmatch(lowered); match != nil {
		if window := ordinalMonthDayWindow(match, day); window != nil {
			return window
		}
	}
	// A month named with a year but no day is that whole month.
	if match := monthYearPattern.FindStringSubmatch(lowered); match != nil {
		if window := monthYearWindow(match, day); window != nil {
			return window
		}
	}
	if match := ordinalPattern.FindStringSubmatch(lowered); match != nil {
		if window := ordinalWindow(match[1], day); window != nil {
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
	// A bare month name resolves to the reference day-of-month inside that
	// month, preferring the past. It is tried after the named-day words so
	// "today in march" is today, the word the query actually keys on.
	for _, month := range monthNameOrder {
		if containsWord(lowered, month) {
			return monthDayOfReference(monthNames[month], day)
		}
	}
	// A four-digit year counts as a year only when a word introduces it, and
	// only when it is near the reference date. Unintroduced, an isolated
	// integer is a port or a ticket number, and a window built from one matches
	// nothing while claiming to be a time constraint.
	if window := yearIntroWindow(lowered, day); window != nil {
		return window
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

// dayMonthWindow resolves the day-first form "<day> <month>[ <year>]" to that
// exact date, using the current year unless one was named.
func dayMonthWindow(match []string, day time.Time) *storage.TemporalWindow {
	dayOfMonth, err := strconv.Atoi(match[1])
	if err != nil {
		return nil
	}
	return monthDayWindow([]string{"", match[2], strconv.Itoa(dayOfMonth), match[3]}, day)
}

// ordinalMonthDayWindow resolves "<n>th of <month>" to that day of the named
// month, using the current year.
func ordinalMonthDayWindow(match []string, day time.Time) *storage.TemporalWindow {
	return monthDayWindow([]string{"", match[2], match[1], ""}, day)
}

// monthYearWindow resolves "<month> <year>" to that whole calendar month. A
// year before year one is unrepresentable and yields no window rather than a
// crash.
func monthYearWindow(match []string, _ time.Time) *storage.TemporalWindow {
	month, ok := monthNames[match[1]]
	if !ok {
		return nil
	}
	year, err := strconv.Atoi(match[2])
	if err != nil || year < 1 {
		return nil
	}
	start := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	end := lastDayOfMonth(year, month)
	return dayRange(start, end)
}

// monthDayOfReference resolves a month named without a day, and a bare ordinal
// small enough to read as one, to the reference day-of-month inside that month.
// A month still ahead of the reference date rolls back a year, which is how the
// reference parser prefers a date in the past.
func monthDayOfReference(month time.Month, day time.Time) *storage.TemporalWindow {
	target := time.Date(day.Year(), month, day.Day(), 0, 0, 0, 0, time.UTC)
	if target.After(day) {
		target = time.Date(day.Year()-1, month, day.Day(), 0, 0, 0, 0, time.UTC)
	}
	return dayRange(target, target)
}

// ordinalWindow resolves "the <n>th" the way the reference parser does. Up to
// twelve the number is read as a month with the reference day-of-month; above
// that it names that day of the reference month when the month is long enough.
func ordinalWindow(number string, day time.Time) *storage.TemporalWindow {
	n, err := strconv.Atoi(number)
	if err != nil {
		return nil
	}
	if n >= 1 && n <= 12 {
		return monthDayOfReference(time.Month(n), day)
	}
	if n >= 13 && n <= 31 {
		if n > lastDayOfMonth(day.Year(), day.Month()).Day() {
			return nil
		}
		return dayRange(time.Date(day.Year(), day.Month(), n, 0, 0, 0, 0, time.UTC),
			time.Date(day.Year(), day.Month(), n, 0, 0, 0, 0, time.UTC))
	}
	return nil
}

// yearIntroWindow resolves "<introducer> <year>" to that whole calendar year.
// The year must be plausibly near the reference date: a number a century and a
// half out is not a year someone means, and a window there would silently match
// nothing. An ISO date that merely starts with four digits ("2019-06") is left
// to the caller's own bounds rather than narrowed here.
func yearIntroWindow(query string, day time.Time) *storage.TemporalWindow {
	indexes := yearIntroPattern.FindStringSubmatchIndex(query)
	if indexes == nil {
		return nil
	}
	end := indexes[3]
	if end+1 < len(query) && (query[end] == '-' || query[end] == '/' || query[end] == '.') &&
		query[end+1] >= '0' && query[end+1] <= '9' {
		return nil
	}
	year, err := strconv.Atoi(query[indexes[2]:end])
	if err != nil || year < 1 {
		return nil
	}
	if year < day.Year()-120 || year > day.Year()+20 {
		return nil
	}
	return dayRange(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC))
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

// countValue reads a matched count, which may be empty (meaning one), digits,
// or a spelled-out number word.
func countValue(text string) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 1, true
	}
	if n, err := strconv.Atoi(text); err == nil {
		return n, true
	}
	n, ok := spelledNumbers[text]
	return n, ok
}

// parseChineseNumber reads a count written in Chinese numerals: a plain digit
// (三), a place-value compound (十, 十二, 二十, 三十, 二十四), or one of the
// two-word forms 廿/卅 build. Digits pass through unchanged. An unknown
// character, or a multi-character text with no place unit, yields false so the
// caller declines rather than guesses.
func parseChineseNumber(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	if count, err := strconv.Atoi(text); err == nil {
		return count, true
	}
	runes := []rune(text)
	if runes[0] == '廿' || runes[0] == '卅' {
		base := 20
		if runes[0] == '卅' {
			base = 30
		}
		switch len(runes) {
		case 1:
			return base, true
		case 2:
			if digit, ok := chineseDigitValue(runes[1]); ok {
				return base + digit, true
			}
		}
		return 0, false
	}
	if !hasChinesePlaceUnit(runes) {
		if len(runes) != 1 {
			return 0, false
		}
		return chineseDigitValue(runes[0])
	}
	total, section, number := 0, 0, 0
	for _, r := range runes {
		if digit, ok := chineseDigitValue(r); ok {
			number = digit
			continue
		}
		if r == '万' {
			section = (section + number) * 10000
			total += section
			section, number = 0, 0
			continue
		}
		place, ok := chinesePlaceValues[r]
		if !ok {
			return 0, false
		}
		if number == 0 {
			number = 1
		}
		section += number * place
		number = 0
	}
	return total + section + number, true
}

// chineseDigitValue returns the value of a Chinese numeral character.
func chineseDigitValue(r rune) (int, bool) {
	value, ok := chineseDigitValues[r]
	return value, ok
}

// hasChinesePlaceUnit reports whether a numeral text carries a 十/百/千/万 unit,
// which is what makes it compound rather than a single digit.
func hasChinesePlaceUnit(runes []rune) bool {
	for _, r := range runes {
		if _, ok := chinesePlaceValues[r]; ok || r == '万' {
			return true
		}
	}
	return false
}

// parseChineseMonth reads a month number written in Chinese numerals or digits.
func parseChineseMonth(text string) (int, bool) {
	if month, err := strconv.Atoi(text); err == nil {
		if month >= 1 && month <= 12 {
			return month, true
		}
		return 0, false
	}
	switch text {
	case "十":
		return 10, true
	case "十一":
		return 11, true
	case "十二":
		return 12, true
	}
	runes := []rune(text)
	if len(runes) == 1 {
		if index := strings.IndexRune("一二三四五六七八九", runes[0]); index >= 0 {
			return index + 1, true
		}
	}
	return 0, false
}

// isASCIIDigits reports whether a count is written in digits rather than in
// Chinese numerals. The reference parser distinguishes the two when deciding
// whether a count is large enough to be a rolling window.
func isASCIIDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// singleDay names the whole day containing t.
func singleDay(t time.Time) *storage.TemporalWindow {
	return dayRange(t, t)
}

// lastDayOfMonth returns the last calendar day of a month.
func lastDayOfMonth(year int, month time.Month) time.Time {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
}

// clampDay builds a day inside a month, pulling the day back to the month's last
// day when the month is shorter, so 31 January minus one month is 28 February.
func clampDay(year int, month time.Month, day int) time.Time {
	if last := lastDayOfMonth(year, month).Day(); day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// addMonthsClamped steps a date by whole calendar months, keeping its
// day-of-month where the target month is long enough.
func addMonthsClamped(base time.Time, months int) time.Time {
	first := time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
	return clampDay(first.Year(), first.Month(), base.Day())
}

// addYearsClamped steps a date by whole years, pulling 29 February back in a
// year that has none.
func addYearsClamped(base time.Time, years int) time.Time {
	return clampDay(base.Year()+years, base.Month(), base.Day())
}

// chineseOffset steps base by count of unit in the given direction. Days and
// weeks move by days; months and years move by calendar month or year so a
// count of months lands on the same day-of-month rather than on an estimate.
func chineseOffset(base time.Time, count int, unit string, direction int) (time.Time, bool) {
	step := count * direction
	switch unit {
	case "天", "日":
		return base.AddDate(0, 0, step), true
	case "周", "星期", "礼拜":
		return base.AddDate(0, 0, step*7), true
	case "月":
		return addMonthsClamped(base, step), true
	case "年":
		return addYearsClamped(base, step), true
	}
	return base, false
}

// chineseWindowTo is the window running from count units back to the reference
// day, which is what "3个月内" and "过去三个月" name.
func chineseWindowTo(day time.Time, count int, unit string) *storage.TemporalWindow {
	start, ok := chineseOffset(day, count, unit, -1)
	if !ok {
		return nil
	}
	return dayRange(start, day)
}

// chineseWindowFrom is the window running from the reference day forward by
// count units, which is what "未来三个月" names.
func chineseWindowFrom(day time.Time, count int, unit string) *storage.TemporalWindow {
	end, ok := chineseOffset(day, count, unit, 1)
	if !ok {
		return nil
	}
	return dayRange(day, end)
}

// chineseHalfWindowTo is the window from half a month or year back to the
// reference day.
func chineseHalfWindowTo(day time.Time, unit string) *storage.TemporalWindow {
	if unit == "月" {
		return dayRange(day.AddDate(0, 0, -15), day)
	}
	return dayRange(addMonthsClamped(day, -6), day)
}

// chineseMonthPhase resolves a phase word inside a named month: 上旬/月初 is the
// first ten days, 中旬/月中 the second ten, and 下旬/月末/月底/月尾 the rest.
func chineseMonthPhase(year, month int, phase string) *storage.TemporalWindow {
	m := time.Month(month)
	switch strings.TrimPrefix(phase, "月") {
	case "初", "上旬":
		return dayRange(time.Date(year, m, 1, 0, 0, 0, 0, time.UTC),
			time.Date(year, m, 10, 0, 0, 0, 0, time.UTC))
	case "中", "中旬":
		return dayRange(time.Date(year, m, 11, 0, 0, 0, 0, time.UTC),
			time.Date(year, m, 20, 0, 0, 0, 0, time.UTC))
	default:
		return dayRange(time.Date(year, m, 21, 0, 0, 0, 0, time.UTC), lastDayOfMonth(year, m))
	}
}

// chineseMonthPhaseWindow resolves the early/middle/late-of-month phrases. A
// phase with no month attached is the reference month's, a qualifier shifts the
// month, and a bare month number names that phase in the most recent such
// month.
func chineseMonthPhaseWindow(query string, day time.Time) *storage.TemporalWindow {
	if match := chineseAbsoluteMonthPhase.FindStringSubmatch(query); match != nil {
		year, err := strconv.Atoi(match[1])
		if err != nil {
			return nil
		}
		month, ok := parseChineseMonth(match[2])
		if !ok {
			return nil
		}
		return chineseMonthPhase(year, month, match[3])
	}
	if match := chineseRelativeMonthPhase.FindStringSubmatch(query); match != nil {
		month, ok := parseChineseMonth(match[2])
		if !ok {
			return nil
		}
		return chineseMonthPhase(day.Year()+chineseYearOffsets[match[1]], month, match[3])
	}
	if match := chineseCurrentMonthPhase.FindStringSubmatch(query); match != nil {
		return chineseMonthPhase(day.Year(), int(day.Month()), match[1])
	}
	if match := chineseNextMonthPhase.FindStringSubmatch(query); match != nil {
		offset := 1
		if strings.HasPrefix(match[0], "下下") {
			offset = 2
		}
		start := addMonthsClamped(day, offset)
		return chineseMonthPhase(start.Year(), int(start.Month()), match[1])
	}
	if match := chinesePrevMonthPhase.FindStringSubmatch(query); match != nil {
		offset := -1
		if strings.HasPrefix(match[0], "上上") {
			offset = -2
		}
		start := addMonthsClamped(day, offset)
		return chineseMonthPhase(start.Year(), int(start.Month()), match[1])
	}
	if match := chineseBareMonthPhase.FindStringSubmatch(query); match != nil {
		month, ok := parseChineseMonth(match[1])
		if !ok {
			return nil
		}
		window := chineseMonthPhase(day.Year(), month, match[2])
		if window == nil || window.Start.After(day) {
			window = chineseMonthPhase(day.Year()-1, month, match[2])
		}
		return window
	}
	if match := chineseBarePhase.FindStringSubmatch(query); match != nil {
		return chineseMonthPhase(day.Year(), int(day.Month()), match[1])
	}
	return nil
}

// chinesePointOffsetWindow resolves "<n> <unit> 前/后" to the single day that
// distance from the reference day.
func chinesePointOffsetWindow(query string, day time.Time) *storage.TemporalWindow {
	match := chinesePointOffset.FindStringSubmatch(query)
	if match == nil {
		return nil
	}
	count, ok := parseChineseNumber(match[2])
	if !ok || count <= 0 {
		return nil
	}
	direction := -1
	if match[4] != "前" {
		direction = 1
	}
	target, ok := chineseOffset(day, count, match[3], direction)
	if !ok {
		return nil
	}
	return singleDay(target)
}

// chineseHalfWindow resolves the half-period forms: 半 on its own, 半 after a
// whole count ("一年半前"), and the counted half ("1个半月后").
func chineseHalfWindow(query string, day time.Time) *storage.TemporalWindow {
	if match := chineseYearHalfPoint.FindStringSubmatch(query); match != nil {
		count, ok := parseChineseNumber(match[1])
		if !ok || count <= 0 {
			return nil
		}
		months := count*12 + 6
		if match[2] != "前" {
			return singleDay(addMonthsClamped(day, months))
		}
		return singleDay(addMonthsClamped(day, -months))
	}
	if match := chineseCountHalfPoint.FindStringSubmatch(query); match != nil {
		count, ok := parseChineseNumber(match[1])
		if !ok || count <= 0 {
			return nil
		}
		if match[2] != "前" {
			return singleDay(addMonthsClamped(day, count).AddDate(0, 0, 15))
		}
		return singleDay(addMonthsClamped(day, -count).AddDate(0, 0, -15))
	}
	if match := chineseHalfMonthPoint.FindStringSubmatch(query); match != nil {
		if match[1] != "前" {
			return singleDay(day.AddDate(0, 0, 15))
		}
		return singleDay(day.AddDate(0, 0, -15))
	}
	if match := chineseHalfYearPoint.FindStringSubmatch(query); match != nil {
		if match[1] != "前" {
			return singleDay(addMonthsClamped(day, 6))
		}
		return singleDay(addMonthsClamped(day, -6))
	}
	return nil
}

// chineseFuzzyWindow resolves the colloquial forms that name a range rather
// than a day: a couple of days is one to three back, a few is two to five.
func chineseFuzzyWindow(query string, day time.Time) *storage.TemporalWindow {
	switch {
	case chineseFuzzyCoupleDays.MatchString(query):
		return dayRange(day.AddDate(0, 0, -3), day.AddDate(0, 0, -1))
	case chineseFuzzyFewDays.MatchString(query):
		return dayRange(day.AddDate(0, 0, -5), day.AddDate(0, 0, -2))
	case chineseFuzzyCoupleWeeks.MatchString(query):
		return dayRange(day.AddDate(0, 0, -21), day.AddDate(0, 0, -7))
	case chineseFuzzyFewWeeks.MatchString(query):
		return dayRange(day.AddDate(0, 0, -35), day.AddDate(0, 0, -14))
	case chineseFuzzyCoupleMonths.MatchString(query):
		return dayRange(day.AddDate(0, 0, -90), day.AddDate(0, 0, -30))
	case chineseFuzzyFewMonths.MatchString(query):
		return dayRange(day.AddDate(0, 0, -150), day.AddDate(0, 0, -60))
	case chineseFuzzyYears.MatchString(query):
		return dayRange(addYearsClamped(day, -3), addYearsClamped(day, -1))
	}
	return nil
}

// chineseRollingWindow resolves the counted and bounded windows: "这三天" and
// "前三个月" run back to the reference day, "未来三个月" runs forward from it,
// and the 内 forms are the bounded back-running ones.
func chineseRollingWindow(query string, day time.Time) *storage.TemporalWindow {
	if match := chineseThisFew.FindStringSubmatch(query); match != nil {
		return chineseWindowTo(day, 5, match[1])
	}
	if match := chineseThisCount.FindStringSubmatch(query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok && count > 1 {
			return chineseWindowTo(day, count, match[2])
		}
		return nil
	}
	// A count of one or two written as a word ("前一天") is a point, not a
	// rolling window; a written-out count above two, or any count in digits, is
	// a window. "前两天" never reaches here because the fuzzy rules claim it.
	if match := chineseFrontDays.FindStringSubmatch(query); match != nil {
		count, ok := parseChineseNumber(match[1])
		if !ok || (count <= 2 && !isASCIIDigits(match[1])) {
			return nil
		}
		return chineseWindowTo(day, count, match[2])
	}
	if match := chineseFrontPeriod.FindStringSubmatch(query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok && count > 1 {
			return chineseWindowTo(day, count, match[2])
		}
		return nil
	}
	if match := chinesePastFew.FindStringSubmatch(query); match != nil {
		return chineseWindowTo(day, 5, match[1])
	}
	if match := chinesePastCount.FindStringSubmatch(query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok {
			return chineseWindowTo(day, count, match[2])
		}
		return nil
	}
	if match := chinesePastHalf.FindStringSubmatch(query); match != nil {
		return chineseHalfWindowTo(day, match[1])
	}
	if match := chineseWithinHalf.FindStringSubmatch(query); match != nil {
		return chineseHalfWindowTo(day, match[1])
	}
	if match := chineseWithinCount.FindStringSubmatch(query); match != nil {
		// "3月内" is a month, not a three-month window, so a month count needs
		// the 个 that makes it a measure; a four-digit year count is a year.
		if match[3] == "月" && match[2] != "个" {
			return nil
		}
		if match[3] == "年" && isASCIIDigits(match[1]) && len(match[1]) == 4 {
			return nil
		}
		if count, ok := parseChineseNumber(match[1]); ok {
			return chineseWindowTo(day, count, match[3])
		}
		return nil
	}
	if match := chineseFutureFew.FindStringSubmatch(query); match != nil {
		return chineseWindowFrom(day, 5, match[1])
	}
	if match := chineseFutureCount.FindStringSubmatch(query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok {
			return chineseWindowFrom(day, count, match[2])
		}
		return nil
	}
	if match := chineseFutureHalf.FindStringSubmatch(query); match != nil {
		if match[1] == "月" {
			return dayRange(day, day.AddDate(0, 0, 15))
		}
		return dayRange(day, addMonthsClamped(day, 6))
	}
	return nil
}

// chinesePeriodWindow derives the window a counted, phased, half or rolling
// Chinese expression names, or nil when the query has none. The order follows
// the reference parser's: an open-ended interval is refused before a suffix can
// be read as a point, and the exact counted forms are tried before the fuzzy
// ones so "两天前" is the day two days back while "前两天" is a range.
func chinesePeriodWindow(query string, day time.Time) *storage.TemporalWindow {
	if chineseSinceGuard.MatchString(query) || chineseStartGuard.MatchString(query) {
		return nil
	}
	if window := chineseMonthPhaseWindow(query, day); window != nil {
		return window
	}
	if window := chinesePointOffsetWindow(query, day); window != nil {
		return window
	}
	if window := chineseHalfWindow(query, day); window != nil {
		return window
	}
	if window := chineseFuzzyWindow(query, day); window != nil {
		return window
	}
	return chineseRollingWindow(query, day)
}

// chineseWindowFromQuery derives the window a Chinese temporal expression
// names, or nil when the query has none. Absolute dates are tried before the
// relative words so "2026年3月15日" is not mistaken for the bare year 2026, and
// the fixed day words before the week/month/year words so "大前天" is not read
// as "前天".
func chineseWindowFromQuery(query string, day time.Time) *storage.TemporalWindow {
	// The counted, phased and rolling forms are tried first, in the reference
	// parser's order, so a phrase like "前两天" is read as the range it is
	// rather than as a count of days.
	if window := chinesePeriodWindow(query, day); window != nil {
		return window
	}
	if match := chineseYearMonthDayPattern.FindStringSubmatch(query); match != nil {
		if window := chineseAbsoluteDay(match[1], match[2], match[3]); window != nil {
			return window
		}
	}
	if match := chineseYearMonthPattern.FindStringSubmatch(query); match != nil {
		if window := chineseYearMonth(match[1], match[2]); window != nil {
			return window
		}
	}
	if match := chineseMonthDayPattern.FindStringSubmatch(query); match != nil {
		if window := chineseAbsoluteDay(strconv.Itoa(day.Year()), match[1], match[2]); window != nil {
			return window
		}
	}
	if match := chineseBareMonthPattern.FindStringSubmatchIndex(query); match != nil {
		if window := chineseBareMonthWindow(query, match, day); window != nil {
			return window
		}
	}
	if match := chineseDayWordPattern.FindStringSubmatch(query); match != nil {
		target := day.AddDate(0, 0, chineseDayOffsets[match[1]])
		return dayRange(target, target)
	}
	if match := chineseWeekPattern.FindStringSubmatch(query); match != nil && match[2] == "" {
		return chineseWeekWindow(day, chineseWeekOffsets[match[1]])
	}
	if match := chineseMonthWordPattern.FindStringSubmatch(query); match != nil {
		return chineseMonthWindow(day, chineseMonthOffsets[match[1]])
	}
	if match := chineseYearWordPattern.FindStringSubmatch(query); match != nil {
		year := day.Year() + chineseYearOffsets[match[1]]
		return dayRange(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
			time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC))
	}
	return nil
}

// chineseAbsoluteDay resolves a stated year, month and day to that single date.
func chineseAbsoluteDay(yearText, monthText, dayText string) *storage.TemporalWindow {
	year, err := strconv.Atoi(yearText)
	if err != nil {
		return nil
	}
	month, err := strconv.Atoi(monthText)
	if err != nil || month < 1 || month > 12 {
		return nil
	}
	dayOfMonth, err := strconv.Atoi(dayText)
	if err != nil || dayOfMonth < 1 || dayOfMonth > 31 {
		return nil
	}
	target := time.Date(year, time.Month(month), dayOfMonth, 0, 0, 0, 0, time.UTC)
	return dayRange(target, target)
}

// chineseBareMonthWindow resolves a month number standing on its own ("3月") to
// that whole calendar month, using last year when the month is still ahead of
// the reference date. A month number preceded by a year or another month is
// part of a longer date and is left to the rules that own it.
func chineseBareMonthWindow(query string, match []int, day time.Time) *storage.TemporalWindow {
	if match[2] > 0 {
		if before, _ := utf8.DecodeLastRuneInString(query[:match[2]]); before == '年' || before == '月' {
			return nil
		}
	}
	month, ok := parseChineseMonth(query[match[2]:match[3]])
	if !ok {
		return nil
	}
	year := day.Year()
	if time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC).After(day) {
		year--
	}
	return dayRange(time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), lastDayOfMonth(year, time.Month(month)))
}

// chineseYearMonth resolves a stated year and month to that whole month.
func chineseYearMonth(yearText, monthText string) *storage.TemporalWindow {
	year, err := strconv.Atoi(yearText)
	if err != nil {
		return nil
	}
	month, err := strconv.Atoi(monthText)
	if err != nil || month < 1 || month > 12 {
		return nil
	}
	return monthWindow(time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC))
}

// chineseWeekWindow returns the Monday-to-Sunday calendar week offset weeks
// from the reference day's week, which is what 本周/上周/下周 name.
func chineseWeekWindow(day time.Time, offset int) *storage.TemporalWindow {
	mondayIndex := (int(day.Weekday()) + 6) % 7 // Monday = 0
	monday := day.AddDate(0, 0, -mondayIndex+offset*7)
	return dayRange(monday, monday.AddDate(0, 0, 6))
}

// chineseMonthWindow returns the calendar month offset months from the
// reference month.
func chineseMonthWindow(day time.Time, offset int) *storage.TemporalWindow {
	first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, offset, 0)
	return monthWindow(first)
}

// monthWindow returns the whole calendar month containing start. Stepping to
// the first of the following month and back one day keeps the end correct for
// months of any length.
func monthWindow(start time.Time) *storage.TemporalWindow {
	end := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	return dayRange(start, end)
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

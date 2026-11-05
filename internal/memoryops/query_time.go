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
var chineseDayWordPattern = regexp.MustCompile(`(大大前天|大大后天|大前天|大后天|前天|后天|昨天|昨日|今天|今日|本日|当日|当天|明天|明日)`)

// chineseDayOffsets maps a fixed day word to its offset from the reference day.
var chineseDayOffsets = map[string]int{
	"大大前天": -4, "大前天": -3, "前天": -2, "昨天": -1, "昨日": -1,
	"今天": 0, "今日": 0, "本日": 0, "当日": 0, "当天": 0, "明天": 1, "明日": 1,
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

// chineseYearFragment matches a year written as four digits or as four numeral
// characters, which is how a Chinese date states its year ("二〇二四年").
const chineseYearFragment = `\d{4}|[零〇○一二三四五六七八九]{4}`

// chineseDayFragment matches a day of month written in digits or in Chinese
// numerals, including the compound 廿 and 卅 forms ("廿五", "卅一").
const chineseDayFragment = `3[01]|[0-2]?[0-9]|卅一|卅|廿[一二三四五六七八九]?|三十一|三十|二十[一二三四五六七八九]?|十[一二三四五六七八九]?|[一二两俩三四五六七八九]`

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

// chineseBoundarySuffixFragment matches the open-interval suffixes the
// boundary rules key on. The reference parser tolerates spaces between a
// period and its suffix.
const chineseBoundarySuffixFragment = `\s*(?:以?前|之前|以?后|之后)`

// chineseMonthBoundarySuffixFragment is the month form of the boundary
// suffix: an optional 份 closes the month word before the suffix.
const chineseMonthBoundarySuffixFragment = `份?\s*(?:以?前|之前|以?后|之后)`

// chineseRelativeYearWordFragment is the relative-year alternation the
// reference parser's boundary rules use, longest forms first. It is wider
// than the calendar-year words the windows resolve, because a boundary
// suffix after any of them declines the range all the same.
const chineseRelativeYearWordFragment = `下一个年度|下一年度|下年度|下一年|大后年|明年|后年|今年|本年|上一年度|上一个年度|上年度|上一年|去年|上年|前一年|大前年|前年`

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

// chineseBoundaryGuard matches a period word or a date followed by an
// open-interval boundary suffix ("去年之前", "2024年3月以前"). The reference
// parser answers those with no temporal constraint at all — the API models a
// closed range only, and everything-before or everything-after is not one —
// so a query carrying one names no window and no other phrase in it may
// supply one.
var chineseBoundaryGuard = regexp.MustCompile(`(?:` +
	`(?:` + chineseYearFragment + `)\s*年\s*(?:` + chineseMonthFragment + `)\s*月\s*(?:` + chineseDayFragment + `)(?:日|号)` + chineseBoundarySuffixFragment + `|` +
	`(?:` + chineseRelativeYearWordFragment + `)\s*(?:` + chineseMonthFragment + `)\s*月\s*(?:` + chineseDayFragment + `)(?:日|号)` + chineseBoundarySuffixFragment + `|` +
	`(?:` + chineseYearFragment + `)\s*年\s*(?:` + chineseMonthFragment + `)\s*月` + chineseMonthBoundarySuffixFragment + `|` +
	`(?:` + chineseRelativeYearWordFragment + `)\s*(?:` + chineseMonthFragment + `)\s*月` + chineseMonthBoundarySuffixFragment + `|` +
	`(?:` + chineseYearFragment + `)\s*年` + chineseBoundarySuffixFragment + `|` +
	`(?:` + chineseRelativeYearWordFragment + `)` + chineseBoundarySuffixFragment + `|` +
	`(?:上上|大上|上|这|本|当|下下|大下|下)` + chineseMeasureFragment + `(?:周|星期|礼拜)` + chineseBoundarySuffixFragment + `|` +
	`(?:上上|大上|上|这|本|当|下下|大下|下)` + chineseMeasureFragment + `月` + chineseMonthBoundarySuffixFragment + `|` +
	`(?:` + chineseMonthFragment + `)\s*月\s*(?:` + chineseDayFragment + `)(?:日|号)` + chineseBoundarySuffixFragment +
	`)`)

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
	// The hour units roll only — a bare hour point ("3小时前") is no form the
	// reference parser resolves — so they stay out of the general unit
	// alternation and these two patterns are the whole of their grammar.
	chinesePastHour   = regexp.MustCompile(`(?:过去|近|最近)(` + chineseNumberFragment + `)(小时|钟头)`)
	chineseFutureHour = regexp.MustCompile(`(?:未来|接下来|往后)(` + chineseNumberFragment + `)(小时|钟头)`)
)

// Month phases name a slice of a month: 上旬/月初 is the first ten days, 中旬/
// 月中 the second ten, and 下旬/月末 the rest. A phase with no month attached is
// the reference month's; a qualifier or a bare month number names another.
var (
	chineseAbsoluteMonthPhase = regexp.MustCompile(`(\d{4})\s*年\s*(` + chineseMonthFragment + `)\s*月份?\s*(` + chineseMonthPhaseFragment + `)`)
	chineseRelativeMonthPhase = regexp.MustCompile(`(` + chineseYearWordFragment + `)\s*(` + chineseMonthFragment + `)\s*月份?\s*(` + chineseMonthPhaseFragment + `)`)
	chineseCurrentMonthPhase  = regexp.MustCompile(`(?:这|本|当)` + chineseMeasureFragment + `月份?\s*(` + chineseMonthPhaseFragment + `)`)
	chineseNextMonthPhase     = regexp.MustCompile(`(?:下下|下)` + chineseMeasureFragment + `月份?\s*(` + chineseMonthPhaseFragment + `)`)
	chinesePrevMonthPhase     = regexp.MustCompile(`(?:上上|上)` + chineseMeasureFragment + `月份?\s*(` + chineseMonthPhaseFragment + `)`)
	chineseBareMonthPhase     = regexp.MustCompile(`(` + chineseMonthFragment + `)\s*月份?\s*(` + chineseMonthPhaseFragment + `)`)
	chineseBarePhase          = regexp.MustCompile(`(` + chinesePhaseFragment + `)`)
)

// chineseBareMonthPattern matches a month number standing on its own ("3月"),
// which names that whole calendar month.
var chineseBareMonthPattern = regexp.MustCompile(`(` + chineseMonthFragment + `)\s*月份?`)

// Chinese absolute dates. The longer year-month-day form is matched first so
// "2026年3月15日" is not read as "2026年3月" or as the bare year 2026. A date
// may end in 号 as well as 日.
var (
	chineseYearMonthDayPattern = regexp.MustCompile(`(` + chineseYearFragment + `)\s*年\s*(` + chineseMonthFragment + `)\s*月\s*(` + chineseDayFragment + `)(?:日|号)`)
	chineseYearMonthPattern    = regexp.MustCompile(`(` + chineseYearFragment + `)\s*年\s*(` + chineseMonthFragment + `)\s*月`)
	chineseMonthDayPattern     = regexp.MustCompile(`(` + chineseMonthFragment + `)月(` + chineseDayFragment + `)(?:日|号)?`)
	// A spaced month-and-day keeps the reference parser own shape: there the
	// day must still carry its 日/号 suffix, so a fully spaced date falls back
	// to the whole month (3 月 15 日 is March, not the 15th).
	chineseSpacedMonthDayPattern = regexp.MustCompile(`(` + chineseMonthFragment + `)\s*月\s*(` + chineseDayFragment + `)(?:日|号)`)
)

// phraseBoundaryPattern wraps an expression so it matches only as a whole
// phrase: a letter or digit may not sit immediately on either side. The class
// is Unicode-aware so Cyrillic and accented words resolve too.
func phraseBoundaryPattern(inner string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(?:` + inner + `)(?:$|[^\p{L}\p{N}])`)
}

var (
	yesterdayWordsPattern = phraseBoundaryPattern(`yesterday|ayer|ieri|hier|gestern|вчера`)
	todayWordsPattern     = phraseBoundaryPattern(`today|hoy|oggi|aujourd['’]?hui|heute|сегодня`)
	dayBeforeLastPattern  = phraseBoundaryPattern(`позавчера`)
)

// fuzzyAgoPattern matches the approximate "a couple/few <unit>s ago" forms.
// The captures are the quantity word and the singular unit.
var fuzzyAgoPattern = regexp.MustCompile(
	`(?:^|[^\p{L}\p{N}])(?:a\s+)?(couple|few)\s+(?:of\s+)?(day|week|month)s?\s+ago(?:$|[^\p{L}\p{N}])`)

// russianFuzzyAgoPattern matches the Russian approximate forms. The first
// capture is the quantity word, the second the unit in its genitive form.
var russianFuzzyAgoPattern = regexp.MustCompile(
	`(?:^|[^\p{L}\p{N}])(пар[ыу]?|несколько)\s+(дн(?:ей|я)|недель|месяцев)\s+назад(?:$|[^\p{L}\p{N}])`)

// fuzzyRangeDays maps a quantity/unit pair to the closed range of day offsets
// it names, measured back from the reference day.
var fuzzyRangeDays = map[string][2]int{
	"couple/day": {-3, -1}, "few/day": {-5, -2},
	"couple/week": {-21, -7}, "few/week": {-35, -14},
	"couple/month": {-90, -30}, "few/month": {-150, -60},
}

// multiLangCalendarPeriods maps the look-back period phrases of the supported
// languages to the calendar period they name. English look-back phrases are
// handled by relativeSpanWindow, so only the other languages live here.
var multiLangCalendarPeriods = []struct {
	pattern *regexp.Regexp
	period  string
}{
	{phraseBoundaryPattern(`la\s+semana\s+pasada|la\s+settimana\s+scorsa|la\s+semaine\s+derniere|la\s+semaine\s+dernière|letzte\s+woche|на\s+прошлой\s+неделе|прошлой\s+неделе|прошлая\s+неделя`), "week"},
	{phraseBoundaryPattern(`el\s+mes\s+pasado|il\s+mese\s+scorso|le\s+mois\s+dernier|letzter\s+monat|letzten\s+monat|в\s+прошлом\s+месяце|прошлом\s+месяце|прошлый\s+месяц`), "month"},
	{phraseBoundaryPattern(`el\s+ano\s+pasado|el\s+año\s+pasado|l'anno\s+scorso|l'annee\s+derniere|l'année\s+dernière|letztes\s+jahr|letzte\s+jahr|в\s+прошлом\s+году|прошлом\s+году|прошлый\s+год`), "year"},
	{phraseBoundaryPattern(`el\s+fin\s+de\s+semana\s+pasado|lo\s+scorso\s+fine\s+settimana|le\s+weekend\s+dernier|le\s+week-end\s+dernier|letztes\s+wochenende|letzte\s+wochenende|на\s+прошлых\s+выходных|в\s+прошлых\s+выходных|прошлых\s+выходных|прошлые\s+выходные`), "weekend"},
}

// calendarPeriodWindow resolves a whole calendar period for a look-back
// direction: the previous week (Monday to Sunday), month, year or weekend
// (Saturday to Sunday). A weekend day rolls back a further week so the weekend
// in progress is not named as the last one.
func calendarPeriodWindow(day time.Time, period string) *storage.TemporalWindow {
	mondayIndex := (int(day.Weekday()) + 6) % 7 // Monday = 0
	switch period {
	case "week":
		start := day.AddDate(0, 0, -(mondayIndex + 7))
		return dayRange(start, start.AddDate(0, 0, 6))
	case "month":
		first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := first.AddDate(0, 0, -1)
		return dayRange(time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC), end)
	case "year":
		year := day.Year() - 1
		return dayRange(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
			time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC))
	case "weekend":
		daysSinceSat := (mondayIndex + 2) % 7
		if daysSinceSat <= 1 {
			daysSinceSat += 7
		}
		sat := day.AddDate(0, 0, -daysSinceSat)
		return dayRange(sat, sat.AddDate(0, 0, 1))
	}
	return nil
}

// explicitPhraseWindow resolves the relative day words, approximate "ago"
// spans and calendar periods the quantified English patterns cannot see. It
// runs before those patterns so "a couple of days ago" is never reduced to the
// single day that "days ago" would name.
func explicitPhraseWindow(lowered string, day time.Time) *storage.TemporalWindow {
	if yesterdayWordsPattern.MatchString(lowered) {
		target := day.AddDate(0, 0, -1)
		return dayRange(target, target)
	}
	if dayBeforeLastPattern.MatchString(lowered) {
		target := day.AddDate(0, 0, -2)
		return dayRange(target, target)
	}
	if todayWordsPattern.MatchString(lowered) {
		return dayRange(day, day)
	}
	if match := fuzzyAgoPattern.FindStringSubmatch(lowered); match != nil {
		if span, ok := fuzzyRangeDays[match[1]+"/"+match[2]]; ok {
			return dayRange(day.AddDate(0, 0, span[0]), day.AddDate(0, 0, span[1]))
		}
	}
	if match := russianFuzzyAgoPattern.FindStringSubmatch(lowered); match != nil {
		quantity := "few"
		if strings.HasPrefix(match[1], "пар") {
			quantity = "couple"
		}
		unit := "day"
		switch {
		case strings.HasPrefix(match[2], "недел"):
			unit = "week"
		case strings.HasPrefix(match[2], "месяц"):
			unit = "month"
		}
		if span, ok := fuzzyRangeDays[quantity+"/"+unit]; ok {
			return dayRange(day.AddDate(0, 0, span[0]), day.AddDate(0, 0, span[1]))
		}
	}
	for _, entry := range multiLangCalendarPeriods {
		if entry.pattern.MatchString(lowered) {
			return calendarPeriodWindow(day, entry.period)
		}
	}
	return nil
}

// multiLangMonthAlternations lists, for each calendar month, the month names
// of the supported languages. Each entry becomes one capture group in the
// month-and-year pattern below.
var multiLangMonthAlternations = []string{
	`january|enero|gennaio|janvier|januar|январь|января|январе`,
	`february|febrero|febbraio|février|fevrier|februar|февраль|февраля|феврале`,
	`march|marzo|mars|märz|marz|март|марта|марте`,
	`april|abril|aprile|avril|апрель|апреля|апреле`,
	`mayo|maggio|mai|may|май|мая|мае`,
	`junio|giugno|juni|juin|june|июнь|июня|июне`,
	`juillet|julio|luglio|juli|july|июль|июля|июле`,
	`agosto|august|août|aout|август|августа|августе`,
	`septiembre|settembre|september|septembre|сентябрь|сентября|сентябре`,
	`october|octubre|ottobre|octobre|oktober|октябрь|октября|октябре`,
	`noviembre|november|novembre|ноябрь|ноября|ноябре`,
	`diciembre|dicembre|décembre|decembre|december|dezember|декабрь|декабря|декабре`, //nolint:misspell // 月份名的多语言变体，不是拼写错误
}

func buildMultiLangMonthYearPattern() *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?:^|[^\p{L}\p{N}])(?:`)
	for i, alt := range multiLangMonthAlternations {
		if i > 0 {
			b.WriteString(`|`)
		}
		b.WriteString(`(`)
		b.WriteString(alt)
		b.WriteString(`)`)
	}
	b.WriteString(`)\s+(\d{4})(?:$|[^\p{L}\p{N}])`)
	return regexp.MustCompile(b.String())
}

var multiLangMonthYearPattern = buildMultiLangMonthYearPattern()

// dayBeforeMonthPattern detects a day number immediately before a month name,
// which is one exact date rather than a month-and-year span.
var dayBeforeMonthPattern = regexp.MustCompile(
	`\d{1,2}\s+(?:` + strings.Join(multiLangMonthAlternations, `|`) + `)`)

// multiLangMonthYearWindow resolves "<month name> <year>" in any supported
// language to that whole calendar month. A day number before the month leaves
// it to the day-first rules that own the exact date.
func multiLangMonthYearWindow(query string, _ time.Time) *storage.TemporalWindow {
	if dayBeforeMonthPattern.MatchString(query) {
		return nil
	}
	match := multiLangMonthYearPattern.FindStringSubmatch(query)
	if match == nil {
		return nil
	}
	month := 0
	for i := 1; i <= len(multiLangMonthAlternations); i++ {
		if match[i] != "" {
			month = i
			break
		}
	}
	year, err := strconv.Atoi(match[len(multiLangMonthAlternations)+1])
	if err != nil || year < 1 || month == 0 {
		return nil
	}
	m := time.Month(month)
	return dayRange(time.Date(year, m, 1, 0, 0, 0, 0, time.UTC), lastDayOfMonth(year, m))
}

var (
	scoreMonthWords = map[string]bool{
		"january": true, "february": true, "march": true, "april": true,
		"may": true, "june": true, "july": true, "august": true,
		"september": true, "october": true, "november": true, "december": true,
	}
	scoreRelativeWords = map[string]bool{
		"today": true, "yesterday": true, "tomorrow": true, "tonight": true, "now": true,
	}
	scoreWeekdayWords = map[string]bool{
		"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
		"friday": true, "saturday": true, "sunday": true,
	}
	scorePeriodWords = map[string]bool{
		"last": true, "next": true, "this": true, "past": true, "coming": true,
		"ago": true, "week": true, "weeks": true, "month": true, "months": true,
		"year": true, "years": true, "day": true, "days": true, "hour": true,
		"hours": true, "minute": true, "minutes": true, "quarter": true,
		"decade": true, "century": true, "weekend": true, "morning": true,
		"afternoon": true, "evening": true, "night": true, "noon": true, "midnight": true,
	}
	scoreTokenPattern = regexp.MustCompile(`[a-z0-9]+`)
)

// signalScore ranks how strong an explicit date signal a matched span carries.
// A digit is the strongest; month and relative words next; weekday and period
// words weakest. A lone four-digit integer with no other date word is a port,
// ticket or buffer size rather than a year, so it scores zero.
func signalScore(text string) int {
	tokens := scoreTokenPattern.FindAllString(strings.ToLower(text), -1)
	if len(tokens) == 0 {
		return 0
	}
	numeric, bareYear, hasDigit := 0, "", false
	scoreable, month, relative, weekday, period := false, false, false, false, false
	for _, tok := range tokens {
		allDigits, anyDigit := true, false
		for i := 0; i < len(tok); i++ {
			if tok[i] < '0' || tok[i] > '9' {
				allDigits = false
			} else {
				anyDigit = true
			}
		}
		if allDigits {
			numeric++
			bareYear = tok
		}
		if anyDigit {
			hasDigit = true
		}
		if scoreMonthWords[tok] {
			month, scoreable = true, true
		}
		if scoreRelativeWords[tok] {
			relative, scoreable = true, true
		}
		if scoreWeekdayWords[tok] {
			weekday, scoreable = true, true
		}
		if scorePeriodWords[tok] {
			period, scoreable = true, true
		}
	}
	if numeric == 1 && len(bareYear) == 4 && !scoreable {
		return 0
	}
	score := 0
	if hasDigit {
		score += 100
	}
	if month {
		score += 50
	}
	if relative {
		score += 50
	}
	if weekday {
		score += 30
	}
	if period {
		score += 20
	}
	return score
}

// strongestBareWordWindow collects every bare relative, weekday and month word
// the query names and keeps the window of the strongest signal; ties go to the
// longest matched word. An explicit month therefore outranks a weekday no
// matter which one appears first in the question.
func strongestBareWordWindow(lowered string, day time.Time) *storage.TemporalWindow {
	type candidate struct {
		score  int
		span   int
		window *storage.TemporalWindow
	}
	var best candidate
	add := func(word string, window *storage.TemporalWindow) {
		if window == nil {
			return
		}
		c := candidate{signalScore(word), len(word), window}
		if best.window == nil || c.score > best.score ||
			(c.score == best.score && c.span > best.span) {
			best = c
		}
	}
	for word, offset := range relativeOffsets {
		if containsWord(lowered, word) {
			target := day.AddDate(0, 0, offset)
			add(word, dayRange(target, target))
		}
	}
	for word, weekday := range weekdayNames {
		if containsWord(lowered, word) {
			target := mostRecentWeekday(day, weekday)
			add(word, dayRange(target, target))
		}
	}
	for _, month := range monthNameOrder {
		if containsWord(lowered, month) {
			add(month, monthDayOfReference(monthNames[month], day))
		}
	}
	return best.window
}

// chineseTemporalTranslation folds the traditional and regional forms of the
// characters the Chinese rules use onto the simplified forms those rules
// match, so a query written in a variant reads through the same grammar.
var chineseTemporalTranslation = map[rune]rune{
	'週': '周', '禮': '礼', '個': '个', '兩': '两', '倆': '俩', '幾': '几',
	'後': '后', '這': '这', '過': '过', '來': '来', '現': '现', '內': '内',
	'間': '间', '號': '号', '當': '当', '數': '数', '開': '开', '時': '时',
	'鐘': '钟', '鍾': '钟', '頭': '头', '還': '还', '麼': '么', '發': '发',
	'討': '讨', '論': '论', '會': '会', '議': '议', '費': '费', '錄': '录',
	'訊': '讯', '說': '说', '談': '谈', '寫': '写', '買': '买', '訂': '订',
	'問': '问', '見': '见', '給': '给', '學': '学', '紀': '纪', '總': '总',
	'結': '结', '劃': '划', '經': '经', '報': '报', '資': '资', '記': '记',
	'計': '计', '誌': '志',
}

// normalizeTemporalQuery folds compatibility and regional character forms onto
// the ones the grammars match: full-width ASCII becomes ASCII, the ideographic
// space becomes an ordinary space, and the traditional characters above become
// their simplified equivalents.
func normalizeTemporalQuery(query string) string {
	var b strings.Builder
	b.Grow(len(query))
	for _, r := range query {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
		if folded, ok := chineseTemporalTranslation[r]; ok {
			r = folded
		}
		b.WriteRune(r)
	}
	return b.String()
}

func containsCJK(text string) bool {
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// chineseTemporalFollowerChars are the characters a Chinese temporal phrase
// may be followed by in a sentence. A match followed by anything else is a
// substring of a larger word, not the phrase itself.
const chineseTemporalFollowerChars = " \t\r\n.,!?;:()[]{}<>\"'，。！？；：（）【】《》“”‘’、的得地了过着吗呢吧呀啊嘛么和及与或至到起内里中时后前份有要去做干说聊谈讨论查找看问见给提记想开买吃喝玩用学写发订安排帮测试部署收入申请下雨代码改动工资回总复统整比哪怎什谁几多少会活事项费录信消新天计划我你他她它咱议讯才再能还已又曾"

var chineseTemporalFollowerPrefixes = []string{
	"是否", "是不是", "已经", "曾经", "的时候", "期间", "以内", "以来", "之前",
	"左右", "上午", "下午", "早上", "晚上", "中午", "凌晨", "转账", "付款",
	"经费", "报销", "拜访", "支出", "阅读", "清晨", "傍晚", "黄昏", "夜里",
	"半夜", "午夜", "总结", "报表", "报告", "记录", "资料", "纪录", "日志",
	"日记", "读",
}

// hasChineseTemporalContext reports whether the text immediately after a
// match is a plausible place for a temporal phrase to end.
func hasChineseTemporalContext(query string, end int) bool {
	if end >= len(query) {
		return true
	}
	suffix := query[end:]
	for _, prefix := range chineseTemporalFollowerPrefixes {
		if strings.HasPrefix(suffix, prefix) {
			return true
		}
	}
	r, _ := utf8.DecodeRuneInString(suffix)
	return strings.ContainsRune(chineseTemporalFollowerChars, r)
}

// chineseSearchIndex returns the match index of the first hit of re whose
// trailing context is a plausible end for a temporal phrase, scanning every
// match rather than only the first so a false-positive hit does not mask a
// real one.
func chineseSearchIndex(re *regexp.Regexp, query string) []int {
	for _, idx := range re.FindAllStringSubmatchIndex(query, -1) {
		if idx[1] < 0 || !hasChineseTemporalContext(query, idx[1]) {
			continue
		}
		return idx
	}
	return nil
}

// chineseSearch returns the capture groups of the first match of re whose
// trailing context is a plausible end for a temporal phrase.
func chineseSearch(re *regexp.Regexp, query string) []string {
	idx := chineseSearchIndex(re, query)
	if idx == nil {
		return nil
	}
	out := make([]string, len(idx)/2)
	for i := range out {
		if start := idx[2*i]; start >= 0 {
			out[i] = query[start:idx[2*i+1]]
		}
	}
	return out
}

// chineseMatches reports whether re has a match whose trailing context is a
// plausible end for a temporal phrase.
func chineseMatches(re *regexp.Regexp, query string) bool {
	return chineseSearchIndex(re, query) != nil
}

// chineseBoundaryFollowPattern and chineseMonthBoundaryFollowPattern stand in
// for the reference parser's negative lookaheads on its calendar-word rules: a
// week, month or year word that a boundary suffix leans against does not name
// that calendar period ("去年后半年的事" keeps 去年 from naming last year).
// RE2 has no lookahead, so each candidate match is checked in the loop below.
var (
	chineseBoundaryFollowPattern      = regexp.MustCompile(`^\s*(?:以?前|之前|以?后|之后)`)
	chineseMonthBoundaryFollowPattern = regexp.MustCompile(`^份?\s*(?:以?前|之前|以?后|之后)`)
)

// chineseSearchNotFollowedByIndex returns the match index of the first hit of
// re whose trailing context is a plausible end for a temporal phrase and that
// follow does not sit against.
func chineseSearchNotFollowedByIndex(re *regexp.Regexp, follow *regexp.Regexp, query string) []int {
	for _, idx := range re.FindAllStringSubmatchIndex(query, -1) {
		if idx[1] < 0 || !hasChineseTemporalContext(query, idx[1]) {
			continue
		}
		if follow.MatchString(query[idx[1]:]) {
			continue
		}
		return idx
	}
	return nil
}

// chineseSearchNotFollowedBy returns the capture groups of that first hit.
func chineseSearchNotFollowedBy(re *regexp.Regexp, follow *regexp.Regexp, query string) []string {
	idx := chineseSearchNotFollowedByIndex(re, follow, query)
	if idx == nil {
		return nil
	}
	out := make([]string, len(idx)/2)
	for i := range out {
		if start := idx[2*i]; start >= 0 {
			out[i] = query[start:idx[2*i+1]]
		}
	}
	return out
}

// chineseNoConstraintGuard matches phrases that explicitly decline any closed
// range: an over-long 大-chain before a day word. A query carrying one has no
// temporal window at all, so no other phrase in it may supply one.
var chineseNoConstraintGuard = regexp.MustCompile(`大{3,}(前|后|後)天`)

// temporalWindowFromQuery derives the window a query's text names, or nil when
// it names none. The reference date anchors every relative phrase, so the same
// query resolves differently a year apart, which is what a relative phrase
// means.
func temporalWindowFromQuery(query string, reference time.Time) *storage.TemporalWindow {
	normalized := normalizeTemporalQuery(query)
	lowered := strings.ToLower(normalized)
	ref := reference.UTC()
	day := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)

	// Chinese expressions are explicit and calendar-based, and the English
	// word-boundary patterns cannot see them, so they are tried first. A query
	// whose Chinese phrase declines any range stops here altogether.
	if window, stop := chineseWindowFromQuery(normalized, day, ref); window != nil || stop {
		if stop {
			return nil
		}
		return window
	}
	// Relative day words, approximate "ago" spans and calendar periods resolve
	// before the quantified patterns, so "a couple of days ago" is not reduced
	// to the single day "days ago" would name.
	if window := explicitPhraseWindow(lowered, day); window != nil {
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
	if window := multiLangMonthYearWindow(lowered, day); window != nil {
		return window
	}
	if match := ordinalPattern.FindStringSubmatch(lowered); match != nil {
		if window := ordinalWindow(match[1], day); window != nil {
			return window
		}
	}
	// Bare relative, weekday and month words are the weakest signals; keep the
	// strongest one the query names rather than whichever the loops see first.
	if window := strongestBareWordWindow(lowered, day); window != nil {
		return window
	}
	// A four-digit year counts as a year only when a word introduces it, and
	// only when it is near the reference date.
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
	// A look-back direction names a whole calendar period, not a rolling span
	// of fixed length: last week is Monday to Sunday of the previous week, and
	// last month and last year are their complete calendar periods.
	if direction == "last" || direction == "past" || direction == "previous" {
		switch unit {
		case "week", "weeks":
			return calendarPeriodWindow(day, "week")
		case "month", "months":
			return calendarPeriodWindow(day, "month")
		case "year", "years":
			return calendarPeriodWindow(day, "year")
		case "weekend":
			return calendarPeriodWindow(day, "weekend")
		}
	}
	days, ok := unitDays[unit]
	if !ok {
		return nil
	}
	switch direction {
	case "next", "coming":
		return dayRange(day.AddDate(0, 0, 1), day.AddDate(0, 0, days))
	case "this":
		return dayRange(day.AddDate(0, 0, -(days-1)), day)
	default:
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
		End:      time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 999999000, time.UTC),
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
		numerals := []rune("一二三四五六七八九")
		for index, numeral := range numerals {
			if numeral == runes[0] {
				return index + 1, true
			}
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

// chineseHourWindowTo is the window running from the reference instant back by
// the stated hours, clamped to whole days like every window here: the
// reference parser floors both bounds, so a count long enough to cross
// midnight reaches into the neighbouring day ("过去24小时").
func chineseHourWindowTo(instant time.Time, hours int) *storage.TemporalWindow {
	return dayRange(chineseHourShift(instant, hours, -1), instant)
}

// chineseHourWindowFrom is the window running forward from the reference
// instant by the stated hours.
func chineseHourWindowFrom(instant time.Time, hours int) *storage.TemporalWindow {
	return dayRange(instant, chineseHourShift(instant, hours, 1))
}

// chineseHourShift steps an instant by whole hours without overflowing
// time.Duration: a count like 千万小时 moves by whole days first and the
// remainder by hours.
func chineseHourShift(instant time.Time, hours, direction int) time.Time {
	days := hours / 24
	rest := hours % 24
	return instant.AddDate(0, 0, direction*days).Add(time.Duration(direction*rest) * time.Hour)
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
	if match := chineseSearch(chineseAbsoluteMonthPhase, query); match != nil {
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
	if match := chineseSearch(chineseRelativeMonthPhase, query); match != nil {
		month, ok := parseChineseMonth(match[2])
		if !ok {
			return nil
		}
		return chineseMonthPhase(day.Year()+chineseYearOffsets[match[1]], month, match[3])
	}
	if match := chineseSearch(chineseCurrentMonthPhase, query); match != nil {
		return chineseMonthPhase(day.Year(), int(day.Month()), match[1])
	}
	if match := chineseSearch(chineseNextMonthPhase, query); match != nil {
		offset := 1
		if strings.HasPrefix(match[0], "下下") {
			offset = 2
		}
		start := addMonthsClamped(day, offset)
		return chineseMonthPhase(start.Year(), int(start.Month()), match[1])
	}
	if match := chineseSearch(chinesePrevMonthPhase, query); match != nil {
		offset := -1
		if strings.HasPrefix(match[0], "上上") {
			offset = -2
		}
		start := addMonthsClamped(day, offset)
		return chineseMonthPhase(start.Year(), int(start.Month()), match[1])
	}
	if match := chineseSearch(chineseBareMonthPhase, query); match != nil {
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
	if match := chineseSearch(chineseBarePhase, query); match != nil {
		return chineseMonthPhase(day.Year(), int(day.Month()), match[1])
	}
	return nil
}

// chinesePointOffsetWindow resolves "<n> <unit> 前/后" to the single day that
// distance from the reference day.
func chinesePointOffsetWindow(query string, day time.Time) *storage.TemporalWindow {
	match := chineseSearch(chinesePointOffset, query)
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
	if match := chineseSearch(chineseYearHalfPoint, query); match != nil {
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
	if match := chineseSearch(chineseCountHalfPoint, query); match != nil {
		count, ok := parseChineseNumber(match[1])
		if !ok || count <= 0 {
			return nil
		}
		if match[2] != "前" {
			return singleDay(addMonthsClamped(day, count).AddDate(0, 0, 15))
		}
		return singleDay(addMonthsClamped(day, -count).AddDate(0, 0, -15))
	}
	if match := chineseSearch(chineseHalfMonthPoint, query); match != nil {
		if match[1] != "前" {
			return singleDay(day.AddDate(0, 0, 15))
		}
		return singleDay(day.AddDate(0, 0, -15))
	}
	if match := chineseSearch(chineseHalfYearPoint, query); match != nil {
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
	case chineseMatches(chineseFuzzyCoupleDays, query):
		return dayRange(day.AddDate(0, 0, -3), day.AddDate(0, 0, -1))
	case chineseMatches(chineseFuzzyFewDays, query):
		return dayRange(day.AddDate(0, 0, -5), day.AddDate(0, 0, -2))
	case chineseMatches(chineseFuzzyCoupleWeeks, query):
		return dayRange(day.AddDate(0, 0, -21), day.AddDate(0, 0, -7))
	case chineseMatches(chineseFuzzyFewWeeks, query):
		return dayRange(day.AddDate(0, 0, -35), day.AddDate(0, 0, -14))
	case chineseMatches(chineseFuzzyCoupleMonths, query):
		return dayRange(day.AddDate(0, 0, -90), day.AddDate(0, 0, -30))
	case chineseMatches(chineseFuzzyFewMonths, query):
		return dayRange(day.AddDate(0, 0, -150), day.AddDate(0, 0, -60))
	case chineseMatches(chineseFuzzyYears, query):
		return dayRange(addYearsClamped(day, -3), addYearsClamped(day, -1))
	}
	return nil
}

// chineseRollingWindow resolves the counted and bounded windows: "这三天" and
// "前三个月" run back to the reference day, "未来三个月" runs forward from it,
// the 内 forms are the bounded back-running ones, and the hour units roll
// from the reference instant, whose time of day decides which day a count
// long enough to cross midnight reaches into.
func chineseRollingWindow(query string, day, ref time.Time) *storage.TemporalWindow {
	if match := chineseSearch(chineseThisFew, query); match != nil {
		return chineseWindowTo(day, 5, match[1])
	}
	if match := chineseSearch(chineseThisCount, query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok && count > 1 {
			return chineseWindowTo(day, count, match[2])
		}
		return nil
	}
	// A count of one or two written as a word ("前一天") is a point, not a
	// rolling window; a written-out count above two, or any count in digits, is
	// a window. "前两天" never reaches here because the fuzzy rules claim it.
	if match := chineseSearch(chineseFrontDays, query); match != nil {
		count, ok := parseChineseNumber(match[1])
		if !ok || (count <= 2 && !isASCIIDigits(match[1])) {
			return nil
		}
		return chineseWindowTo(day, count, match[2])
	}
	if match := chineseSearch(chineseFrontPeriod, query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok && count > 1 {
			return chineseWindowTo(day, count, match[2])
		}
		return nil
	}
	if match := chineseSearch(chinesePastFew, query); match != nil {
		return chineseWindowTo(day, 5, match[1])
	}
	if match := chineseSearch(chinesePastCount, query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok {
			return chineseWindowTo(day, count, match[2])
		}
		return nil
	}
	if match := chineseSearch(chinesePastHalf, query); match != nil {
		return chineseHalfWindowTo(day, match[1])
	}
	if match := chineseSearch(chineseWithinHalf, query); match != nil {
		return chineseHalfWindowTo(day, match[1])
	}
	if match := chineseSearch(chineseWithinCount, query); match != nil {
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
	if match := chineseSearch(chinesePastHour, query); match != nil {
		if hours, ok := parseChineseNumber(match[1]); ok {
			return chineseHourWindowTo(ref, hours)
		}
		return nil
	}
	if match := chineseSearch(chineseFutureHour, query); match != nil {
		if hours, ok := parseChineseNumber(match[1]); ok {
			return chineseHourWindowFrom(ref, hours)
		}
		return nil
	}
	if match := chineseSearch(chineseFutureFew, query); match != nil {
		return chineseWindowFrom(day, 5, match[1])
	}
	if match := chineseSearch(chineseFutureCount, query); match != nil {
		if count, ok := parseChineseNumber(match[1]); ok {
			return chineseWindowFrom(day, count, match[2])
		}
		return nil
	}
	if match := chineseSearch(chineseFutureHalf, query); match != nil {
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
func chinesePeriodWindow(query string, day, ref time.Time) (*storage.TemporalWindow, bool) {
	if chineseMatches(chineseNoConstraintGuard, query) {
		return nil, true
	}
	if chineseMatches(chineseSinceGuard, query) || chineseMatches(chineseStartGuard, query) ||
		chineseMatches(chineseBoundaryGuard, query) {
		return nil, true
	}
	if window := chineseMonthPhaseWindow(query, day); window != nil {
		return window, false
	}
	if window := chinesePointOffsetWindow(query, day); window != nil {
		return window, false
	}
	if window := chineseHalfWindow(query, day); window != nil {
		return window, false
	}
	if window := chineseFuzzyWindow(query, day); window != nil {
		return window, false
	}
	if window := chineseRollingWindow(query, day, ref); window != nil {
		return window, false
	}
	return nil, false
}

// chineseWindowFromQuery derives the window a Chinese temporal expression
// names, or nil when the query has none. Absolute dates are tried before the
// relative words so "2026年3月15日" is not mistaken for the bare year 2026, and
// the fixed day words before the week/month/year words so "大前天" is not read
// as "前天". The reference instant ref backs the hour windows, whose bounds
// move with the time of day; every other window is day-granular.
func chineseWindowFromQuery(query string, day, ref time.Time) (*storage.TemporalWindow, bool) {
	if !containsCJK(query) {
		return nil, false
	}
	// The counted, phased and rolling forms are tried first so a phrase like
	// 前两天 is read as the range it is rather than as a count of days.
	if window, stop := chinesePeriodWindow(query, day, ref); window != nil || stop {
		return window, stop
	}
	if match := chineseSearch(chineseYearMonthDayPattern, query); match != nil {
		if window := chineseAbsoluteDay(match[1], match[2], match[3]); window != nil {
			return window, false
		}
	}
	if match := chineseSearch(chineseYearMonthPattern, query); match != nil {
		if window := chineseYearMonth(match[1], match[2]); window != nil {
			return window, false
		}
	}
	if match := chineseMonthDayMatch(query); match != nil {
		if window := chineseMonthDayWindow(query, match, day); window != nil {
			return window, false
		}
	}
	if match := chineseSearchNotFollowedByIndex(chineseBareMonthPattern, chineseMonthBoundaryFollowPattern, query); match != nil {
		if window := chineseBareMonthWindow(query, match, day); window != nil {
			return window, false
		}
	}
	// The fixed day, week, month and year words are matched across the whole
	// query: an early hit embedded in a larger word must not mask a real
	// phrase later in the sentence.
	if match := chineseSearch(chineseDayWordPattern, query); match != nil {
		target := day.AddDate(0, 0, chineseDayOffsets[match[1]])
		return dayRange(target, target), false
	}
	if match := chineseSearchNotFollowedBy(chineseWeekPattern, chineseBoundaryFollowPattern, query); match != nil && match[2] == "" {
		return chineseWeekWindow(day, chineseWeekOffsets[match[1]]), false
	}
	if match := chineseSearchNotFollowedBy(chineseMonthWordPattern, chineseMonthBoundaryFollowPattern, query); match != nil {
		return chineseMonthWindow(day, chineseMonthOffsets[match[1]]), false
	}
	if match := chineseSearchNotFollowedBy(chineseYearWordPattern, chineseBoundaryFollowPattern, query); match != nil {
		year := day.Year() + chineseYearOffsets[match[1]]
		return dayRange(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
			time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC)), false
	}
	return nil, false
}

// chineseAbsoluteDay resolves a stated year, month and day to that single
// date. A day the named month does not have names no date, so an impossible
// day yields no window rather than a day in the month that follows.
func chineseAbsoluteDay(yearText, monthText, dayText string) *storage.TemporalWindow {
	year, ok := chineseYearValue(yearText)
	if !ok {
		return nil
	}
	month, ok := parseChineseMonth(monthText)
	if !ok {
		return nil
	}
	dayOfMonth, ok := parseChineseDay(dayText)
	if !ok || dayOfMonth > lastDayOfMonth(year, time.Month(month)).Day() {
		return nil
	}
	target := time.Date(year, time.Month(month), dayOfMonth, 0, 0, 0, 0, time.UTC)
	return dayRange(target, target)
}

// chineseYearValue reads a year stated in digits or as four numeral
// characters, each numeral contributing one digit ("二〇二四" is 2024).
func chineseYearValue(text string) (int, bool) {
	if year, err := strconv.Atoi(text); err == nil {
		return year, true
	}
	digits := make([]rune, 0, 4)
	for _, r := range text {
		switch r {
		case '零', '〇', '○':
			digits = append(digits, '0')
		default:
			index := -1
			for i, numeral := range []rune("一二三四五六七八九") {
				if numeral == r {
					index = i
					break
				}
			}
			if index < 0 {
				return 0, false
			}
			digits = append(digits, rune('0'+index+1))
		}
	}
	if len(digits) != 4 {
		return 0, false
	}
	year, err := strconv.Atoi(string(digits))
	if err != nil {
		return 0, false
	}
	return year, true
}

// parseChineseDay reads a day of month written in digits or Chinese numerals.
func parseChineseDay(text string) (int, bool) {
	day, ok := parseChineseNumber(text)
	if !ok || day < 1 || day > 31 {
		return 0, false
	}
	return day, true
}

// chineseMonthDayMatch returns the match index of the first month-and-day that
// stands on its own: a date preceded by a year belongs to the year rules, and
// a match whose trailing context is not a plausible end for a phrase is part
// of a larger word. The spaced form is tried first and demands the 日/号 the
// reference parser demands of it; the adjacent form keeps the suffix optional,
// so "三月廿五" still names a day.
func chineseMonthDayMatch(query string) []int {
	for _, re := range []*regexp.Regexp{chineseSpacedMonthDayPattern, chineseMonthDayPattern} {
		for _, idx := range re.FindAllStringSubmatchIndex(query, -1) {
			if idx[2] > 0 {
				if before, _ := utf8.DecodeLastRuneInString(query[:idx[2]]); before == '年' {
					continue
				}
			}
			if !hasChineseTemporalContext(query, idx[1]) {
				continue
			}
			return idx
		}
	}
	return nil
}

// chineseMonthDayWindow resolves a month and day standing together ("3月15日",
// "三月廿五") to that single date. A day the month does not have names no
// date, and a date still ahead of the reference day falls back a year, the
// way a bare month prefers the past.
func chineseMonthDayWindow(query string, idx []int, day time.Time) *storage.TemporalWindow {
	month, ok := parseChineseMonth(query[idx[2]:idx[3]])
	if !ok {
		return nil
	}
	dayOfMonth, ok := parseChineseDay(query[idx[4]:idx[5]])
	if !ok {
		return nil
	}
	if dayOfMonth > lastDayOfMonth(day.Year(), time.Month(month)).Day() {
		return nil
	}
	target := time.Date(day.Year(), time.Month(month), dayOfMonth, 0, 0, 0, 0, time.UTC)
	if target.After(day) {
		year := day.Year() - 1
		if dayOfMonth > lastDayOfMonth(year, time.Month(month)).Day() {
			return nil
		}
		target = time.Date(year, time.Month(month), dayOfMonth, 0, 0, 0, 0, time.UTC)
	}
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
	year, ok := chineseYearValue(yearText)
	if !ok {
		return nil
	}
	month, ok := parseChineseMonth(monthText)
	if !ok {
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

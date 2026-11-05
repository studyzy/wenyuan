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
		// A look-back week is the whole previous calendar week (Mon-Sun), not a
		// rolling seven days, so it matches the Chinese 上周 rule.
		{"last week", "what happened last week", "2026-10-05", "2026-10-11"},
		{"next month", "what is planned next month", "2026-10-15", "2026-11-13"},
		{"last friday", "what did we decide last friday", "2026-10-09", "2026-10-09"},
		{"bare weekday", "what happened on monday", "2026-10-12", "2026-10-12"},
		{"month day", "what happened in march 5", "2026-03-05", "2026-03-05"},
		{"month day year", "what happened on march 5, 2024", "2024-03-05", "2024-03-05"},
		{"bare year", "what happened in 2024", "2024-01-01", "2024-12-31"},
		{"spelled days ago", "what changed three days ago", "2026-10-11", "2026-10-11"},
		{"spelled in the last", "what did we do in the last two weeks", "2026-09-30", "2026-10-14"},
		{"spelled in future", "what happens in two weeks", "2026-10-14", "2026-10-28"},
		{"cjk today", "今天我们讨论什么", "2026-10-14", "2026-10-14"},
		{"cjk yesterday", "昨天发生了什么", "2026-10-13", "2026-10-13"},
		{"cjk tomorrow", "明天有什么安排", "2026-10-15", "2026-10-15"},
		{"cjk day before yesterday", "前天发生了什么", "2026-10-12", "2026-10-12"},
		{"cjk day after tomorrow", "后天有什么安排", "2026-10-16", "2026-10-16"},
		{"cjk this week", "本周做了什么", "2026-10-12", "2026-10-18"},
		{"cjk last week", "上周做了什么", "2026-10-05", "2026-10-11"},
		{"cjk next week", "下周要做什么", "2026-10-19", "2026-10-25"},
		{"cjk this month", "本月发生了什么", "2026-10-01", "2026-10-31"},
		{"cjk last month", "上个月发生了什么", "2026-09-01", "2026-09-30"},
		{"cjk next month", "下个月要做什么", "2026-11-01", "2026-11-30"},
		{"cjk bare last month", "上月发生了什么", "2026-09-01", "2026-09-30"},
		{"cjk bare next month", "下月要做什么", "2026-11-01", "2026-11-30"},
		{"cjk this year", "今年发生了什么", "2026-01-01", "2026-12-31"},
		{"cjk last year", "去年发生了什么", "2025-01-01", "2025-12-31"},
		{"cjk next year", "明年要做什么", "2027-01-01", "2027-12-31"},
		{"cjk days ago", "3天前发生了什么", "2026-10-11", "2026-10-11"},
		{"cjk days later", "3天后要做什么", "2026-10-17", "2026-10-17"},
		{"cjk days later traditional", "3天後要做什么", "2026-10-17", "2026-10-17"},
		{"cjk month day", "3月15日发生了什么", "2026-03-15", "2026-03-15"},
		{"cjk year month", "2026年3月发生了什么", "2026-03-01", "2026-03-31"},
		{"cjk year month day", "2024年3月15日发生了什么", "2024-03-15", "2024-03-15"},
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

// The extended Chinese forms resolve against the reference's own arithmetic:
// an exact count of periods lands on the day that distance back, a rolling
// window runs from that distance back to the reference day, and a colloquial
// phrase covers the range the reference parser gives it.
func TestChineseExtendedPeriodForms(t *testing.T) {
	reference := time.Date(2026, time.October, 14, 0, 0, 0, 0, time.UTC)
	days := func(n int) string { return reference.AddDate(0, 0, n).Format("2006-01-02") }
	months := func(n int) string { return addMonthsClamped(reference, n).Format("2006-01-02") }
	years := func(n int) string { return addYearsClamped(reference, n).Format("2006-01-02") }
	today := days(0)

	cases := []struct {
		name  string
		query string
		start string
		end   string
	}{
		// An exact count names a point: 三天前 is the day three days back, and a
		// month count steps by calendar month, so three months back from the
		// 14th is the 14th.
		{"three days ago", "三天前", days(-3), days(-3)},
		{"twelve days ago", "十二天前", days(-12), days(-12)},
		{"twenty days ago", "二十天前", days(-20), days(-20)},
		{"thirty days ago", "三十天前", days(-30), days(-30)},
		{"one week ago", "一周前", days(-7), days(-7)},
		{"two weeks ago", "两周前", days(-14), days(-14)},
		{"three weeks ago", "3个星期前", days(-21), days(-21)},
		{"three months ago", "三个月前", months(-3), months(-3)},
		{"twelve months ago", "十二个月前", months(-12), months(-12)},
		{"two years ago", "两年前", years(-2), years(-2)},
		{"twenty years ago", "二十年前", years(-20), years(-20)},
		{"five days later", "五天后", days(5), days(5)},
		{"three months later", "三个月后", months(3), months(3)},
		{"three days from now", "三天以后", days(3), days(3)},
		// Half a period, on its own and after a whole count.
		{"half a month ago", "半个月前", days(-15), days(-15)},
		{"half a year ago", "半年前", months(-6), months(-6)},
		{"a year and a half ago", "一年半前", months(-18), months(-18)},
		{"a month and a half later", "1个半月后", addMonthsClamped(reference, 1).AddDate(0, 0, 15).Format("2006-01-02"), addMonthsClamped(reference, 1).AddDate(0, 0, 15).Format("2006-01-02")},
		{"half a year later", "半年后", months(6), months(6)},
		// Windows bounded by the reference day.
		{"within three months", "3个月内", months(-3), today},
		{"within thirty days", "30天内", days(-30), today},
		{"within three days", "三天内", days(-3), today},
		{"within five years", "5年以内", years(-5), today},
		{"within half a month", "半个月内", days(-15), today},
		{"past three months", "过去三个月", months(-3), today},
		{"recent two weeks", "最近两周", days(-14), today},
		{"recent half a year", "最近半年", months(-6), today},
		{"recent half a month", "近半个月", days(-15), today},
		{"future three months", "未来三个月", today, months(3)},
		{"next two weeks", "接下来两周", today, days(14)},
		// Colloquial phrases name a range, and the fuzzy range differs from the
		// exact point: 前几天 is not 三天前.
		{"couple of days", "前两天发生了什么", days(-3), days(-1)},
		{"a few days", "前几天发生了什么", days(-5), days(-2)},
		{"recent few days", "近几天发生了什么", days(-5), today},
		{"recent two days", "近两天", days(-2), today},
		{"these three days", "这三天", days(-3), today},
		{"front three days", "前三天", days(-3), today},
		{"front three months", "前三个月", months(-3), today},
		{"front few months", "前几个月", days(-150), days(-60)},
		{"these few months", "这几个月", months(-5), today},
		{"couple of weeks", "一两周前", days(-21), days(-7)},
		{"couple of days adjacent", "两三天前", days(-3), days(-1)},
		// Month phases. 上旬 is the first ten days, 中旬 the second ten, and
		// 下旬 the rest of the current month.
		{"early in month", "上旬", "2026-10-01", "2026-10-10"},
		{"middle of month", "中旬", "2026-10-11", "2026-10-20"},
		{"late in month", "下旬", "2026-10-21", "2026-10-31"},
		{"start of month", "月初", "2026-10-01", "2026-10-10"},
		{"end of month", "月末", "2026-10-21", "2026-10-31"},
		{"middle of this month", "本月中旬", "2026-10-11", "2026-10-20"},
		{"early in a bare month", "3月上旬", "2026-03-01", "2026-03-10"},
		{"end of a bare month", "3月末", "2026-03-21", "2026-03-31"},
		{"middle of last month", "上个月中旬", "2026-09-11", "2026-09-20"},
		{"early next month", "下个月上旬", "2026-11-01", "2026-11-10"},
		{"late two months ago", "上上月下旬", "2026-08-21", "2026-08-31"},
		{"bare month", "3月内", "2026-03-01", "2026-03-31"},
		{"late in a stated month", "2024年3月下旬", "2024-03-21", "2024-03-31"},
		{"middle of last december", "去年12月中旬", "2025-12-11", "2025-12-20"},
		// A bare-month phase still ahead of the reference date uses last year's,
		// like the reference parser's prefer-past rule.
		{"future month phase rolls back", "12月上旬", "2025-12-01", "2025-12-10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window := temporalWindowFromQuery(tc.query, parseReference)
			if window == nil {
				t.Fatalf("query %q produced no window", tc.query)
			}
			if got := window.Start.UTC().Format("2006-01-02"); got != tc.start {
				t.Errorf("start = %v, want %v", got, tc.start)
			}
			if got := window.End.UTC().Format("2006-01-02"); got != tc.end {
				t.Errorf("end = %v, want %v", got, tc.end)
			}
		})
	}
}

// A count written in Chinese numerals must resolve the same way its digits do,
// which is what lets the phrases above share one parser.
func TestParseChineseNumber(t *testing.T) {
	cases := map[string]int{
		"3": 3, "一": 1, "两": 2, "三": 3, "十": 10, "十二": 12, "二十": 20,
		"三十": 30, "二十四": 24, "廿": 20, "廿五": 25, "卅": 30, "百": 100,
	}
	for text, want := range cases {
		got, ok := parseChineseNumber(text)
		if !ok || got != want {
			t.Errorf("parseChineseNumber(%q) = %d, %v; want %d, true", text, got, ok, want)
		}
	}
	for _, text := range []string{"", "三四", "abc", "半"} {
		if got, ok := parseChineseNumber(text); ok {
			t.Errorf("parseChineseNumber(%q) = %d, true; want false", text, got)
		}
	}
}

// The English forms the reference resolves that a day-and-unit parser misses: a
// month named with a year but no day is that whole month, the day-first form is
// one exact date, and an ordinal names a day.
func TestEnglishExtendedForms(t *testing.T) {
	cases := []struct {
		name  string
		query string
		start string
		end   string
	}{
		{"month and year", "what happened in march 2024", "2024-03-01", "2024-03-31"},
		{"day month year", "meeting on 13 july 2026", "2026-07-13", "2026-07-13"},
		{"the third", "what happened on the 3rd", "2026-03-14", "2026-03-14"},
		{"ordinal day", "what happened on the 13th", "2026-10-13", "2026-10-13"},
		{"ordinal of month", "what happened on the 21st of march", "2026-03-21", "2026-03-21"},
		{"bare month name", "what happened in march", "2026-03-14", "2026-03-14"},
		{"bare month rolls back", "what happened in december", "2025-12-14", "2025-12-14"},
		{"introduced year", "what happened in 2019", "2019-01-01", "2019-12-31"},
		{"year word", "year 2019 report", "2019-01-01", "2019-12-31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window := temporalWindowFromQuery(tc.query, parseReference)
			if window == nil {
				t.Fatalf("query %q produced no window", tc.query)
			}
			if got := window.Start.UTC().Format("2006-01-02"); got != tc.start {
				t.Errorf("start = %v, want %v", got, tc.start)
			}
			if got := window.End.UTC().Format("2006-01-02"); got != tc.end {
				t.Errorf("end = %v, want %v", got, tc.end)
			}
		})
	}
}

// An ordinary number in a sentence must not become a window: a port or a ticket
// number is not a year, a count with no period unit is not a duration, and a
// suffix that opens an interval names no closed range.
func TestTemporalWindowFalsePositiveGuards(t *testing.T) {
	for _, query := range []string{
		"port 2019 is not a date",
		"ticket 3250 was closed",
		"the buffer size is 4096 bytes",
		"we have 3 servers in the fleet",
		"我买了三台服务器",
		"我们有三个人",
		"三天以前发生了什么",
		"三天之前发生了什么",
		"3天后开始",
		"三天后开始",
		"最近改了端口 2019",
		"what happened in 1850",
		"what happened in 2099",
	} {
		if window := temporalWindowFromQuery(query, parseReference); window != nil {
			t.Errorf("query %q produced a window %v..%v, want none", query, window.Start, window.End)
		}
	}
}

// A Chinese relative phrase is anchored to the supplied reference exactly like
// the English one, so 昨天 moves with the caller's stated frame rather than the
// wall clock.
func TestChineseWindowAnchorsToReference(t *testing.T) {
	early := temporalWindowFromQuery("昨天发生了什么", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	late := temporalWindowFromQuery("昨天发生了什么", time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC))
	if early == nil || late == nil {
		t.Fatal("a Chinese relative phrase must resolve")
	}
	if early.Start.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("early start = %v, want 2026-01-01", early.Start)
	}
	if late.Start.Format("2006-01-02") != "2026-01-08" {
		t.Fatalf("late start = %v, want 2026-01-08", late.Start)
	}
}

// The fixed day words must win over their shorter suffixes, so 大前天 is three
// days back rather than two.
func TestChineseLongerDayWordWins(t *testing.T) {
	window := temporalWindowFromQuery("大前天发生了什么", parseReference)
	if window == nil {
		t.Fatal("大前天 must resolve")
	}
	if got := window.Start.Format("2006-01-02"); got != "2026-10-11" {
		t.Fatalf("start = %v, want 2026-10-11", got)
	}
}

// An explicit caller window must survive a temporal phrase in the question, so
// the caller's stated range is authoritative.
func TestExplicitWindowBeatsQueryText(t *testing.T) {
	explicit, err := parseTemporalWindow(windowFromJSON(t, `{"start":"2020-01-01T00:00:00Z","end":"2020-01-31T00:00:00Z"}`))
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

// assertTemporalWindow checks the inclusive day bounds a query resolves to.
func assertTemporalWindow(t *testing.T, query string, ref time.Time, start, end string) {
	t.Helper()
	window := temporalWindowFromQuery(query, ref)
	if window == nil {
		t.Fatalf("query %q produced no window", query)
	}
	if got := window.Start.UTC().Format("2006-01-02"); got != start {
		t.Errorf("query %q start = %v, want %v", query, got, start)
	}
	if got := window.End.UTC().Format("2006-01-02"); got != end {
		t.Errorf("query %q end = %v, want %v", query, got, end)
	}
}

// Look-back periods resolve to whole calendar weeks, months, years and
// weekends instead of fixed-length rolling spans. Forward and current
// directions keep their original rolling spans.
func TestCalendarAlignedLookBack(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	cases := []struct {
		name, query, start, end string
	}{
		{"last week", "what happened last week", "2026-10-05", "2026-10-11"},
		{"past week", "what happened past week", "2026-10-05", "2026-10-11"},
		{"previous week", "what happened previous week", "2026-10-05", "2026-10-11"},
		{"last month", "what happened last month", "2026-09-01", "2026-09-30"},
		{"past month", "what happened past month", "2026-09-01", "2026-09-30"},
		{"previous month", "what happened previous month", "2026-09-01", "2026-09-30"},
		{"last year", "what happened last year", "2025-01-01", "2025-12-31"},
		{"previous year", "what happened previous year", "2025-01-01", "2025-12-31"},
		{"last weekend", "what happened last weekend", "2026-10-10", "2026-10-11"},
		{"past weekend", "what happened past weekend", "2026-10-10", "2026-10-11"},
		{"next week", "what is planned next week", days(1), days(7)},
		{"this week", "what happened this week", days(-6), days(0)},
		{"next month", "what is planned next month", days(1), days(30)},
		{"next weekend", "what is planned next weekend", days(1), days(2)},
		{"this weekend", "what happened this weekend", days(-1), days(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// Asked on a weekend day, "last weekend" names the weekend before the one in
// progress, so both Saturday and Sunday roll back a further week.
func TestLastWeekendOnWeekendDay(t *testing.T) {
	for _, tc := range []struct{ ref, start, end string }{
		{"2026-10-10", "2026-10-03", "2026-10-04"},
		{"2026-10-11", "2026-10-03", "2026-10-04"},
	} {
		ref, _ := time.Parse("2006-01-02", tc.ref)
		assertTemporalWindow(t, "what happened last weekend", ref, tc.start, tc.end)
	}
}

// Approximate "ago" phrases name a range rather than the single day a bare
// "days ago" would, in English and Russian alike.
func TestFuzzyAgoPhrases(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	cases := []struct {
		name, query, start, end string
	}{
		{"a couple of days ago", "what changed a couple of days ago", days(-3), days(-1)},
		{"a few days ago", "what changed a few days ago", days(-5), days(-2)},
		{"couple of days ago", "what changed couple of days ago", days(-3), days(-1)},
		{"a couple of weeks ago", "what changed a couple of weeks ago", days(-21), days(-7)},
		{"a few weeks ago", "what changed a few weeks ago", days(-35), days(-14)},
		{"a couple of months ago", "what changed a couple of months ago", days(-90), days(-30)},
		{"a few months ago", "what changed a few months ago", days(-150), days(-60)},
		{"russian couple days", "что было пару дней назад", days(-3), days(-1)},
		{"russian few days", "что было несколько дней назад", days(-5), days(-2)},
		{"russian couple weeks", "что было пару недель назад", days(-21), days(-7)},
		{"russian few weeks", "что было несколько недель назад", days(-35), days(-14)},
		{"russian couple months", "что было пару месяцев назад", days(-90), days(-30)},
		{"russian few months", "что было несколько месяцев назад", days(-150), days(-60)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// Relative day words resolve in the supported languages too.
func TestMultiLangRelativeDayWords(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	cases := []struct {
		query, start, end string
	}{
		{"ayer", days(-1), days(-1)},
		{"hier", days(-1), days(-1)},
		{"gestern", days(-1), days(-1)},
		{"вчера", days(-1), days(-1)},
		{"hoy", days(0), days(0)},
		{"oggi", days(0), days(0)},
		{"heute", days(0), days(0)},
		{"сегодня", days(0), days(0)},
		{"позавчера", days(-2), days(-2)},
	}
	for _, tc := range cases {
		assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
	}
}

// Calendar period phrases resolve to the same aligned windows in every
// supported language.
func TestMultiLangCalendarPeriods(t *testing.T) {
	cases := []struct {
		query, start, end string
	}{
		{"la semana pasada", "2026-10-05", "2026-10-11"},
		{"la settimana scorsa", "2026-10-05", "2026-10-11"},
		{"letzte woche", "2026-10-05", "2026-10-11"},
		{"прошлой неделе", "2026-10-05", "2026-10-11"},
		{"el mes pasado", "2026-09-01", "2026-09-30"},
		{"le mois dernier", "2026-09-01", "2026-09-30"},
		{"el año pasado", "2025-01-01", "2025-12-31"},
		{"letztes jahr", "2025-01-01", "2025-12-31"},
		{"el fin de semana pasado", "2026-10-10", "2026-10-11"},
	}
	for _, tc := range cases {
		assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
	}
}

// A month name followed by a four-digit year is that whole calendar month in
// any supported language.
func TestMultiLangMonthYear(t *testing.T) {
	cases := []struct {
		query, start, end string
	}{
		{"enero 2024", "2024-01-01", "2024-01-31"},
		{"marzo 2023", "2023-03-01", "2023-03-31"},
		{"января 2025", "2025-01-01", "2025-01-31"},
		{"décembre 2022", "2022-12-01", "2022-12-31"},
		{"märz 2020", "2020-03-01", "2020-03-31"},
	}
	for _, tc := range cases {
		assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
	}
}

// signalScore ranks a matched span by the date signal it carries; a lone
// four-digit integer with no other date word scores zero.
func TestSignalScore(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"2024", 0},
		{"port 2019", 0},
		{"march 2024", 150},
		{"3 days ago", 120},
		{"today", 50},
		{"march", 50},
		{"friday", 30},
		{"last week", 20},
		{"", 0},
	} {
		if got := signalScore(tc.text); got != tc.want {
			t.Errorf("signalScore(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

// When a query names several bare time words, the strongest signal wins: an
// explicit month outranks a weekday even though the weekday appears first.
func TestStrongestBareWordWins(t *testing.T) {
	assertTemporalWindow(t, "what happened on friday in march", parseReference,
		"2026-03-14", "2026-03-14")
}

// Compatibility and regional character forms are folded onto the grammar's
// own forms before matching, so full-width numerals and traditional weekday
// and day words resolve.
func TestChineseQueryNormalization(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	cases := []struct {
		name, query, start, end string
	}{
		{"full-width numerals", "３天前发生了什么", days(-3), days(-3)},
		{"traditional day word", "大後天有什么安排", days(3), days(3)},
		{"traditional week word", "上週做了什么", "2026-10-05", "2026-10-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// chineseSearch scans every hit: an early day word embedded in a larger word
// is skipped and the later, properly-contexted phrase is used instead.
func TestChineseSearchSkipsEmbeddedHit(t *testing.T) {
	assertTemporalWindow(t, "前天战昨天发生了什么", parseReference,
		"2026-10-13", "2026-10-13")
}

// A Chinese phrase that declines any closed range short-circuits the whole
// query, so a temporal phrase in another language cannot supply a window.
func TestChineseNoConstraintShortCircuits(t *testing.T) {
	for _, query := range []string{
		"三天以前 last week",
		"大大大前天发生了什么",
	} {
		if window := temporalWindowFromQuery(query, parseReference); window != nil {
			t.Errorf("query %q produced a window %v..%v, want none",
				query, window.Start, window.End)
		}
	}
}

// Stepping a whole month or year back keeps the day inside the target month,
// clamping the 31st (and 29 February) to that month's last day.
func TestMonthEndClamp(t *testing.T) {
	marchEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	assertTemporalWindow(t, "一个月前发生了什么", marchEnd, "2026-02-28", "2026-02-28")
	leapDay := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	assertTemporalWindow(t, "一年前发生了什么", leapDay, "2023-02-28", "2023-02-28")
}

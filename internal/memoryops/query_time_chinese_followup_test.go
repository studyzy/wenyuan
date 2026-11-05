package memoryops

import (
	"testing"
)

// These tables pin the four Chinese temporal rules the first port left out,
// each read off the reference parser's own rules: an open-interval boundary
// suffix names no window at all, the hour units roll a window across
// midnight, 当日/当天 are fixed day words, and the composite forms tolerate
// spaces between their components where the reference parser does.

// assertNoWindow fails when a query produces a window at all.
func assertNoWindow(t *testing.T, query string) {
	t.Helper()
	if window := temporalWindowFromQuery(query, parseReference); window != nil {
		t.Errorf("query %q produced a window %v..%v, want none", query, window.Start, window.End)
	}
}

// TestChineseBoundarySuffixShortCircuit pins the open-interval rule: a period
// word or a date followed by 之前/以前/之后/以后 is everything before or after
// that point, which no closed range represents, so the whole query is
// untimed — including when the phrase is mixed into a longer sentence or a
// query that also carries an English phrase. A word the suffix only leans
// against (去年前三个月) is not an open interval and keeps its window.
func TestChineseBoundarySuffixShortCircuit(t *testing.T) {
	for _, query := range []string{
		"去年之前", "去年 之前", "今年以后", "明年以后", "上年以前", "前年之前",
		"2024年之前", "2024 年以前", "上一年度之前",
		"2024年3月以前", "2024年3月之前", "去年3月以前",
		"2024年3月15日之前", "去年3月15日之前",
		"3月15日以前", "3月15日 之前",
		"上周之前", "这周以后", "上个星期以前", "上週之前",
		"上个月以前", "上月之前", "本月之后", "当月份以后",
		"２０２４年之前",
		"去年之前 last week",
	} {
		assertNoWindow(t, query)
	}
	assertTemporalWindow(t, "去年前三个月", parseReference, "2026-07-14", "2026-10-14")
	// A bare 前 after a counted month reads as the point offset ("3 months
	// ago"), not as a boundary suffix.
	assertTemporalWindow(t, "3月前", parseReference, "2026-07-14", "2026-07-14")
	// 去年后半年 leans 后 against a follow-on word, which is exactly where the
	// reference parser's own lookahead refuses the year word.
	assertNoWindow(t, "去年后半年的事")
}

// TestChineseHourRollingWindows pins the hour units: 过去/近/最近 N 小时(钟头)
// runs back to the reference instant and 未来/接下来/往后 runs forward from it,
// both clamped to whole days, so a count long enough to cross midnight
// reaches into the neighbouring day. The hour units roll only: no half
// count, no bare point form, and no space before the count.
func TestChineseHourRollingWindows(t *testing.T) {
	cases := []struct {
		name, query, start, end string
	}{
		{"past three hours", "过去3小时", "2026-10-14", "2026-10-14"},
		{"past twenty four hours", "过去24小时", "2026-10-13", "2026-10-14"},
		{"past twenty five hours", "过去25小时", "2026-10-13", "2026-10-14"},
		{"recent two hours", "最近两小时", "2026-10-14", "2026-10-14"},
		{"nearly twelve zhongtou", "近12钟头", "2026-10-14", "2026-10-14"},
		{"future five hours", "未来5小时", "2026-10-14", "2026-10-14"},
		{"next three hours", "接下来三小时", "2026-10-14", "2026-10-14"},
		{"an hour later", "往后一小时", "2026-10-14", "2026-10-14"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
	for _, query := range []string{
		"过去半小时", "未来半小时", "3小时前", "三小时后", "过去 三小时", "最近 两小时",
	} {
		assertNoWindow(t, query)
	}
}

// TestChineseCurrentDayWords pins 当日 and 当天 as fixed day words naming the
// reference day, and the context gate keeping 当日 out of 当日程安排.
func TestChineseCurrentDayWords(t *testing.T) {
	for _, query := range []string{"当日", "当天", "当日发生了什么", "当天的安排"} {
		assertTemporalWindow(t, query, parseReference, "2026-10-14", "2026-10-14")
	}
	assertNoWindow(t, "当日程安排")
}

// TestChineseComponentWhitespace pins the spaces the reference parser
// tolerates between a composite expression's components — around 年 and 月 and
// before a month phase — and the one it does not: the day number and its
// 日/号 suffix stay adjacent, so a fully spaced date collapses to its month.
func TestChineseComponentWhitespace(t *testing.T) {
	cases := []struct {
		name, query, start, end string
	}{
		{"spaced year month day", "2024年 3月15日", "2024-03-15", "2024-03-15"},
		{"spaced month day", "3月 15日", "2026-03-15", "2026-03-15"},
		{"spaced month prefix", "3 月15日", "2026-03-15", "2026-03-15"},
		{"spaced year month", "2024 年 3 月", "2024-03-01", "2024-03-31"},
		{"fully spaced date is the month", "3 月 15 日", "2026-03-01", "2026-03-31"},
		{"spaced year month phase", "2024年3月 上旬", "2024-03-01", "2024-03-10"},
		{"spaced last month phase", "上个月 中旬", "2026-09-11", "2026-09-20"},
		{"spaced current month phase", "这月 初", "2026-10-01", "2026-10-10"},
		{"spaced bare month phase", "12月 上旬", "2025-12-01", "2025-12-10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
	// The counted forms keep their components adjacent, and a space alone
	// stays a valid phrase end: 去年 以内 is still last year.
	assertNoWindow(t, "三天 之前")
	assertTemporalWindow(t, "去年 以内", parseReference, "2025-01-01", "2025-12-31")
}

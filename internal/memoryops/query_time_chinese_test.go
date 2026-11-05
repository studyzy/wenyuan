package memoryops

import "testing"

// assertNoChineseWindow checks that a query names no temporal window at all,
// so an ordinary count or a phrase the grammar refuses is not narrowed to a
// period that would hide the right memories.
func assertNoChineseWindow(t *testing.T, query string) {
	t.Helper()
	if window := temporalWindowFromQuery(query, parseReference); window != nil {
		t.Errorf("query %q produced a window %v..%v, want none",
			query, window.Start.Format("2006-01-02"), window.End.Format("2006-01-02"))
	}
}

// A match that runs into text no temporal phrase can end on is a substring of
// a larger word, not a date: a count before a noun or a verb stays a count.
// Each case below names a window when the trailing-context gate is missing.
func TestChineseFollowerGateRejectsNonTemporal(t *testing.T) {
	for _, query := range []string{
		"三个月前生效",
		"三天内完毕",
		"3月生效",
		"我们三个人",
	} {
		assertNoChineseWindow(t, query)
	}
}

// The gate accepts the matches it should: a phrase at the end of the query, or
// one followed by text a temporal phrase plausibly ends on, still resolves.
func TestChineseFollowerGateAcceptsTemporalContext(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	cases := []struct {
		name, query, start, end string
	}{
		{"point at end", "三天前", days(-3), days(-3)},
		{"point before a verb", "三天前开的会", days(-3), days(-3)},
		{"window before a verb", "三天内回复", days(-3), days(0)},
		{"month with measure word", "上个月份的报表", "2026-09-01", "2026-09-30"},
		{"week before a verb", "上周做了什么", "2026-10-05", "2026-10-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// Regional character forms of the gate's own vocabulary must be accepted: a
// phrase followed by a traditional-form word folds onto the simplified word
// the gate knows, and the gate lists the forms the folding produces.
func TestChineseGateCoversRegionalContextForms(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	cases := []struct {
		name, query, start, end string
	}{
		{"record in traditional form", "上周記錄", "2026-10-05", "2026-10-11"},
		{"ji-lu in traditional form", "上周紀錄", "2026-10-05", "2026-10-11"},
		{"log in traditional form", "上周日誌", "2026-10-05", "2026-10-11"},
		{"diary in traditional form", "上周日記", "2026-10-05", "2026-10-11"},
		{"reading as follower", "上周读的资料", "2026-10-05", "2026-10-11"},
		{"ascii quote as follower", "上周\"总结\"", "2026-10-05", "2026-10-11"},
		{"agenda after window", "这三天議程", days(-3), days(0)},
		{"message after window", "最近三天訊息", days(-3), days(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// Every regional character the grammar folds carries one simplified form the
// rules match, so a query written in either form reads through one grammar.
func TestChineseTranslationFoldPairs(t *testing.T) {
	pairs := []struct{ regional, simplified rune }{
		{'週', '周'}, {'禮', '礼'}, {'個', '个'}, {'兩', '两'}, {'幾', '几'},
		{'後', '后'}, {'這', '这'}, {'過', '过'}, {'來', '来'}, {'現', '现'},
		{'倆', '俩'}, {'內', '内'}, {'間', '间'}, {'號', '号'}, {'當', '当'},
		{'數', '数'}, {'開', '开'}, {'時', '时'}, {'鐘', '钟'}, {'鍾', '钟'},
		{'頭', '头'}, {'還', '还'}, {'麼', '么'}, {'發', '发'}, {'討', '讨'},
		{'論', '论'}, {'會', '会'}, {'議', '议'}, {'費', '费'}, {'錄', '录'},
		{'訊', '讯'}, {'說', '说'}, {'談', '谈'}, {'寫', '写'}, {'買', '买'},
		{'訂', '订'}, {'問', '问'}, {'見', '见'}, {'給', '给'}, {'學', '学'},
		{'紀', '纪'}, {'總', '总'}, {'結', '结'}, {'劃', '划'}, {'經', '经'},
		{'報', '报'}, {'資', '资'}, {'記', '记'}, {'計', '计'}, {'誌', '志'},
	}
	for _, pair := range pairs {
		if got := normalizeTemporalQuery(string(pair.regional)); got != string(pair.simplified) {
			t.Errorf("fold %q = %q, want %q", pair.regional, got, pair.simplified)
		}
	}
}

// A query written in regional character forms resolves to the same window as
// its simplified spelling, so either form of one question behaves alike.
func TestChineseTranslationEquivalence(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	months := func(n int) string { return addMonthsClamped(parseReference, n).Format("2006-01-02") }
	cases := []struct {
		name, regional, simplified, start, end string
	}{
		{"week word", "上週做了什麼", "上周做了什么", "2026-10-05", "2026-10-11"},
		{"count before a noun", "兩週前的記錄", "两周前的记录", days(-14), days(-14)},
		{"day word", "大後天有什麼安排", "大后天有什么安排", days(3), days(3)},
		{"fuzzy months", "幾個月前的資料", "几个月前的资料", days(-150), days(-60)},
		{"bounded window", "三個月內", "三个月内", months(-3), days(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.regional, parseReference, tc.start, tc.end)
			assertTemporalWindow(t, tc.simplified, parseReference, tc.start, tc.end)
		})
	}
}

// A day of month written with the compound numerals 廿 and 卅, or with plain
// Chinese numerals, names that date just as its digits do.
func TestChineseCompoundNumeralDates(t *testing.T) {
	cases := []struct {
		name, query, start, end string
	}{
		{"digit month and 廿 day", "3月廿五日发生了什么", "2026-03-25", "2026-03-25"},
		{"textual month and 廿 day", "三月廿五日", "2026-03-25", "2026-03-25"},
		{"卅 day", "三月卅日", "2026-03-30", "2026-03-30"},
		{"卅一 day", "3月卅一日", "2026-03-31", "2026-03-31"},
		{"三十 one day", "三月三十一日", "2026-03-31", "2026-03-31"},
		{"plain textual day", "三月十五日", "2026-03-15", "2026-03-15"},
		{"day without the 日 suffix", "三月廿五", "2026-03-25", "2026-03-25"},
		{"号 suffix", "3月15号", "2026-03-15", "2026-03-15"},
		{"leap february 廿九", "2024年二月廿九日", "2024-02-29", "2024-02-29"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// A day the named month does not have names no date: an impossible day yields
// no window rather than a neighbouring month's, in digits and in numerals.
func TestChineseAbsoluteDayRefusesImpossibleDates(t *testing.T) {
	for _, query := range []string{
		"2026年2月30日",
		"2026年4月31日",
		"2023年2月29日",
		"2023年二月廿九日",
	} {
		assertNoChineseWindow(t, query)
	}
}

// A year written as four numeral characters is a year: the date or month it
// states resolves to that stated year, not to the reference year.
func TestChineseDigitYearDates(t *testing.T) {
	cases := []struct {
		name, query, start, end string
	}{
		{"digit year date", "二〇二四年三月十五日发生了什么", "2024-03-15", "2024-03-15"},
		{"digit year date bare", "二〇二四年三月十五日", "2024-03-15", "2024-03-15"},
		{"digit year month", "二〇二四年三月", "2024-03-01", "2024-03-31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemporalWindow(t, tc.query, parseReference, tc.start, tc.end)
		})
	}
}

// A suffix that opens an interval, or points one at the present, names no
// closed range: the whole query yields no window even when other text in it
// would resolve to one.
func TestChineseOpenIntervalShortCircuit(t *testing.T) {
	for _, query := range []string{
		"一周以前 last week",
		"五个月后开始",
		"三个月之后开始",
	} {
		assertNoChineseWindow(t, query)
	}
}

// The start-clause guard only covers a phrase nothing follows: a start clause
// with something happening there leaves the point the offset names.
func TestChineseStartClauseWithFollowingEvent(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	assertTemporalWindow(t, "三天后开始执行", parseReference, days(3), days(3))
}

// Every hit of a pattern is checked against its trailing context, so a hit
// that runs into unrelated text is skipped and a properly-contexted phrase
// later in the same query is used instead.
func TestChineseFullIterationSkipsContextlessHit(t *testing.T) {
	days := func(n int) string { return parseReference.AddDate(0, 0, n).Format("2006-01-02") }
	assertTemporalWindow(t, "三天前生效，三天内回复", parseReference, days(-3), days(0))
}

// A month stated with a year names that whole month, including the length the
// leap February has.
func TestChineseStatedMonthWindow(t *testing.T) {
	assertTemporalWindow(t, "2024年2月", parseReference, "2024-02-01", "2024-02-29")
}

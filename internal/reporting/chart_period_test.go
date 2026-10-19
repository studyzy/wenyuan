package reporting

import (
	"net/http/httptest"
	"testing"
)

// The reference declares `period: str = Query("7d", ...)`, so the default binds
// only when the key is missing. `?period=` binds the empty string, and the
// response echoes that back: the window still falls to 7 days (the engine treats
// anything but "1d"/"30d" as 7d), but the echoed value is the caller's own -- an
// echo of "7d" for a request that sent "" is a value the caller never wrote.
func TestChartPeriodTellsAbsentFromEmpty(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{``, "7d"},
		{`?other=1`, "7d"},
		{`?period=`, ""},
		{`?period=&other=1`, ""},
		{`?period=1d`, "1d"},
		{`?period=30d`, "30d"},
		{`?period=bogus`, "bogus"},
		// A repeated scalar binds its last occurrence, as httpx.Query does.
		{`?period=&period=1d`, "1d"},
		{`?period=1d&period=`, ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/v1/default/banks/b/audit-logs/stats"+tc.query, nil)
		if got := chartPeriod(req); got != tc.want {
			t.Errorf("query %q: period = %q, want %q", tc.query, got, tc.want)
		}
	}
}

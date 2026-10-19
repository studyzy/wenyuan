package reporting

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// chartPeriod reads a charting endpoint's `period` parameter, telling an absent
// parameter apart from one sent with an empty value.
//
// The reference declares it as `period: str = Query("7d", ...)`, so the default
// applies only when the key is missing: `?period=` binds the empty string and
// that is what the response echoes back, while the window itself still falls to
// the 7-day default because the engine tests `period == "1d"` / `"30d"` and
// treats everything else as 7d. The two must therefore be decided separately --
// an echoed `"7d"` for a request that sent `""` is a value the caller never
// wrote, and a client comparing the echo against what it sent sees a mismatch
// that is not there.
//
// A repeated scalar binds its last occurrence, as httpx.Query does everywhere
// else, so `?period=&period=1d` reads `1d`.
//
// Presence is read from the URL, but the value still goes through httpx.Query
// so the read is recorded for the route's declaration audit: a parameter this
// handler acts on must be declared, and a silent direct read would hide it.
func chartPeriod(r *http.Request) string {
	if _, present := r.URL.Query()["period"]; !present {
		return "7d"
	}
	return httpx.Query(r, "period")
}

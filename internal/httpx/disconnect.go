package httpx

import "net/http"

// The reference monitors its long-running operations (recall, reflect, the
// synchronous retain and the graph read) with a scope-level cancellation token
// that trips on `http.disconnect` (api/disconnect.py). Work that stops because
// the token fired is answered with the token's own reason through the shared
// error envelope (api/http.py:470-476), so an abandoned request records 499 —
// not the 200/500 the interrupted run would otherwise map to.

// DetailClientDisconnected is the reason the reference's disconnect token
// carries, which is the body its 499 answers with.
const DetailClientDisconnected = "client disconnected"

// WriteClientDisconnected answers 499 with the reference's disconnect reason
// when the request's context is already over — the standard library cancels it
// when the client goes away mid-run — and reports whether it did. The metrics
// and access log record the written status like any other.
func WriteClientDisconnected(w http.ResponseWriter, r *http.Request) bool {
	if r.Context().Err() == nil {
		return false
	}
	WriteError(w, NewHTTPError(KindClientClosed, DetailClientDisconnected))
	return true
}

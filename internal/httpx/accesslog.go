package httpx

import (
	"fmt"
	"io"
	"net/http"
)

// AccessLogMiddleware writes one line per request in the shape the reference's
// server writes: `LEVEL:     <client> - "<request line>" <status>`.
//
// The line is what the reference's HTTP server emits when its access log is
// switched on, so a log pipeline built around the reference parses ours without
// a mapping. It deliberately does not go through the structured handler: the
// reference's access logger bypasses its own JSON formatter, so turning the
// process's log stream into JSON records does not change this line's shape
// there either.
func AccessLogMiddleware(writer io.Writer) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			record := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(record, r)
			if writer == nil {
				return
			}
			// The level is the response's: a served request is information, and a
			// server fault is an error. A rejection the caller caused stays at
			// information level, because it is not a failure of this process.
			level := "INFO"
			if record.status >= http.StatusInternalServerError {
				level = "ERROR"
			}
			// A log sink that fails must not take the request with it: the line is
			// the request's record, not its result, so the error is dropped rather
			// than propagated into a handler that has already answered.
			_, _ = fmt.Fprintf(writer, "%s:     %s - \"%s %s %s\" %d\n",
				level, r.RemoteAddr, r.Method, requestLineTarget(r), r.Proto, record.status)
		})
	}
}

// requestLineTarget renders the target as a request line carries it — path and
// query together — because that is what an access log is read for.
func requestLineTarget(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.Path
	}
	return r.URL.Path + "?" + r.URL.RawQuery
}

// statusRecorder captures the status a handler wrote. A handler that writes a
// body without calling WriteHeader served the request, so the default is 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(body)
}

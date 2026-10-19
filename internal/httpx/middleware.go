package httpx

import (
	"bytes"
	"compress/gzip"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
)

// Middleware is the standard handler decorator shape.
type Middleware func(http.Handler) http.Handler

// MiddlewareConfig declares the client-visible middleware toggles.
type MiddlewareConfig struct {
	// GzipMinSize is the compression size floor in bytes. A negative value
	// disables the compression middleware entirely.
	GzipMinSize int
	// Metrics receives request and compression counters when non-nil.
	Metrics *MetricsRecorder
}

// Chain composes middleware so the first entry is the outermost wrapper.
func Chain(handler http.Handler, middlewares ...Middleware) http.Handler {
	wrapped := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		wrapped = middlewares[i](wrapped)
	}
	return wrapped
}

// IgnoredParamsMiddleware reports unknown query parameters on the response.
// The report is route-specific: the matched route declares the parameters it
// accepts through QueryParams, and the query side is computed from that
// declaration (plus the route's path-parameter names) when the response is
// written. Body fields a handler records are added to the same collector. The
// header is written when the response status is first written, because a header
// set after WriteHeader is dropped.
//
// An already-installed collector is reused rather than replaced, so a caller
// that wraps a request with IgnoredParamTrace observes the same request the
// middleware reports on.
func IgnoredParamsMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The reference intercepts the MCP namespace in a wrapping
			// middleware, so an MCP request never reaches the reporting route
			// class and produces no ignored-parameter header. Passing it
			// through untouched keeps that behaviour.
			if strings.HasPrefix(r.URL.Path, NamespaceMCP) {
				next.ServeHTTP(w, r)
				return
			}
			collector := collectorFrom(r.Context())
			if collector == nil {
				collector = newIgnoredCollector()
				r = withIgnoredCollector(r, collector)
			}
			recorder := &ignoredHeaderWriter{ResponseWriter: w, collector: collector, request: r}
			next.ServeHTTP(recorder, r)
		})
	}
}

// ignoredHeaderWriter injects the ignored-parameters header at the moment the
// response status is committed, so the header survives even though handlers
// call WriteHeader themselves.
type ignoredHeaderWriter struct {
	http.ResponseWriter
	collector *ignoredCollector
	request   *http.Request
	wrote     bool
}

func (w *ignoredHeaderWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	if names := w.collector.ignoredFor(w.request); len(names) > 0 {
		w.Header().Set(HeaderIgnoredParams, formatIgnoredParams(names))
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *ignoredHeaderWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// GzipMiddleware compresses response bodies at or above the size floor when the
// client advertises gzip support. A negative floor disables compression.
func GzipMiddleware(minSize int) Middleware {
	return func(next http.Handler) http.Handler {
		if minSize < 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gz := &gzipResponseWriter{
				ResponseWriter: w,
				minSize:        minSize,
				acceptGzip:     acceptsGzip(r),
			}
			defer gz.Close()
			next.ServeHTTP(gz, r)
		})
	}
}

// acceptsGzip reports whether the client advertised gzip. The reference's gzip
// middleware tests for the substring "gzip" in Accept-Encoding rather than
// parsing the header into tokens, so a value it would compress (for example
// "x-gzip", or "gzip;q=0") is compressed here too.
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// gzipResponseWriter buffers the response so the compression floor, the Vary
// header and the Content-Length can all be decided once the body length is
// known — the reference's ASGI gzip middleware works the same way, holding the
// whole body before it answers. A response that already carries a
// Content-Encoding, or one whose content type is text/event-stream, is passed
// through untouched rather than buffered, which also keeps an event stream
// unbuffered.
type gzipResponseWriter struct {
	http.ResponseWriter
	minSize    int
	acceptGzip bool
	status     int
	buf        []byte
	// passthrough streams every write straight through: the response is
	// excluded from compression and from the Vary header.
	passthrough bool
	settled     bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.settled {
		return
	}
	w.status = status
	if w.excluded() {
		w.passthrough = true
		w.settled = true
		w.ResponseWriter.WriteHeader(status)
	}
}

// excluded reports whether the response must not be compressed or varied: it
// already carries a Content-Encoding, or it is an event stream, which the
// reference's gzip middleware names as its only excluded content type.
func (w *gzipResponseWriter) excluded() bool {
	header := w.Header()
	if header.Get("Content-Encoding") != "" {
		return true
	}
	return strings.HasPrefix(header.Get("Content-Type"), "text/event-stream")
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.passthrough {
		return w.ResponseWriter.Write(b)
	}
	w.buf = append(w.buf, b...)
	// An identity response (the client accepts no gzip) only needs the floor to
	// decide its Vary header, so once the body reaches it the response is
	// committed and the remainder streams through instead of being buffered.
	if !w.acceptGzip && len(w.buf) >= w.minSize {
		w.commitIdentityLarge()
	}
	return len(b), nil
}

// commitIdentityLarge writes a buffered identity body at or above the floor
// with the Vary header, then switches to passthrough so a larger body is not
// held in memory.
func (w *gzipResponseWriter) commitIdentityLarge() {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.settled = true
	w.passthrough = true
	header := w.Header()
	header.Add("Vary", "Accept-Encoding")
	// Content-Length is left as the handler set it (the reference's identity
	// responder keeps the original headers); recomputing it here would be wrong
	// for a body still streaming, whose total is not yet known.
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(w.buf)
	w.buf = nil
}

// Flush forces the buffered response out uncompressed, so a streaming handler
// that flushes is not held back by the buffer.
func (w *gzipResponseWriter) Flush() {
	if w.passthrough {
		if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.passthrough = true
	w.settled = true
	w.ResponseWriter.WriteHeader(status)
	if len(w.buf) > 0 {
		_, _ = w.ResponseWriter.Write(w.buf)
		w.buf = w.buf[:0]
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Close writes the buffered response, compressing it and adding the Vary header
// when the body reaches the floor.
func (w *gzipResponseWriter) Close() {
	if w.passthrough {
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.settled = true
	header := w.Header()
	if len(w.buf) < w.minSize {
		// Below the floor the reference does not vary the response either, so a
		// small body is written exactly as the handler left it.
		w.ResponseWriter.WriteHeader(status)
		if len(w.buf) > 0 {
			_, _ = w.ResponseWriter.Write(w.buf)
		}
		return
	}
	// A body at or above the floor is varied whether or not it is compressed:
	// the reference's identity responder adds Vary for it too, so a cache in
	// front of the service keys on Accept-Encoding either way. Only the gzip
	// branch reaches here once buffered: an identity body this large is
	// committed in Write.
	header.Add("Vary", "Accept-Encoding")
	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	_, _ = compressor.Write(w.buf)
	_ = compressor.Close()
	body := compressed.Bytes()
	header.Set("Content-Encoding", "gzip")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(body)
}

// RecoveryMiddleware turns a panic in the request path into the same 500 a
// genuinely unhandled exception produces in the reference: an ASGI server that
// no route's own `except` clause caught re-raises to Starlette's
// ServerErrorMiddleware, which answers the plain-text body "Internal Server
// Error" (21 bytes, no trailing newline, content-type text/plain; charset=utf-8).
//
// It sits inside the access-log and metrics middlewares, so a panicking request
// is still counted and logged with the 500 the client received, and outside the
// reporting middleware, so the recovery response carries no ignored-parameter
// header — ServerErrorMiddleware sits outside the observability middleware too.
//
// A panic with http.ErrAbortHandler is re-raised: the standard library uses it
// to abort a connection deliberately, and swallowing it would turn a requested
// abort into a response. A panic after the response has started cannot be
// answered, so it is re-raised as well and the connection is closed, which is
// what the ASGI server does for an exception raised mid-response.
func RecoveryMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer := &recoveryWriter{ResponseWriter: w}
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				slog.Error("panic recovered while serving request",
					"method", r.Method, "path", r.URL.Path,
					"panic", recovered, "stack", string(debug.Stack()))
				if writer.wrote {
					// The status line is already on the wire; there is nothing
					// left to answer with.
					panic(recovered)
				}
				writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
				writer.WriteHeader(http.StatusInternalServerError)
				_, _ = writer.Write([]byte("Internal Server Error"))
			}()
			next.ServeHTTP(writer, r)
		})
	}
}

// recoveryWriter notes whether the response has started, so the recovery
// middleware does not try to answer a request whose status line is already
// committed.
type recoveryWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *recoveryWriter) WriteHeader(status int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *recoveryWriter) Write(body []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(body)
}

// DisconnectCancelMiddleware passes the request context through unchanged. The
// standard library already cancels it when the client disconnects, so a handler
// that selects on the context observes the disconnect; the admission lane uses
// it to report 499.
func DisconnectCancelMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}
}

// nameMissingBank replaces the missing-bank marker with the reference's message,
// which names the bank. The mapper that produced the error has no bank id; the
// request does, so the substitution happens here.
func nameMissingBank(r *http.Request, err error) error {
	httpErr, ok := err.(*HTTPError)
	if !ok || httpErr.Status != KindNotFound || httpErr.Detail != DetailMissingBank {
		return err
	}
	bankID := r.PathValue("bank_id")
	if bankID == "" {
		return err
	}
	named := *httpErr
	named.Detail = BankNotFoundDetail(bankID)
	return &named
}

// WriteErrorFor writes a shared error envelope, naming the bank the way the
// reference does when the mapper's error carries no bank id.
func WriteErrorFor(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, nameMissingBank(r, err))
}

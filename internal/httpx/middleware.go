package httpx

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// MetricsRecorder counts requests and compressed bodies. It backs the
// monitoring metrics endpoint without adding a metrics dependency.
type MetricsRecorder struct {
	Requests   atomic.Int64
	Compressed atomic.Int64
	InFlight   atomic.Int64
}

// Middleware is the standard handler decorator shape.
type Middleware func(http.Handler) http.Handler

// MiddlewareConfig declares the client-visible middleware toggles.
type MiddlewareConfig struct {
	// GzipMinSize is the compression size floor in bytes. A negative value
	// disables the compression middleware entirely.
	GzipMinSize int
	// DropNullFields enables null-field dropping for responses produced under
	// this middleware chain.
	DropNullFields bool
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

// IgnoredParamsMiddleware reports unknown query parameter names on the
// response. Handlers record the names; parameters discovered upstream are also
// reported so the header is present even on simple routes.
func IgnoredParamsMiddleware(filter QueryFilter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorded := RecordIgnoredParamsFor(r, filter)
			recorder := &ignoredHeaderWriter{ResponseWriter: w, names: nil}
			next.ServeHTTP(recorder, recorded)
			names := ignoredParamsFrom(recorded.Context())
			if len(names) == 0 {
				names = recorder.names
			}
			if len(names) > 0 {
				recorder.Header().Set(HeaderIgnoredParams, formatIgnoredParams(names))
			}
		})
	}
}

// ignoredHeaderWriter defers header writes until the handler has populated the
// ignored-parameter list.
type ignoredHeaderWriter struct {
	http.ResponseWriter
	names []string
	wrote bool
}

func (w *ignoredHeaderWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *ignoredHeaderWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
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
			if !clientAcceptsGzip(r) {
				next.ServeHTTP(w, r)
				return
			}
			gz := &gzipResponseWriter{
				ResponseWriter: w,
				minSize:        minSize,
			}
			defer gz.Close()
			next.ServeHTTP(gz, r)
			gz.VaryHeader()
		})
	}
}

func clientAcceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		encoding := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if strings.EqualFold(encoding, "gzip") {
			return true
		}
	}
	return false
}

// gzipResponseWriter buffers up to the floor before deciding whether to
// compress. It is not safe for streaming handlers; those are out of scope for
// the foundation unit.
type gzipResponseWriter struct {
	http.ResponseWriter
	minSize    int
	status     int
	buf        []byte
	compressor *gzip.Writer
	compressed bool
	wrote      bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.buf = append(w.buf, b...)
	if w.compressed || len(w.buf) >= w.minSize {
		if err := w.flushCompressed(); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (w *gzipResponseWriter) flushCompressed() error {
	if w.compressed {
		_, err := w.compressor.Write(w.buf)
		w.buf = w.buf[:0]
		return err
	}
	w.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	w.ResponseWriter.Header().Add("Vary", "Accept-Encoding")
	w.ResponseWriter.Header().Del("Content-Length")
	w.compressor = gzip.NewWriter(w.ResponseWriter)
	w.compressed = true
	w.wrote = true
	w.ResponseWriter.WriteHeader(w.status)
	_, err := w.compressor.Write(w.buf)
	w.buf = w.buf[:0]
	return err
}

// Close flushes any buffered body, compressing or passing through as
// determined by the floor.
func (w *gzipResponseWriter) Close() {
	if w.compressed {
		_ = w.compressor.Close()
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
	if len(w.buf) > 0 {
		_, _ = w.ResponseWriter.Write(w.buf)
	}
}

func (w *gzipResponseWriter) VaryHeader() {
	if w.compressed {
		w.ResponseWriter.Header().Add("Vary", "Accept-Encoding")
	}
}

// MetricsMiddleware records request counts and in-flight depth.
func MetricsMiddleware(recorder *MetricsRecorder) Middleware {
	return func(next http.Handler) http.Handler {
		if recorder == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.InFlight.Add(1)
			defer recorder.InFlight.Add(-1)
			recorder.Requests.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// DisconnectCancelMiddleware cancels the request context when the client goes
// away so long-running read work stops early.
func DisconnectCancelMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := contextWithCloseNotify(r)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NullDropMiddleware injects the response shaping option into the request
// context so handlers encode consistently.
type encodeOptionsKey struct{}

// WithEncodeOptions records the response shaping options for this request.
func WithEncodeOptions(r *http.Request, opts EncodeOptions) *http.Request {
	return r.WithContext(contextWithValue(r.Context(), encodeOptionsKey{}, opts))
}

// EncodeOptionsFor returns the shaping options recorded for this request.
func EncodeOptionsFor(r *http.Request) EncodeOptions {
	opts, _ := r.Context().Value(encodeOptionsKey{}).(EncodeOptions)
	return opts
}

// WriteJSONFor writes a JSON response using the request's shaping options.
func WriteJSONFor(w http.ResponseWriter, r *http.Request, status int, payload any) {
	WriteJSON(w, status, payload, EncodeOptionsFor(r))
}

// WriteErrorFor writes a shared error envelope using the request's shaping
// options.
func WriteErrorFor(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, err, EncodeOptionsFor(r))
}

// WriteValidationFor writes a validation failure body using the request's
// shaping options.
func WriteValidationFor(w http.ResponseWriter, r *http.Request, failure *ValidationFailure) {
	WriteValidationFailure(w, failure, EncodeOptionsFor(r))
}

func parseIntHeader(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

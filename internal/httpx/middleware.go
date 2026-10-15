package httpx

import (
	"compress/gzip"
	"net/http"
	"strings"
)

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

// WriteJSONOpenRow writes a response whose object rows must keep explicit null
// fields. The reference keeps nulls for open-row payloads such as graph nodes,
// so this overrides the request's null-dropping option for those responses.
func WriteJSONOpenRow(w http.ResponseWriter, r *http.Request, status int, payload any) {
	WriteJSON(w, status, payload, EncodeOptions{DropNullFields: false})
}

// WriteErrorFor writes a shared error envelope using the request's shaping
// options.
func WriteErrorFor(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, nameMissingBank(r, err), EncodeOptionsFor(r))
}

// WriteValidationFor writes a validation failure body using the request's
// shaping options.
func WriteValidationFor(w http.ResponseWriter, r *http.Request, failure *ValidationFailure) {
	WriteValidationFailure(w, failure, EncodeOptionsFor(r))
}

package httpx

import (
	"bytes"
	"net/http"
	"strings"
)

// DetailNotFound and DetailMethodNotAllowed are the bodies the reference's
// router answers an unmatched path or method with.
const (
	DetailNotFound         = "Not Found"
	DetailMethodNotAllowed = "Method Not Allowed"
)

// fallbackBodyLimit bounds what the fallback middleware buffers. The router's
// own rejections are a single short line; anything larger is a real handler's
// response and is passed through as it is written.
const fallbackBodyLimit = 64

// RouteFallbackMiddleware rewrites the router's own text rejections into the
// reference's JSON envelope.
//
// A router that finds no route answers in plain text, which is a different
// media type and a different body from the reference's `{"detail": "Not
// Found"}`. A client that reads the error body therefore sees two shapes. The
// rewrite only touches the router's own rejections: a handler's response,
// including a handler's own 404 or 405, is untouched.
func RouteFallbackMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer := &fallbackWriter{ResponseWriter: w}
			next.ServeHTTP(writer, r)
			writer.finish()
		})
	}
}

// fallbackWriter buffers a small response until it can tell whether the router
// itself produced it.
type fallbackWriter struct {
	http.ResponseWriter
	status      int
	buffer      bytes.Buffer
	passthrough bool
}

func (w *fallbackWriter) WriteHeader(status int) {
	if w.passthrough {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		w.passthrough = true
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *fallbackWriter) Write(body []byte) (int, error) {
	if w.passthrough {
		return w.ResponseWriter.Write(body)
	}
	if w.status == 0 {
		w.status = http.StatusOK
		w.passthrough = true
		w.ResponseWriter.WriteHeader(w.status)
		return w.ResponseWriter.Write(body)
	}
	if w.buffer.Len()+len(body) > fallbackBodyLimit {
		// Larger than a router rejection: it is a handler's response.
		w.flushThrough()
		return w.ResponseWriter.Write(body)
	}
	return w.buffer.Write(body)
}

func (w *fallbackWriter) flushThrough() {
	w.passthrough = true
	status := w.status
	if status == 0 {
		// A handler that wrote neither a status nor a body served an empty 200.
		status = http.StatusOK
	}
	header := w.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(w.buffer.Bytes())
	w.buffer.Reset()
}

// finish emits the JSON envelope for a buffered router rejection, or releases
// the buffered response unchanged when it was not one.
func (w *fallbackWriter) finish() {
	if w.passthrough {
		return
	}
	contentType := w.Header().Get("Content-Type")
	plain := contentType == "" || strings.HasPrefix(contentType, "text/plain")
	if (w.status == http.StatusNotFound || w.status == http.StatusMethodNotAllowed) && plain {
		header := w.Header()
		// The router's own content-type and sniffing headers describe the text
		// body being replaced.
		header.Del("X-Content-Type-Options")
		header.Set("Content-Type", "application/json")
		detail := DetailNotFound
		if w.status == http.StatusMethodNotAllowed {
			detail = DetailMethodNotAllowed
		}
		body, err := encodeJSON(ErrorEnvelope{Detail: detail}, EncodeOptions{})
		if err == nil {
			w.ResponseWriter.WriteHeader(w.status)
			_, _ = w.ResponseWriter.Write(body)
			return
		}
	}
	w.flushThrough()
}

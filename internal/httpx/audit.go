package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/audit"
)

// AuditOptions configures the HTTP audit middleware. A nil AuditOptions, a nil
// Hook, or a nil Action disables HTTP auditing entirely.
type AuditOptions struct {
	// Action maps a matched route (HTTP method + path template) to its audit
	// action and whether the request body is recorded. ok=false means the route
	// is not audited.
	Action func(method, pattern string) (action string, captureRequest, ok bool)
	// Hook makes the record/no-record decision and writes the entry.
	Hook AuditHook
}

// AuditHook is the audit surface the middleware depends on. It is satisfied by
// *audit.Logger; the interface keeps the router free of the audit package's
// concrete type.
type AuditHook interface {
	// ShouldAudit applies the action allowlist and the bank's resolved switch.
	ShouldAudit(ctx context.Context, action, bankID string) bool
	// Record writes one entry. It must never return an error: recording an
	// operation may not fail the operation it describes.
	Record(ctx context.Context, entry audit.Entry)
}

// auditBodyLimit bounds how much of a request body is captured for the trail.
// A larger body is recorded as absent rather than buffered unbounded.
const auditBodyLimit = 4 << 20

// auditMiddleware records an audited route before and after its handler runs.
// It sits innermost in the chain, so it observes the uncompressed response the
// handler produced.
func auditMiddleware(opts *AuditOptions, resolveEndpoint func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opts == nil || opts.Hook == nil || opts.Action == nil {
				next.ServeHTTP(w, r)
				return
			}
			action, captureRequest, ok := opts.Action(r.Method, resolveEndpoint(r))
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			bankID := bankIDFromPath(r.URL.Path)
			if !opts.Hook.ShouldAudit(r.Context(), action, bankID) {
				next.ServeHTTP(w, r)
				return
			}

			started := time.Now().UTC()
			var request map[string]any
			if captureRequest {
				request = captureRequestBody(r)
			}

			recorder := &auditResponseWriter{ResponseWriter: w}
			next.ServeHTTP(recorder, r)

			entry := audit.Entry{
				Action:    action,
				Transport: audit.TransportHTTP,
				BankID:    bankID,
				StartedAt: started,
				EndedAt:   time.Now().UTC(),
				Request:   request,
				Response:  recorder.responseObject(),
			}
			// The error text is the failure's own body, so an operator reading
			// the trail sees why a call failed, not only that it did.
			if recorder.status >= http.StatusBadRequest {
				entry.Error = recorder.errorText()
			}
			// The write outlives the request: it must complete even as the
			// client disconnects, which is why it drops the request's
			// cancellation.
			opts.Hook.Record(context.WithoutCancel(r.Context()), entry)
		})
	}
}

// captureRequestBody reads the whole request body into a JSON object and
// restores it, so the handler sees the same bytes the auditor recorded. A body
// that is not a JSON object, or is too large, is recorded as absent.
func captureRequestBody(r *http.Request) map[string]any {
	if r.Body == nil {
		return nil
	}
	limited := io.LimitReader(r.Body, auditBodyLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > auditBodyLimit {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil
	}
	return object
}

// bankIDFromPath extracts the bank identifier from a bank-scoped path. Every
// audited route is bank-scoped, and the identifier is the segment after the
// "banks" marker.
func bankIDFromPath(path string) string {
	const marker = "/banks/"
	index := strings.Index(path, marker)
	if index < 0 {
		return ""
	}
	rest := path[index+len(marker):]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	return rest
}

// auditResponseWriter buffers the response body and status so the audit entry
// can record them, forwarding every write to the wrapped writer unchanged.
type auditResponseWriter struct {
	http.ResponseWriter
	buf       bytes.Buffer
	status    int
	wroteHead bool
}

// WriteHeader records the status and forwards it once.
func (w *auditResponseWriter) WriteHeader(status int) {
	if w.wroteHead {
		return
	}
	w.wroteHead = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write records the body and forwards it.
func (w *auditResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHead {
		w.status = http.StatusOK
		w.wroteHead = true
	}
	if w.buf.Len() < auditBodyLimit {
		remaining := auditBodyLimit - w.buf.Len()
		if remaining > len(data) {
			remaining = len(data)
		}
		w.buf.Write(data[:remaining])
	}
	return w.ResponseWriter.Write(data)
}

// responseObject parses the recorded body as a JSON object. A body that is not
// an object (or not valid JSON) is recorded as absent.
func (w *auditResponseWriter) responseObject() map[string]any {
	if w.buf.Len() == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(w.buf.Bytes(), &object); err != nil {
		return nil
	}
	return object
}

// errorText renders the failure body for the trail's metadata. An object body
// reports its detail or error field; anything else reports the raw body.
func (w *auditResponseWriter) errorText() string {
	if w.buf.Len() == 0 {
		return ""
	}
	raw := strings.TrimSpace(w.buf.String())
	var object map[string]any
	if err := json.Unmarshal(w.buf.Bytes(), &object); err == nil {
		for _, key := range []string{"detail", "error", "message"} {
			if text, ok := object[key].(string); ok && text != "" {
				return text
			}
		}
	}
	return raw
}

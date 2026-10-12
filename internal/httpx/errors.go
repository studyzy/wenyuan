package httpx

import "net/http"

// Public error identifiers. The shared envelope always carries the message
// under the canonical "detail" field.
const (
	FieldDetail = "detail"
)

// Public response headers.
const (
	HeaderIgnoredParams = "X-Ignored-Params"
	HeaderRetryAfter    = "Retry-After"
)

// ValidationError describes one rejected request field. It follows the
// framework's standard structured error-item list shape.
type ValidationError struct {
	Loc   []string `json:"loc"`
	Msg   string   `json:"msg"`
	Type  string   `json:"type"`
	Input any      `json:"input,omitempty"`
}

// ValidationFailure is an ordered list of structured error items.
type ValidationFailure struct {
	Errors []ValidationError `json:"errors"`
}

func (v *ValidationFailure) Error() string {
	if len(v.Errors) == 0 {
		return "validation failed"
	}
	return v.Errors[0].Msg
}

// ErrorEnvelope is the shared non-validation error body. Only the detail
// field is confirmed public surface; per-class shapes remain an open
// decision, so no additional fields are emitted here.
type ErrorEnvelope struct {
	Detail string `json:"detail"`
}

// Error kinds routable through the shared envelope writer.
const (
	KindBadRequest    = http.StatusBadRequest
	KindNotFound      = http.StatusNotFound
	KindConflict      = http.StatusConflict
	KindGone          = http.StatusGone
	KindUnprocessable = http.StatusUnprocessableEntity
	KindUnavailable   = http.StatusServiceUnavailable
	KindTimeout       = http.StatusGatewayTimeout
	KindInternal      = http.StatusInternalServerError
)

// HTTPError carries a status code and a client-safe detail message.
type HTTPError struct {
	Status int
	Detail string
	Header http.Header
}

func (e *HTTPError) Error() string { return e.Detail }

// NewHTTPError builds an HTTPError with the shared envelope semantics.
func NewHTTPError(status int, detail string) *HTTPError {
	return &HTTPError{Status: status, Detail: detail}
}

// WithHeader attaches a response header (for example Retry-After) to an error.
func (e *HTTPError) WithHeader(name, value string) *HTTPError {
	if e.Header == nil {
		e.Header = http.Header{}
	}
	e.Header.Set(name, value)
	return e
}

// Error constants reused by every area so message wording stays centralized.
const (
	DetailUnknownBank = "bank not found"
	DetailRetired     = "this endpoint has been retired; read the bank configuration with GET /v1/default/banks/{bank_id}/config, write it with PATCH /v1/default/banks/{bank_id}/config, and read the display name from GET /v1/default/banks"
	DetailWritesDown  = "memory writes are temporarily unavailable"
	DetailSearchTO    = "search timed out"
	DetailInternal    = "internal server error"
)

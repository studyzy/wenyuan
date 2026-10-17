package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"
)

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
// framework's standard structured error-item list shape: the same keys the
// reference emits under "detail" for a request validation failure.
type ValidationError struct {
	Type string   `json:"type"`
	Loc  []string `json:"loc"`
	Msg  string   `json:"msg"`
	// Input is the rejected value. The framework always emits the key (null when
	// there is no value to name), so it is not omitted here.
	Input any `json:"input"`
	// Ctx carries the type-specific context the framework adds (a bound, the
	// expected members of an enum). It is emitted only when present.
	Ctx any `json:"ctx,omitempty"`
}

// MarshalJSON emits the item in the framework's own shape, in which a segment
// of loc is a string for a named field and a number for a position in a list.
// A segment that is entirely digits is therefore emitted as a number: the only
// numeric segments the framework produces are list positions.
func (e ValidationError) MarshalJSON() ([]byte, error) {
	loc := make([]any, 0, len(e.Loc))
	for _, segment := range e.Loc {
		if index, err := strconv.Atoi(segment); err == nil && strconv.Itoa(index) == segment {
			loc = append(loc, index)
			continue
		}
		loc = append(loc, segment)
	}
	return json.Marshal(struct {
		Type  string `json:"type"`
		Loc   []any  `json:"loc"`
		Msg   string `json:"msg"`
		Input any    `json:"input"`
		Ctx   any    `json:"ctx,omitempty"`
	}{Type: e.Type, Loc: loc, Msg: e.Msg, Input: e.Input, Ctx: e.Ctx})
}

// ValidationFailure is an ordered list of structured error items. It marshals
// under the same "detail" envelope as every other error, because the reference
// uses one key for both request validation and non-validation failures.
type ValidationFailure struct {
	Errors []ValidationError
	// Status and Detail carry a failure the request layer raises before
	// validation (a body that is not text). Such a request is refused with a
	// plain detail and its own status instead of the item list, but it travels
	// the same path as a rejected body because every caller already reports one.
	Status int
	Detail string
}

// MarshalJSON emits the shared envelope so a validation failure is wire
// compatible with the reference's {"detail":[...]} shape.
func (v ValidationFailure) MarshalJSON() ([]byte, error) {
	if v.Detail != "" {
		return json.Marshal(ErrorEnvelope{Detail: v.Detail})
	}
	return json.Marshal(struct {
		Detail []ValidationError `json:"detail"`
	}{Detail: v.Errors})
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

// PayloadEnvelope is ErrorEnvelope for a detail that is not a string, which the
// reference's envelope allows.
type PayloadEnvelope struct {
	Detail any `json:"detail"`
}

// Error kinds routable through the shared envelope writer.
const (
	KindBadRequest    = http.StatusBadRequest
	KindForbidden     = http.StatusForbidden
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
	// Payload, when non-nil, is written as the `detail` value instead of Detail's
	// string. The reference's envelope is {"detail": <any>}: most of its errors
	// carry a sentence there, but a few carry an object - the Memory Defense
	// all-blocked 422 reports a `violations` list - so the envelope has to be able
	// to express one. Detail is still set alongside it, because an error's text is
	// what logging and Go callers read.
	Payload any
	Header  http.Header
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
	// DetailMissingBank marks a not-found response about a bank. The writer names
	// the bank from the request path, so an error mapper does not need the id.
	DetailMissingBank = "Bank not found"
	// DetailRetiredProfile is the message the reference answers the removed bank
	// profile routes with.
	DetailRetiredProfile = "The bank profile endpoints have been removed. Disposition traits and the reflect mission are bank " +
		"configuration: read them from GET /v1/default/banks/{bank_id}/config as `disposition_skepticism`, " +
		"`disposition_literalism`, `disposition_empathy` and `reflect_mission`, and write them with " +
		"PATCH /v1/default/banks/{bank_id}/config. The `name` field this endpoint also returned was a " +
		"display-only label; read it from GET /v1/default/banks."
	// DetailRetiredBackground is the message the reference answers the removed
	// background route with.
	DetailRetiredBackground = "The bank background endpoint has been removed. The background was folded into the reflect " +
		"mission: write it with PATCH /v1/default/banks/{bank_id}/config as `reflect_mission`. That " +
		"call replaces the value rather than merging into it, so read the current mission from " +
		"GET /v1/default/banks/{bank_id}/config first if you relied on this endpoint's append behaviour."
	DetailWritesDown = "memory writes are temporarily unavailable"
	DetailSearchTO   = "search timed out"
	DetailInternal   = "internal server error"
)

// BankNotFoundDetail is the body of a missing-bank response, naming the bank the
// way the reference names it.
func BankNotFoundDetail(bankID string) string {
	return "Bank '" + bankID + "' not found"
}

// OperationValidationError is a validator's refusal — a missing bank, a tag
// scope violation, a rejected operation — carrying the reason and the status
// the route is expected to answer with.
//
// The reference decides the response by whether the route CATCHES the error:
// a route with an `except OperationValidationError` clause answers the reason
// as `detail` under the carried status, while a route that swallows it into the
// generic `except Exception` handler answers 500 with `str(exc)`. The two are
// therefore different responses for the same failure, and both are contract.
type OperationValidationError struct {
	// Reason is the client-facing detail a catching route returns.
	Reason string
	// Status is the status a catching route returns alongside the reason.
	Status int
}

// Error is the wrapped text an UNCAUGHT validator failure surfaces with through
// the generic handler: the reference's `str(OperationValidationError)`.
func (e *OperationValidationError) Error() string {
	return "Operation validation failed: " + e.Reason
}

// Caught maps the failure onto what a route that catches it returns: the reason
// as detail, the validator's status alongside it.
func (e *OperationValidationError) Caught() *HTTPError {
	return NewHTTPError(e.Status, e.Reason)
}

// Uncaught maps the failure onto the generic 500 an uncaught validator failure
// produces, carrying the wrapped text as detail.
func (e *OperationValidationError) Uncaught() *HTTPError {
	return NewHTTPError(http.StatusInternalServerError, e.Error())
}

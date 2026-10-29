package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// errNegativeDuration reports a header value that cannot be a delay.
var errNegativeDuration = errors.New("negative duration")

// apiErrorEnvelope is the error body an OpenAI-compatible endpoint returns.
type apiErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// resetHeaderNames are the headers an endpoint may use to report when a rate
// limit clears.
var resetHeaderNames = []string{
	"X-RateLimit-Reset-Requests",
	"X-RateLimit-Reset",
	"Retry-After",
}

// contentPolicyMarkers are the phrases that mark a permanent content refusal.
var contentPolicyMarkers = []string{
	"content policy",
	"content_policy",
	"content filter",
	"safety",
	"flagged",
}

// classifyResponse turns a non-success status plus body into the boundary's
// typed error. It is the single seam where vendor status and body become a
// failure class, so a caller never inspects a vendor value directly.
func classifyResponse(status int, header http.Header, body []byte, now time.Time) error {
	detail := decodeErrorMessage(body)
	switch {
	case status == http.StatusTooManyRequests:
		return &provider.RateLimitResetError{RetryAt: resetTime(header, now), WithinBackoff: true}
	case isContentPolicy(status, detail):
		return &provider.ContentPolicyError{Reason: detail}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &provider.MissingCredentialError{Provider: NameOpenAI, ConfigKey: provider.ConfigKeyAPIKey}
	case status >= 500 || status == http.StatusRequestEntityTooLarge:
		return &provider.TransientError{Cause: malformedResponse(status, detail)}
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity || status == http.StatusNotFound:
		// A client error that is not a content refusal is permanent: the same
		// request earns the same rejection, so retrying wastes the budget.
		return &permanentRequestError{status: status, detail: describe(status, detail)}
	default:
		return malformedResponse(status, detail)
	}
}

// permanentRequestError marks a client-error rejection that must not be
// retried. It is not transient, so the retry loop stops after one attempt.
type permanentRequestError struct {
	status int
	detail string
}

func (e *permanentRequestError) Error() string {
	if e.detail == "" {
		return http.StatusText(e.status)
	}
	return e.detail
}

// forcedToolChoice reports whether a request pins the model to a tool: a named
// choice or required. Only those can be answered with a tool_choice rejection;
// "none" and omitted are not worth a fallback.
func forcedToolChoice(policy *provider.ToolSelectionPolicy) bool {
	return policy != nil && (policy.Name != "" || policy.Mode == "required")
}

// rejectsToolChoice reports whether an endpoint refused the request over its
// tool_choice. The 400s read "The tool_choice parameter does not support being
// set to required...", "only \"auto\" is supported for tool_choice", and alike,
// from endpoints that serve the same weights as ones accepting every choice —
// so the error is matched, not a model list, and matched narrowly: the status
// plus the field named in the message, so no other client error falls into the
// fallback.
func rejectsToolChoice(err error) bool {
	var permanent *permanentRequestError
	if !errors.As(err, &permanent) || permanent.status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(permanent.Error(), "tool_choice")
}

// malformedResponse builds the typed malformed-response error for a status
// and detail.
func malformedResponse(status int, detail string) error {
	return &provider.MalformedResponseError{Detail: describe(status, detail)}
}

func describe(status int, detail string) string {
	if detail == "" {
		return http.StatusText(status)
	}
	return detail
}

// isContentPolicy reports whether a status and message describe a permanent
// content refusal rather than an authentication or transient fault.
func isContentPolicy(status int, detail string) bool {
	if status != http.StatusBadRequest && status != http.StatusForbidden &&
		status != http.StatusUnprocessableEntity {
		return false
	}
	lowered := strings.ToLower(detail)
	for _, marker := range contentPolicyMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// maxErrorDetailChars bounds the upstream message retained on an error. The
// body reader already caps how many bytes are read, but an unbounded fallback
// would still let a large non-envelope body ride into an error message and from
// there into a caller-visible diagnostic, so the retained text is clipped too.
const maxErrorDetailChars = 1024

// decodeErrorMessage extracts the human-readable message from an error body,
// falling back to a clipped form of the raw body when it is not the expected
// envelope.
func decodeErrorMessage(body []byte) string {
	var env apiErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Message != "" {
		return clipDetail(env.Error.Message)
	}
	return clipDetail(strings.TrimSpace(string(body)))
}

// clipDetail truncates a detail string to the retained maximum.
func clipDetail(detail string) string {
	if len(detail) <= maxErrorDetailChars {
		return detail
	}
	return detail[:maxErrorDetailChars]
}

// resetTime reads the moment a rate limit clears from the response headers,
// falling back to one minute from now when none is usable.
func resetTime(header http.Header, now time.Time) time.Time {
	for _, name := range resetHeaderNames {
		raw := strings.TrimSpace(header.Get(name))
		if raw == "" {
			continue
		}
		if seconds, err := parseSeconds(raw); err == nil {
			return now.Add(seconds)
		}
		if at, err := time.Parse(time.RFC3339, raw); err == nil {
			return at
		}
	}
	return now.Add(time.Minute)
}

// parseSeconds reads a decimal seconds value, tolerating a fractional part.
func parseSeconds(raw string) (time.Duration, error) {
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, errNegativeDuration
	}
	return time.Duration(f * float64(time.Second)), nil
}

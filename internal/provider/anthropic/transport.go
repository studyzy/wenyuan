package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// maxResponseBytes caps how much of a response body is read, so a hostile or
// broken endpoint cannot exhaust memory.
const maxResponseBytes = 8 << 20 // 8 MiB

// attempt issues one request and returns its body, mapping a non-success
// status onto the boundary's failure class.
func (c *Client) attempt(ctx context.Context, body []byte) ([]byte, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	url := c.baseURL() + "/messages"
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot build request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", AnthropicVersion)
	if key := c.Settings.APIKey; key != "" {
		// This API authenticates with its own header rather than Bearer.
		req.Header.Set("X-Api-Key", key)
	}
	for name, value := range c.Settings.Headers {
		req.Header.Set(name, value)
	}
	resp, err := c.doer().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &provider.TransientError{Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if readErr != nil {
			return nil, &provider.MalformedResponseError{Detail: "cannot read response body", Cause: readErr}
		}
		return raw, nil
	}
	return nil, classifyResponse(resp.StatusCode, raw)
}

// classifyResponse turns a non-success status plus body into the boundary's
// typed error.
func classifyResponse(status int, body []byte) error {
	detail := errorDetail(body)
	switch {
	case status == http.StatusTooManyRequests:
		return &provider.RateLimitResetError{WithinBackoff: true}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &provider.MissingCredentialError{Provider: NameAnthropic, ConfigKey: provider.ConfigKeyAPIKey}
	case isContentRefusal(detail):
		return &provider.ContentPolicyError{Reason: detail}
	case status >= 500:
		return &provider.TransientError{Cause: &provider.MalformedResponseError{Detail: describe(status, detail)}}
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity || status == http.StatusNotFound:
		// A client error that is not a refusal is permanent: the same request
		// earns the same rejection.
		return &permanentError{status: status, detail: describe(status, detail)}
	default:
		return &provider.MalformedResponseError{Detail: describe(status, detail)}
	}
}

// permanentError marks a failure the retry budget must not be spent on.
type permanentError struct {
	status int
	detail string
}

func (e *permanentError) Error() string { return e.detail }

// errorDetail reads the message from the API's error envelope, falling back to
// the raw body when it is not that shape.
func errorDetail(body []byte) string {
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 400 {
		return trimmed[:400]
	}
	return trimmed
}

// describe renders a status and detail without exposing the raw body shape.
func describe(status int, detail string) string {
	if detail == "" {
		return "the provider returned status " + http.StatusText(status)
	}
	return "the provider returned status " + http.StatusText(status) + ": " + detail
}

// isContentRefusal reports whether the detail names a content refusal, which
// the boundary treats as permanent rather than retryable.
func isContentRefusal(detail string) bool {
	lowered := strings.ToLower(detail)
	for _, marker := range []string{"content policy", "safety", "blocked", "refused"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// shouldRetry reports whether an error is worth another attempt.
func shouldRetry(err error) bool {
	if provider.IsContentPolicy(err) || provider.IsMissingCredential(err) ||
		provider.IsNotAvailable(err) || provider.IsInvalidProvider(err) {
		return false
	}
	var permanent *permanentError
	if errors.As(err, &permanent) {
		return false
	}
	return provider.IsTransient(err) || provider.IsRateLimitReset(err) ||
		provider.IsMalformedResponse(err) || isTimeout(err)
}

// isTimeout reports whether an error is a wall-clock or connection timeout.
func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	if errors.As(err, &t) {
		return t.Timeout()
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// baseURL returns the configured endpoint with any trailing slash removed.
func (c *Client) baseURL() string {
	if c.Settings.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.Settings.BaseURL, "/")
}

// timeout resolves the per-attempt bound, defaulting to the client's.
func (c *Client) timeout() time.Duration {
	if c.Settings.Timeout > 0 {
		return c.Settings.Timeout
	}
	return 120 * time.Second
}

// doer returns the configured transport, defaulting to a plain client.
func (c *Client) doer() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// sleep waits between attempts, defaulting to the real clock.
func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

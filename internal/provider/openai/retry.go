package openai

import (
	"context"
	"errors"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// sendWithRetry performs one logical call, retrying transient failures with
// capped exponential backoff. The per-attempt wall-clock bound is applied via
// the request context so a stalled response fails rather than pinning the
// caller. Permits are not held while sleeping.
func (c *Client) sendWithRetry(ctx context.Context, path string, body []byte, policy provider.RetryPolicy, scope string) ([]byte, error) {
	policy = policy.Normalize()
	deadline := time.Time{}
	if policy.Budget > 0 {
		deadline = c.now().Add(policy.Budget)
	}
	var last error
	for attempt := 0; ; attempt++ {
		if !deadline.IsZero() && c.now().After(deadline) {
			return nil, exhausted(last, "retry budget elapsed")
		}
		raw, err := c.attempt(ctx, path, body)
		if err == nil {
			return raw, nil
		}
		last = err
		if !shouldRetry(err) {
			return nil, err
		}
		if attempt >= policy.MaxRetries {
			return nil, exhausted(last, "")
		}
		delay := policy.BackoffFor(attempt)
		if reset, ok := asRateLimit(err); ok && reset.WithinBackoff {
			delay = time.Until(reset.RetryAt)
			if delay <= 0 {
				delay = policy.BackoffFor(attempt)
			}
		}
		if !deadline.IsZero() && c.now().Add(delay).After(deadline) {
			return nil, exhausted(last, "retry budget would be exceeded by the next backoff")
		}
		c.sleep(delay)
	}
}

// attempt performs a single HTTP attempt with its own wall-clock bound.
func (c *Client) attempt(ctx context.Context, path string, body []byte) ([]byte, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.TimeoutFor(0))
	defer cancel()

	resp, err := c.send(attemptCtx, path, body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &provider.TransientError{Cause: err}
	}
	// The response body is owned here: every exit path below returns only
	// derived data, so closing it now cannot strand a caller with a live body.
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := readBody(resp)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if readErr != nil {
			return nil, &provider.MalformedResponseError{Detail: "cannot read response body", Cause: readErr}
		}
		return raw, nil
	}
	return nil, classifyResponse(resp.StatusCode, resp.Header, raw, c.now())
}

// shouldRetry reports whether an error is worth another attempt. A
// content-policy refusal and a missing credential are permanent, so they are
// raised without transport retry.
func shouldRetry(err error) bool {
	if provider.IsContentPolicy(err) || provider.IsMissingCredential(err) {
		return false
	}
	if provider.IsNotAvailable(err) || provider.IsInvalidProvider(err) {
		return false
	}
	return provider.IsTransient(err) || provider.IsRateLimitReset(err) ||
		provider.IsMalformedResponse(err) || isTimeout(err)
}

// asRateLimit extracts the reset time from a rate-limit error.
func asRateLimit(err error) (*provider.RateLimitResetError, bool) {
	if !provider.IsRateLimitReset(err) {
		return nil, false
	}
	reset, ok := err.(*provider.RateLimitResetError)
	return reset, ok
}

// isTimeout reports whether an error is a wall-clock or connection timeout.
func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	if !errors.As(err, &t) {
		return false
	}
	return t.Timeout()
}

// exhausted wraps the last error so the caller sees the real cause while the
// boundary still reports the transient class.
func exhausted(last error, note string) error {
	if last == nil {
		last = provider.ErrMalformedResponse
	}
	if note == "" {
		if provider.IsTransient(last) {
			return last
		}
		return &provider.TransientError{Cause: last}
	}
	return &provider.TransientError{Cause: &retryExhausted{cause: last, note: note}}
}

// retryExhausted carries the last cause plus a note about why retrying stopped.
type retryExhausted struct {
	cause error
	note  string
}

func (e *retryExhausted) Error() string { return e.note }
func (e *retryExhausted) Unwrap() error { return e.cause }

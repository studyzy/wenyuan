package openai

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// test-rate-limit-reset-surfaced: a rate limit with a known reset raises a
// distinct error carrying the retry-at time.
func TestRateLimitResetSurfaced(t *testing.T) {
	now := time.Unix(1000, 0)
	header := http.Header{}
	header.Set("Retry-After", "30")
	err := classifyResponse(http.StatusTooManyRequests, header, []byte(`{"error":{"message":"slow down"}}`), now)
	if !provider.IsRateLimitReset(err) {
		t.Fatalf("error is %T, want rate-limit-reset", err)
	}
	reset, ok := err.(*provider.RateLimitResetError)
	if !ok {
		t.Fatal("not a typed rate-limit error")
	}
	if !reset.RetryAt.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("retry at = %s, want %s", reset.RetryAt, now.Add(30*time.Second))
	}
	if !reset.WithinBackoff {
		t.Fatal("reset within the backoff window should be flagged")
	}
}

// An X-RateLimit-Reset header is honoured when Retry-After is absent.
func TestRateLimitResetHeaderAlternatives(t *testing.T) {
	now := time.Unix(2000, 0)
	header := http.Header{}
	header.Set("X-RateLimit-Reset", "15")
	err := classifyResponse(http.StatusTooManyRequests, header, nil, now)
	reset := err.(*provider.RateLimitResetError)
	if !reset.RetryAt.Equal(now.Add(15 * time.Second)) {
		t.Fatalf("retry at = %s", reset.RetryAt)
	}
}

// A 5xx is classified as transient so the retry loop will re-attempt it.
func TestServerErrorIsTransient(t *testing.T) {
	err := classifyResponse(http.StatusBadGateway, http.Header{}, []byte("gateway"), time.Now())
	if !provider.IsTransient(err) {
		t.Fatalf("error is %T, want transient", err)
	}
}

// A content-policy 403 is classified as permanent, not as a credential
// problem.
func TestContentPolicyForbiddenIsPermanent(t *testing.T) {
	err := classifyResponse(http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"flagged by safety"}}`), time.Now())
	if !provider.IsContentPolicy(err) {
		t.Fatalf("error is %T, want content-policy", err)
	}
}

// An unknown non-success status falls back to the malformed-response class.
func TestUnknownStatusIsMalformed(t *testing.T) {
	err := classifyResponse(http.StatusTeapot, http.Header{}, nil, time.Now())
	if !provider.IsMalformedResponse(err) {
		t.Fatalf("error is %T, want malformed-response", err)
	}
}

// The error message is extracted from the vendor envelope.
func TestErrorMessageExtraction(t *testing.T) {
	got := decodeErrorMessage([]byte(`{"error":{"message":"specific reason"}}`))
	if got != "specific reason" {
		t.Fatalf("message = %q", got)
	}
	got = decodeErrorMessage([]byte("plain text"))
	if got != "plain text" {
		t.Fatalf("fallback message = %q", got)
	}
}

// test-error-detail-bounded: a large non-envelope error body must not ride
// into an error message unbounded, because callers surface the message.
func TestErrorDetailIsBounded(t *testing.T) {
	huge := strings.Repeat("x", maxErrorDetailChars*4)
	detail := decodeErrorMessage([]byte(huge))
	if len(detail) != maxErrorDetailChars {
		t.Fatalf("raw body detail = %d chars, want it clipped to %d", len(detail), maxErrorDetailChars)
	}
	if err := classifyResponse(http.StatusBadRequest, http.Header{}, []byte(huge), time.Now()); len(err.Error()) > maxErrorDetailChars+128 {
		t.Fatalf("classified error message is not bounded: %d chars", len(err.Error()))
	}
}

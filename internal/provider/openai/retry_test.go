package openai

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// test-transient-error-retried: a transient failure is retried with capped
// exponential backoff and then succeeds.
func TestTransientFailureRetriedThenSucceeds(t *testing.T) {
	c, doer := testClient(
		doubleResponse{status: http.StatusBadGateway, body: "boom"},
		jsonResponse(chatBody("recovered", "stop")),
	)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recovered" {
		t.Fatalf("content = %q", result.Content)
	}
	if doer.calls != 2 {
		t.Fatalf("attempts = %d, want 2", doer.calls)
	}
}

// test-transient-exhausted-reraised: an exhausted budget re-raises the last
// error.
func TestExhaustedBudgetReraisesLastError(t *testing.T) {
	c, doer := testClient(
		doubleResponse{status: http.StatusBadGateway, body: "boom"},
		doubleResponse{status: http.StatusBadGateway, body: "boom"},
		doubleResponse{status: http.StatusBadGateway, body: "boom"},
		doubleResponse{status: http.StatusBadGateway, body: "boom"},
	)
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if err == nil {
		t.Fatal("an exhausted budget must raise an error")
	}
	if !provider.IsTransient(err) {
		t.Fatalf("error is %T, want transient", err)
	}
	// MaxRetries=2 means three attempts total.
	if doer.calls != 3 {
		t.Fatalf("attempts = %d, want 3", doer.calls)
	}
}

// A rate-limit reset within the backoff window is raised carrying the
// retry-at time after the budget is exhausted.
func TestRateLimitResetRaisesDistinctError(t *testing.T) {
	c, _ := testClient(
		doubleResponse{status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "1"}},
		doubleResponse{status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "1"}},
		doubleResponse{status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "1"}},
		doubleResponse{status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "1"}},
	)
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsRateLimitReset(err) {
		t.Fatalf("error is %T, want rate-limit-reset", err)
	}
}

// test-timeout-bounds-attempt: a stalled response fails at the configured
// wall-clock timeout.
func TestTimeoutBoundsAttempt(t *testing.T) {
	settings := clientSettings()
	settings.Timeout = 20 * time.Millisecond
	c := NewClient(settings, &stallingTransport{})
	c.Sleep = func(time.Duration) {}
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if err == nil {
		t.Fatal("a stalled attempt must fail at the timeout")
	}
}

// A call-level retry override replaces the scope policy.
func TestCallRetryOverrideWins(t *testing.T) {
	c, doer := testClient(
		doubleResponse{status: http.StatusBadGateway, body: "boom"},
		jsonResponse(chatBody("ok", "stop")),
	)
	policy := provider.RetryPolicy{MaxRetries: 0, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	_, err := c.Complete(context.Background(), provider.CompletionRequest{Retry: &policy})
	// With zero retries the first transient failure is final.
	if err == nil {
		t.Fatal("a zero-retry override must not retry")
	}
	if doer.calls != 1 {
		t.Fatalf("attempts = %d, want 1", doer.calls)
	}
}

// The concurrency bound composes: a per-operation cap tighter than the global
// cap is the bound that applies.
func TestConcurrencyCompositionApplies(t *testing.T) {
	settings := clientSettings()
	settings.Concurrency = provider.ConcurrencyPolicy{Global: 16, PerOperation: 2}
	if got := settings.Concurrency.Effective(); got != 2 {
		t.Fatalf("effective concurrency = %d, want 2", got)
	}
}

// stallingTransport blocks until the request context expires.
type stallingTransport struct{}

func (s *stallingTransport) Do(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

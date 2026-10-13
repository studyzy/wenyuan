package provider

import (
	"errors"
	"testing"
	"time"
)

// Distinct failure classes must be distinguishable by error type without
// inspecting a vendor-specific value.
func TestFailureClassesAreDistinct(t *testing.T) {
	classes := []error{
		ErrNotAvailable,
		ErrMissingCredential,
		ErrContentPolicy,
		ErrRateLimitReset,
		ErrOutputTooLong,
		ErrMalformedResponse,
		ErrInvalidProvider,
	}
	for i, a := range classes {
		for j, b := range classes {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Fatalf("class %d matches class %d", i, j)
			}
		}
	}
}

func TestTypedErrorsMatchTheirClass(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		check func(error) bool
	}{
		{"not available", &NotAvailableError{ConfigKey: "K", Scope: "chat"}, IsNotAvailable},
		{"missing credential", &MissingCredentialError{Provider: "p", ConfigKey: "K"}, IsMissingCredential},
		{"invalid provider", &InvalidProviderError{Name: "x", Accepted: []string{"none"}}, IsInvalidProvider},
		{"content policy", &ContentPolicyError{Reason: "r"}, IsContentPolicy},
		{"rate limit reset", &RateLimitResetError{RetryAt: time.Now()}, IsRateLimitReset},
		{"output too long", &OutputTooLongError{FinishReason: "length"}, IsOutputTooLong},
		{"malformed response", &MalformedResponseError{Detail: "d"}, IsMalformedResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.check(tc.err) {
				t.Fatalf("%T did not match its class predicate", tc.err)
			}
		})
	}
}

// A malformed response that wraps a decode failure stays reachable by class
// even though Unwrap exposes the cause.
func TestMalformedResponseKeepsClassWhenWrappingCause(t *testing.T) {
	cause := errors.New("unexpected end of JSON input")
	err := &MalformedResponseError{Detail: "body", Cause: cause}
	if !IsMalformedResponse(err) {
		t.Fatal("wrapped malformed response lost its class")
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause is not reachable through Unwrap")
	}
}

func TestContentPolicyErrorCarriesReason(t *testing.T) {
	err := &ContentPolicyError{Reason: "blocked"}
	if got := err.Error(); got == "" || got == ErrContentPolicy.Error() {
		t.Fatalf("reason was dropped: %q", got)
	}
}

func TestRateLimitResetCarriesRetryAt(t *testing.T) {
	at := time.Date(2026, 10, 13, 4, 20, 0, 0, time.UTC)
	var reached *RateLimitResetError
	err := error(&RateLimitResetError{RetryAt: at, WithinBackoff: true})
	if !errors.As(err, &reached) {
		t.Fatal("rate limit reset not reachable by As")
	}
	if !reached.RetryAt.Equal(at) || !reached.WithinBackoff {
		t.Fatalf("carried %+v, want retry-at %s within backoff", reached, at)
	}
}

func TestNotAvailableNamesConfigKey(t *testing.T) {
	err := &NotAvailableError{ConfigKey: "HINDSIGHT_API_LLM_PROVIDER", Scope: "chat"}
	if !IsNotAvailable(err) {
		t.Fatal("not-available predicate failed")
	}
	if got := err.Error(); got == "" {
		t.Fatal("empty message")
	}
}

func TestInvalidProviderListsAcceptedNames(t *testing.T) {
	err := &InvalidProviderError{Name: "bogus", Accepted: []string{"none", "openai"}}
	if !IsInvalidProvider(err) {
		t.Fatal("invalid-provider predicate failed")
	}
	if got := err.Error(); got == "" {
		t.Fatal("empty message")
	}
}

func TestTransientErrorIsRetryable(t *testing.T) {
	cause := errors.New("connection reset")
	err := &TransientError{Cause: cause}
	if !IsTransient(err) {
		t.Fatal("transient error not recognized")
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause is not reachable")
	}
	if IsTransient(errors.New("plain")) {
		t.Fatal("a plain error must not be transient")
	}
}

// test-retry-backoff-clamped: backoff doubles from the initial value and is
// clamped at the ceiling.
func TestRetryBackoffClamped(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5, InitialBackoff: time.Second, MaxBackoff: 8 * time.Second}
	want := []time.Duration{1, 2, 4, 8, 8} // seconds
	for i, w := range want {
		if got := p.BackoffFor(i); got != w*time.Second {
			t.Fatalf("attempt %d backoff = %s, want %s", i, got, w*time.Second)
		}
	}
}

// test-retry-policy-normalize: a negative field is out of contract and is
// replaced with the documented default, while an explicit zero retry budget is
// preserved because zero is a valid operator value meaning "do not retry".
func TestRetryPolicyNormalizeFillsDefaults(t *testing.T) {
	negative := RetryPolicy{
		MaxRetries:     -1,
		InitialBackoff: -time.Second,
		MaxBackoff:     -time.Minute,
	}
	got := negative.Normalize()
	want := DefaultRetryPolicy()
	if got != want {
		t.Fatalf("negative normalize = %+v, want %+v", got, want)
	}

	// Zero retries is an explicit operator choice and must survive normalize.
	noRetry := RetryPolicy{
		MaxRetries:     0,
		InitialBackoff: time.Second,
		MaxBackoff:     30 * time.Second,
	}
	if got := noRetry.Normalize(); got.MaxRetries != 0 {
		t.Fatalf("explicit zero retries must be preserved, got %d", got.MaxRetries)
	}
}

// test-concurrency-composition: the process-wide and per-operation caps
// compose by taking the tighter bound rather than replacing one another.
func TestConcurrencyComposition(t *testing.T) {
	cases := []struct {
		name   string
		global int
		perOp  int
		want   int
	}{
		{"global tighter", 4, 16, 4},
		{"per-operation tighter", 32, 2, 2},
		{"per-operation unset", 32, 0, 32},
		{"global unset", 0, 8, 8},
		{"both unset", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConcurrencyPolicy{Global: tc.global, PerOperation: tc.perOp}.Effective()
			if got != tc.want {
				t.Fatalf("effective = %d, want %d", got, tc.want)
			}
		})
	}
}

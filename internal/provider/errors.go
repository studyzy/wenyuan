package provider

import (
	"errors"
	"fmt"
	"time"
)

// The typed failure classes the boundary guarantees. Each is a distinct type so
// a caller can react to one class without inspecting a vendor-specific value.
var (
	// ErrNotAvailable reports that no model is configured, so an operation
	// that needs one cannot proceed. It is a configuration-level outcome, not
	// a transport failure.
	ErrNotAvailable = errors.New("no model provider is available")
	// ErrMissingCredential reports that the selected provider needs a
	// credential and none was configured.
	ErrMissingCredential = errors.New("provider credential is missing")
	// ErrContentPolicy reports a deterministic refusal: the same input earns
	// the same refusal, so it is never retried.
	ErrContentPolicy = errors.New("the provider refused the request on content-policy grounds")
	// ErrAuthRejected reports that the provider rejected the request's
	// credentials (HTTP 401/403). A rejection is a property of the configured
	// key, not of the moment or the content: every replay sends the same key
	// and earns the same rejection (issue #5644), so it is never retried.
	ErrAuthRejected = errors.New("the provider rejected the request's credentials")
	// ErrRateLimitReset reports a rate limit with a known reset time.
	ErrRateLimitReset = errors.New("the provider rate limit was reached")
	// ErrOutputTooLong reports that a generation hit its own output token
	// limit, so its payload may be truncated.
	ErrOutputTooLong = errors.New("the generation reached its output token limit")
	// ErrMalformedResponse reports a success status carrying a body the
	// boundary cannot interpret.
	ErrMalformedResponse = errors.New("the provider response was malformed")
	// ErrInvalidProvider reports a provider name the boundary does not accept.
	ErrInvalidProvider = errors.New("the configured provider name is not accepted")
)

// NotAvailableError names the configuration key an operator must set to make a
// model available. It reports the not-available failure class.
type NotAvailableError struct {
	// ConfigKey is the operator-facing key holding the provider selection.
	ConfigKey string
	// Scope identifies the provider scope that is unconfigured.
	Scope string
}

func (e *NotAvailableError) Error() string {
	scope := e.Scope
	if scope == "" {
		scope = "chat"
	}
	return fmt.Sprintf("%s: %s is not configured for the %s scope", ErrNotAvailable, e.ConfigKey, scope)
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *NotAvailableError) Unwrap() error { return ErrNotAvailable }

// MissingCredentialError names the credential key the selected provider needs.
type MissingCredentialError struct {
	// Provider is the selected provider name.
	Provider string
	// ConfigKey is the credential key.
	ConfigKey string
}

func (e *MissingCredentialError) Error() string {
	return fmt.Sprintf("%s: provider %q requires %s", ErrMissingCredential, e.Provider, e.ConfigKey)
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *MissingCredentialError) Unwrap() error { return ErrMissingCredential }

// InvalidProviderError lists the names the boundary accepts.
type InvalidProviderError struct {
	// Name is the unrecognized selection.
	Name string
	// Accepted names every provider the boundary recognizes.
	Accepted []string
}

func (e *InvalidProviderError) Error() string {
	return fmt.Sprintf("%s: %q is not one of %v", ErrInvalidProvider, e.Name, e.Accepted)
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *InvalidProviderError) Unwrap() error { return ErrInvalidProvider }

// ContentPolicyError carries the provider's reason for a permanent refusal.
type ContentPolicyError struct {
	// Reason is the provider-supplied explanation, when it gave one.
	Reason string
}

func (e *ContentPolicyError) Error() string {
	if e.Reason == "" {
		return ErrContentPolicy.Error()
	}
	return fmt.Sprintf("%s: %s", ErrContentPolicy, e.Reason)
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *ContentPolicyError) Unwrap() error { return ErrContentPolicy }

// AuthError reports a credential rejection the provider answered with 401 or
// 403. It names the provider whose key was rejected so the failure reads as a
// deployment problem rather than a bad request, and it is distinct from the
// missing-credential class: nothing is unconfigured here, the key that was
// sent is wrong.
type AuthError struct {
	// Provider is the provider whose credential was rejected.
	Provider string
	// ConfigKey is the key holding the credential to correct.
	ConfigKey string
	// Reason is the provider-supplied explanation, when it gave one.
	Reason string
}

func (e *AuthError) Error() string {
	message := fmt.Sprintf("%s: provider %q rejected %s", ErrAuthRejected, e.Provider, e.ConfigKey)
	if e.Reason != "" {
		return message + ": " + e.Reason
	}
	return message
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *AuthError) Unwrap() error { return ErrAuthRejected }

// RateLimitResetError carries the moment the provider says capacity returns.
type RateLimitResetError struct {
	// RetryAt is when the caller may try again.
	RetryAt time.Time
	// WithinBackoff reports whether the reset falls inside the caller's
	// maximum backoff window, so waiting is worthwhile rather than futile.
	WithinBackoff bool
}

func (e *RateLimitResetError) Error() string {
	return fmt.Sprintf("%s: capacity returns at %s", ErrRateLimitReset, e.RetryAt.UTC().Format(time.RFC3339))
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *RateLimitResetError) Unwrap() error { return ErrRateLimitReset }

// OutputTooLongError reports that a generation stopped at its own output limit.
type OutputTooLongError struct {
	// FinishReason is the provider-reported stop reason.
	FinishReason string
}

func (e *OutputTooLongError) Error() string {
	if e.FinishReason == "" {
		return ErrOutputTooLong.Error()
	}
	return fmt.Sprintf("%s: the provider reported %q", ErrOutputTooLong, e.FinishReason)
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *OutputTooLongError) Unwrap() error { return ErrOutputTooLong }

// MalformedResponseError describes a success body the boundary cannot read.
type MalformedResponseError struct {
	// Detail explains what was wrong with the body.
	Detail string
	// Cause is the underlying decode failure, when there was one.
	Cause error
}

func (e *MalformedResponseError) Error() string {
	if e.Detail == "" {
		return ErrMalformedResponse.Error()
	}
	return fmt.Sprintf("%s: %s", ErrMalformedResponse, e.Detail)
}

// Unwrap exposes the sentinel so errors.Is works for the whole class, and the
// decode failure so errors.As can reach it.
func (e *MalformedResponseError) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	return ErrMalformedResponse
}

// Is reports a match for the malformed-response class even when a cause is
// attached, so the class stays reachable by errors.Is.
func (e *MalformedResponseError) Is(target error) bool { return target == ErrMalformedResponse }

// TransientError marks a fault worth retrying. It wraps the underlying cause so
// an exhausted retry budget can re-raise the last real error.
type TransientError struct {
	// Cause is the underlying fault.
	Cause error
}

func (e *TransientError) Error() string {
	if e.Cause == nil {
		return "transient provider failure"
	}
	return fmt.Sprintf("transient provider failure: %s", e.Cause)
}

// Unwrap exposes the underlying fault.
func (e *TransientError) Unwrap() error { return e.Cause }

// IsTransient reports whether an error belongs to the retryable class.
func IsTransient(err error) bool {
	var transient *TransientError
	return errors.As(err, &transient)
}

// IsContentPolicy reports whether an error is a permanent content refusal.
func IsContentPolicy(err error) bool { return errors.Is(err, ErrContentPolicy) }

// IsAuthRejected reports whether an error is a permanent credential rejection.
// Like the content-policy class it is a function of the deployment's
// configuration rather than of the moment, so a caller must fail the operation
// on first sight instead of replaying it through the retry budget (#5644).
func IsAuthRejected(err error) bool { return errors.Is(err, ErrAuthRejected) }

// IsPermanent reports whether an error can never succeed on replay, so the
// retry budget must not be spent on it: a content refusal, a rejected
// credential, or a provider the boundary cannot address at all.
func IsPermanent(err error) bool {
	return IsContentPolicy(err) || IsAuthRejected(err) ||
		IsMissingCredential(err) || IsInvalidProvider(err)
}

// IsRateLimitReset reports whether an error carries a known rate-limit reset.
func IsRateLimitReset(err error) bool {
	var reset *RateLimitResetError
	return errors.As(err, &reset)
}

// IsOutputTooLong reports whether an error is the output-limit class.
func IsOutputTooLong(err error) bool { return errors.Is(err, ErrOutputTooLong) }

// IsMalformedResponse reports whether an error is the malformed-body class.
func IsMalformedResponse(err error) bool { return errors.Is(err, ErrMalformedResponse) }

// IsNotAvailable reports whether an error is the no-model class.
func IsNotAvailable(err error) bool { return errors.Is(err, ErrNotAvailable) }

// IsMissingCredential reports whether an error is the missing-credential class.
func IsMissingCredential(err error) bool { return errors.Is(err, ErrMissingCredential) }

// IsInvalidProvider reports whether an error is the unknown-name class.
func IsInvalidProvider(err error) bool { return errors.Is(err, ErrInvalidProvider) }

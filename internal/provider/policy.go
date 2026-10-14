package provider

import "time"

// Default retry and backoff values applied when a scope configures nothing.
const (
	// DefaultMaxRetries is the attempt budget beyond the first try.
	DefaultMaxRetries = 3
	// DefaultInitialBackoff is the first retry delay.
	DefaultInitialBackoff = time.Second
	// DefaultMaxBackoff is the backoff ceiling.
	DefaultMaxBackoff = 60 * time.Second
	// DefaultMaxConcurrent is the process-wide simultaneous chat-call cap.
	DefaultMaxConcurrent = 32
	// DefaultTimeout is the per-request wall-clock bound.
	DefaultTimeout = 120 * time.Second
)

// RetryPolicy is the retry tuning for one call.
type RetryPolicy struct {
	// MaxRetries is how many times a transient failure is retried.
	MaxRetries int
	// InitialBackoff is the first retry delay.
	InitialBackoff time.Duration
	// MaxBackoff is the ceiling the exponential backoff is clamped to.
	MaxBackoff time.Duration
	// Budget caps total wall-clock time across attempts; zero means unbounded.
	Budget time.Duration
}

// DefaultRetryPolicy returns the policy applied when a scope configures none.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     DefaultMaxRetries,
		InitialBackoff: DefaultInitialBackoff,
		MaxBackoff:     DefaultMaxBackoff,
	}
}

// Normalize replaces non-positive fields with the documented defaults so a
// partially configured scope never disables retries by accident.
func (p RetryPolicy) Normalize() RetryPolicy {
	if p.MaxRetries < 0 {
		p.MaxRetries = DefaultMaxRetries
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = DefaultInitialBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = DefaultMaxBackoff
	}
	if p.MaxBackoff < p.InitialBackoff {
		p.MaxBackoff = p.InitialBackoff
	}
	return p
}

// BackoffFor returns the delay before the given attempt. Attempt 0 is the
// first retry. The delay doubles from the initial value and is clamped at the
// ceiling, so it never grows without bound.
func (p RetryPolicy) BackoffFor(attempt int) time.Duration {
	p = p.Normalize()
	if attempt < 0 {
		attempt = 0
	}
	delay := p.InitialBackoff
	for i := 0; i < attempt; i++ {
		if delay >= p.MaxBackoff {
			return p.MaxBackoff
		}
		delay *= 2
	}
	if delay > p.MaxBackoff {
		return p.MaxBackoff
	}
	return delay
}

// ConcurrencyPolicy bounds simultaneous calls at two layers that compose.
type ConcurrencyPolicy struct {
	// Global caps simultaneous chat calls process-wide.
	Global int
	// PerOperation caps simultaneous calls for one operation scope.
	PerOperation int
}

// Effective returns the tighter of the two bounds, treating a non-positive
// value as unset. A result of zero means unbounded.
func (c ConcurrencyPolicy) Effective() int {
	return tighter(c.Global, c.PerOperation)
}

// tighter returns the smaller positive value, or zero when both are unset.
func tighter(a, b int) int {
	if a <= 0 {
		if b <= 0 {
			return 0
		}
		return b
	}
	if b <= 0 || a <= b {
		return a
	}
	return b
}

// Settings is the resolved, pure inputs an adapter is built from. It carries
// no process state and holds no handle, so it can be constructed and compared
// in tests without a live environment.
type Settings struct {
	// Provider is the selected provider name.
	Provider string
	// APIKey authenticates the selected provider; empty when none is needed.
	APIKey string
	// BaseURL is the endpoint; empty lets the adapter apply its default.
	BaseURL string
	// Model is the model identifier.
	Model string
	// Timeout is the per-attempt wall-clock bound; zero means the adapter default.
	Timeout time.Duration
	// ConnectTimeout bounds only the connection phase.
	ConnectTimeout time.Duration
	// Retry is the retry tuning for this scope.
	Retry RetryPolicy
	// Concurrency bounds simultaneous calls for this scope.
	Concurrency ConcurrencyPolicy
	// MaxTokens caps generated output; zero means unset.
	MaxTokens int
	// Temperature is the sampling temperature; nil means omit the parameter.
	Temperature *float64
	// ReasoningEffort is the requested reasoning level; empty means unset.
	ReasoningEffort string
	// StrictSchema is the resolved structured-output strictness.
	StrictSchema bool
	// JSONMode overrides whether a JSON-object response mode is used.
	JSONMode *bool
	// ServiceTier selects the optional cheaper tier; empty means unset.
	ServiceTier string
	// OutputLanguage forces generated content language; empty means unset.
	OutputLanguage string
	// SendBankAsUser attributes calls to the calling tenant.
	SendBankAsUser bool
	// CacheAffinity pins a provider-side prompt cache; empty means unset.
	CacheAffinity string
	// PromptCacheEnabled reuses a bank-agnostic system prefix.
	PromptCacheEnabled bool
	// ExtraBody merges provider-specific request fields.
	ExtraBody map[string]any
	// Headers are extra request headers.
	Headers map[string]string
	// Vision overrides whether the backend reads image parts; nil means defer.
	Vision *bool
	// SupportsMaxItems declares the backend accepts a maximum-items keyword.
	SupportsMaxItems bool
	// SupportsStringPattern declares the backend accepts a string-pattern keyword.
	SupportsStringPattern bool
	// ForcedTool routes structured output through a function tool.
	ForcedTool bool
	// Embedding holds the parallel embedding settings; nil when unused.
	Embedding *EmbeddingSettings
	// Rerank holds the cross-encoder settings; nil when unused, which selects
	// the best-per-arm fallback the recall pipeline has always scored with.
	Rerank *RerankSettings
}

// RerankSettings is the resolved cross-encoder configuration.
type RerankSettings struct {
	// Provider is the selected rerank provider name.
	Provider string
	// APIKey authenticates the vendor-scoped rerank endpoint.
	APIKey string
	// BaseURL is the rerank endpoint.
	BaseURL string
	// Model is the reranking model identifier; empty means the adapter's
	// default.
	Model string
	// CandidateDepth bounds how many candidates reach the reranker.
	CandidateDepth int
	// Retry is the retry tuning for rerank calls.
	Retry RetryPolicy
}

// EmbeddingSettings is the resolved embedding-provider configuration.
type EmbeddingSettings struct {
	// Provider is the selected embedding provider name.
	Provider string
	// APIKey authenticates the vendor-scoped embedding endpoint.
	APIKey string
	// BaseURL is the embedding endpoint.
	BaseURL string
	// Model is the embedding model identifier.
	Model string
	// BatchSize is how many inputs go in one provider request.
	BatchSize int
	// VendorDimensions is the adapter-level requested width; it never changes
	// the destination's stored width.
	VendorDimensions int
	// MaxInputTokens caps each input's length.
	MaxInputTokens int
	// PassagePrefix is prepended to stored passages.
	PassagePrefix string
	// QueryPrefix is prepended to query text.
	QueryPrefix string
	// Retry is the retry tuning for embedding calls.
	Retry RetryPolicy
	// MaxConcurrentRequests bounds in-flight requests per encode call.
	MaxConcurrentRequests int
	// LocalModel selects a local embedding model; empty means unused.
	LocalModel string
	// LocalForceCPU forces local inference onto the CPU.
	LocalForceCPU bool
	// LocalTrustRemoteCode opts into remote model code.
	LocalTrustRemoteCode bool
	// AllowMPS is the removed accelerator opt-in, retained only so the
	// resolution can warn that it has no effect.
	AllowMPS bool
	// AllowMPSWasSet records whether the removed flag was present.
	AllowMPSWasSet bool
}

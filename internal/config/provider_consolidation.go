package config

import (
	"strconv"
	"strings"
	"time"
)

// Consolidation-scope configuration keys. The consolidation role has its own
// independently configured provider scope; every field is unset by default
// and falls back to the corresponding global value, so leaving the scope
// untouched reproduces the single-provider behaviour exactly.
const (
	EnvConsolidationProvider            = "HINDSIGHT_API_CONSOLIDATION_LLM_PROVIDER"
	EnvConsolidationAPIKey              = "HINDSIGHT_API_CONSOLIDATION_LLM_API_KEY"
	EnvConsolidationModel               = "HINDSIGHT_API_CONSOLIDATION_LLM_MODEL"
	EnvConsolidationBaseURL             = "HINDSIGHT_API_CONSOLIDATION_LLM_BASE_URL"
	EnvConsolidationMaxConcurrent       = "HINDSIGHT_API_CONSOLIDATION_LLM_MAX_CONCURRENT"
	EnvConsolidationMaxRetries          = "HINDSIGHT_API_CONSOLIDATION_LLM_MAX_RETRIES"
	EnvConsolidationInitialBackoff      = "HINDSIGHT_API_CONSOLIDATION_LLM_INITIAL_BACKOFF"
	EnvConsolidationMaxBackoff          = "HINDSIGHT_API_CONSOLIDATION_LLM_MAX_BACKOFF"
	EnvConsolidationTimeout             = "HINDSIGHT_API_CONSOLIDATION_LLM_TIMEOUT"
	EnvConsolidationStrategy            = "HINDSIGHT_API_CONSOLIDATION_LLM_STRATEGY"
	EnvConsolidationLiteLLMRouterConfig = "HINDSIGHT_API_CONSOLIDATION_LLM_LITELLMROUTER_CONFIG"
	EnvConsolidationReasoningEffort     = "HINDSIGHT_API_CONSOLIDATION_LLM_REASONING_EFFORT"
	EnvConsolidationExtraBody           = "HINDSIGHT_API_CONSOLIDATION_LLM_EXTRA_BODY"
	EnvConsolidationCacheAffinity       = "HINDSIGHT_API_CONSOLIDATION_LLM_CACHE_AFFINITY"
	EnvConsolidationBatchSize           = "HINDSIGHT_API_CONSOLIDATION_LLM_BATCH_SIZE"
	EnvConsolidationParallelism         = "HINDSIGHT_API_CONSOLIDATION_LLM_PARALLELISM"
)

// consolidationKeys lists every consolidation key so a test can assert none is
// dropped.
func consolidationKeys() []string {
	return []string{
		EnvConsolidationProvider, EnvConsolidationAPIKey, EnvConsolidationModel,
		EnvConsolidationBaseURL, EnvConsolidationMaxConcurrent, EnvConsolidationMaxRetries,
		EnvConsolidationInitialBackoff, EnvConsolidationMaxBackoff, EnvConsolidationTimeout,
		EnvConsolidationStrategy, EnvConsolidationLiteLLMRouterConfig,
		EnvConsolidationReasoningEffort, EnvConsolidationExtraBody,
		EnvConsolidationCacheAffinity, EnvConsolidationBatchSize, EnvConsolidationParallelism,
	}
}

// ConsolidationKeys returns every consolidation-scope key.
func ConsolidationKeys() []string { return append([]string(nil), consolidationKeys()...) }

// ConsolidationScope is the resolved second provider scope. A nil pointer
// field means the key was unset, so it stays distinguishable from a concrete
// value and from an explicit false. It is resolvable without requiring the
// consolidation behaviour path to exist.
type ConsolidationScope struct {
	Provider        *string
	APIKey          *string
	Model           *string
	BaseURL         *string
	MaxConcurrent   *int
	MaxRetries      *int
	InitialBackoff  *time.Duration
	MaxBackoff      *time.Duration
	Timeout         *time.Duration
	Strategy        *string
	RouterConfig    *[]string
	ReasoningEffort *string
	ExtraBody       map[string]any
	CacheAffinity   *string
	BatchSize       *int
	Parallelism     *int

	// Raw retains every consolidation key's value so no key is dropped.
	Raw map[string]string
}

// ResolveConsolidation resolves the consolidation provider scope. Each field
// is read only when the key is present and non-blank, so an unset field stays
// nil and inherits the global value at use time.
func ResolveConsolidation(lookup func(string) (string, bool)) ConsolidationScope {
	scope := ConsolidationScope{Raw: map[string]string{}}
	for _, key := range consolidationKeys() {
		if v, ok := lookup(key); ok {
			scope.Raw[key] = v
		}
	}
	scope.Provider = optionalString(lookup, EnvConsolidationProvider)
	scope.APIKey = optionalString(lookup, EnvConsolidationAPIKey)
	scope.Model = optionalString(lookup, EnvConsolidationModel)
	scope.BaseURL = optionalString(lookup, EnvConsolidationBaseURL)
	scope.MaxConcurrent = optionalPositiveInt(lookup, EnvConsolidationMaxConcurrent)
	scope.MaxRetries = optionalInt(lookup, EnvConsolidationMaxRetries)
	scope.InitialBackoff = optionalDuration(lookup, EnvConsolidationInitialBackoff)
	scope.MaxBackoff = optionalDuration(lookup, EnvConsolidationMaxBackoff)
	scope.Timeout = optionalDuration(lookup, EnvConsolidationTimeout)
	scope.Strategy = optionalString(lookup, EnvConsolidationStrategy)
	scope.RouterConfig = optionalList(lookup, EnvConsolidationLiteLLMRouterConfig)
	scope.ReasoningEffort = optionalString(lookup, EnvConsolidationReasoningEffort)
	scope.ExtraBody = parseJSONObject(stringFromEnv(lookup, EnvConsolidationExtraBody))
	scope.CacheAffinity = optionalString(lookup, EnvConsolidationCacheAffinity)
	scope.BatchSize = optionalInt(lookup, EnvConsolidationBatchSize)
	scope.Parallelism = optionalInt(lookup, EnvConsolidationParallelism)
	return scope
}

// EffectiveProvider returns the consolidation provider, falling back to the
// global provider when the scope did not set one.
func (s ConsolidationScope) EffectiveProvider(global ProviderConfig) string {
	if s.Provider != nil {
		return *s.Provider
	}
	return global.Provider
}

// EffectiveModel returns the consolidation model, falling back to the global
// model when the scope did not set one.
func (s ConsolidationScope) EffectiveModel(global ProviderConfig) string {
	if s.Model != nil {
		return *s.Model
	}
	return global.Model
}

// EffectiveMaxRetries returns the consolidation attempt budget, falling back
// to the global value field by field.
func (s ConsolidationScope) EffectiveMaxRetries(global ProviderConfig) int {
	if s.MaxRetries != nil {
		return *s.MaxRetries
	}
	return global.MaxRetries
}

// EffectiveTimeout returns the consolidation timeout, falling back to global.
func (s ConsolidationScope) EffectiveTimeout(global ProviderConfig) time.Duration {
	if s.Timeout != nil {
		return *s.Timeout
	}
	return global.Timeout
}

// EffectiveReasoningEffort returns the consolidation reasoning level, falling
// back to global without normalizing unset to a concrete level.
func (s ConsolidationScope) EffectiveReasoningEffort(global ProviderConfig) string {
	if s.ReasoningEffort != nil {
		return *s.ReasoningEffort
	}
	return global.ReasoningEffort
}

// optionalString returns a pointer only when the key is present and non-blank.
func optionalString(lookup func(string) (string, bool), key string) *string {
	v := stringFromEnv(lookup, key)
	if v == "" {
		return nil
	}
	return &v
}

// optionalInt returns a pointer only when the key parses to an integer.
func optionalInt(lookup func(string) (string, bool), key string) *int {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return &n
}

// optionalPositiveInt returns a pointer only for a positive integer value.
func optionalPositiveInt(lookup func(string) (string, bool), key string) *int {
	n := optionalInt(lookup, key)
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}

// optionalDuration returns a pointer only when the key is present and parses.
func optionalDuration(lookup func(string) (string, bool), key string) *time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	d := durationFromEnv(lookup, key, 0)
	if d == 0 {
		return nil
	}
	return &d
}

// optionalList returns a pointer only when the key has entries.
func optionalList(lookup func(string) (string, bool), key string) *[]string {
	list := splitList(stringFromEnv(lookup, key))
	if list == nil {
		return nil
	}
	return &list
}

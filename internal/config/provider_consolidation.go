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
	EnvConsolidationProvider            = "WENYUAN_API_CONSOLIDATION_LLM_PROVIDER"
	EnvConsolidationAPIKey              = "WENYUAN_API_CONSOLIDATION_LLM_API_KEY"
	EnvConsolidationModel               = "WENYUAN_API_CONSOLIDATION_LLM_MODEL"
	EnvConsolidationBaseURL             = "WENYUAN_API_CONSOLIDATION_LLM_BASE_URL"
	EnvConsolidationMaxConcurrent       = "WENYUAN_API_CONSOLIDATION_LLM_MAX_CONCURRENT"
	EnvConsolidationMaxRetries          = "WENYUAN_API_CONSOLIDATION_LLM_MAX_RETRIES"
	EnvConsolidationInitialBackoff      = "WENYUAN_API_CONSOLIDATION_LLM_INITIAL_BACKOFF"
	EnvConsolidationMaxBackoff          = "WENYUAN_API_CONSOLIDATION_LLM_MAX_BACKOFF"
	EnvConsolidationTimeout             = "WENYUAN_API_CONSOLIDATION_LLM_TIMEOUT"
	EnvConsolidationStrategy            = "WENYUAN_API_CONSOLIDATION_LLM_STRATEGY"
	EnvConsolidationLiteLLMRouterConfig = "WENYUAN_API_CONSOLIDATION_LLM_LITELLMROUTER_CONFIG"
	EnvConsolidationReasoningEffort     = "WENYUAN_API_CONSOLIDATION_LLM_REASONING_EFFORT"
	EnvConsolidationExtraBody           = "WENYUAN_API_CONSOLIDATION_LLM_EXTRA_BODY"
	EnvConsolidationCacheAffinity       = "WENYUAN_API_CONSOLIDATION_LLM_CACHE_AFFINITY"
	EnvConsolidationBatchSize           = "WENYUAN_API_CONSOLIDATION_LLM_BATCH_SIZE"
	EnvConsolidationParallelism         = "WENYUAN_API_CONSOLIDATION_LLM_PARALLELISM"
)

// llmScopeSuffixes are the per-role chat-scope key suffixes every role shares.
var llmScopeSuffixes = []string{
	"PROVIDER", "API_KEY", "MODEL", "BASE_URL", "MAX_CONCURRENT", "MAX_RETRIES",
	"INITIAL_BACKOFF", "MAX_BACKOFF", "TIMEOUT", "STRATEGY",
	"LITELLMROUTER_CONFIG", "REASONING_EFFORT", "EXTRA_BODY", "CACHE_AFFINITY",
}

// llmScopeExtraSuffixes are the role-specific keys the contract carries.
var llmScopeExtraSuffixes = map[string][]string{
	"WENYUAN_API_CONSOLIDATION_LLM": {"BATCH_SIZE", "PARALLELISM"},
}

// llmScopeKeys returns the resolved key list for a role's chat scope.
func llmScopeKeys(prefix string) []string {
	suffixes := append([]string(nil), llmScopeSuffixes...)
	suffixes = append(suffixes, llmScopeExtraSuffixes[prefix]...)
	keys := make([]string, 0, len(suffixes))
	for _, suffix := range suffixes {
		keys = append(keys, prefix+"_"+suffix)
	}
	return keys
}

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

// LLMScope is one role's independently configured chat scope. A nil pointer
// field means the key was unset, so it stays distinguishable from a concrete
// value and from an explicit false. It is resolvable without requiring the
// per-role routing behaviour to exist.
type LLMScope struct {
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

	// Raw retains every key's value read for this scope so no key is dropped.
	Raw map[string]string
}

// ConsolidationScope is the consolidation role's scope.
type ConsolidationScope = LLMScope

// ResolveLLMScope resolves one role's chat scope from its key prefix. Each
// field is read only when the key is present and non-blank, so an unset field
// stays nil and inherits the global value at use time.
func ResolveLLMScope(lookup func(string) (string, bool), prefix string) LLMScope {
	scope := LLMScope{Raw: map[string]string{}}
	for _, key := range llmScopeKeys(prefix) {
		if v, ok := lookup(key); ok {
			scope.Raw[key] = v
		}
	}
	key := func(suffix string) string { return prefix + "_" + suffix }
	scope.Provider = optionalString(lookup, key("PROVIDER"))
	scope.APIKey = optionalString(lookup, key("API_KEY"))
	scope.Model = optionalString(lookup, key("MODEL"))
	scope.BaseURL = optionalString(lookup, key("BASE_URL"))
	scope.MaxConcurrent = optionalPositiveInt(lookup, key("MAX_CONCURRENT"))
	scope.MaxRetries = optionalInt(lookup, key("MAX_RETRIES"))
	scope.InitialBackoff = optionalDuration(lookup, key("INITIAL_BACKOFF"))
	scope.MaxBackoff = optionalDuration(lookup, key("MAX_BACKOFF"))
	scope.Timeout = optionalDuration(lookup, key("TIMEOUT"))
	scope.Strategy = optionalString(lookup, key("STRATEGY"))
	scope.RouterConfig = optionalList(lookup, key("LITELLMROUTER_CONFIG"))
	scope.ReasoningEffort = optionalString(lookup, key("REASONING_EFFORT"))
	scope.ExtraBody = parseJSONObject(stringFromEnv(lookup, key("EXTRA_BODY")))
	scope.CacheAffinity = optionalString(lookup, key("CACHE_AFFINITY"))
	if extra := llmScopeExtraSuffixes[prefix]; len(extra) > 0 {
		scope.BatchSize = optionalInt(lookup, key("BATCH_SIZE"))
		scope.Parallelism = optionalInt(lookup, key("PARALLELISM"))
	}
	return scope
}

// ResolveConsolidation resolves the consolidation provider scope.
func ResolveConsolidation(lookup func(string) (string, bool)) ConsolidationScope {
	return ResolveLLMScope(lookup, "WENYUAN_API_CONSOLIDATION_LLM")
}

// EffectiveProvider returns the scope's provider, falling back to the global
// provider when the scope did not set one.
func (s LLMScope) EffectiveProvider(global ProviderConfig) string {
	if s.Provider != nil {
		return *s.Provider
	}
	return global.Provider
}

// EffectiveModel returns the scope's model, falling back to the global model
// when the scope did not set one.
func (s LLMScope) EffectiveModel(global ProviderConfig) string {
	if s.Model != nil {
		return *s.Model
	}
	return global.Model
}

// EffectiveMaxRetries returns the scope's attempt budget, falling back to the
// global value field by field.
func (s LLMScope) EffectiveMaxRetries(global ProviderConfig) int {
	if s.MaxRetries != nil {
		return *s.MaxRetries
	}
	return global.MaxRetries
}

// EffectiveTimeout returns the scope's timeout, falling back to global.
func (s LLMScope) EffectiveTimeout(global ProviderConfig) time.Duration {
	if s.Timeout != nil {
		return *s.Timeout
	}
	return global.Timeout
}

// EffectiveReasoningEffort returns the scope's reasoning level, falling back to
// global without normalizing unset to a concrete level.
func (s LLMScope) EffectiveReasoningEffort(global ProviderConfig) string {
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

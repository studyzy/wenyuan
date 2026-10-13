package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Provider configuration keys. They are the frozen operator-facing contract
// and are resolved inside the existing environment source, so flags,
// environment, .env, and defaults keep their existing precedence.
const (
	EnvLLMProvider             = "HINDSIGHT_API_LLM_PROVIDER"
	EnvLLMAPIKey               = "HINDSIGHT_API_LLM_API_KEY"
	EnvLLMBaseURL              = "HINDSIGHT_API_LLM_BASE_URL"
	EnvLLMModel                = "HINDSIGHT_API_LLM_MODEL"
	EnvLLMCacheAffinity        = "HINDSIGHT_API_LLM_CACHE_AFFINITY"
	EnvLLMConnectTimeout       = "HINDSIGHT_API_LLM_CONNECT_TIMEOUT"
	EnvLLMDebugDump4XX         = "HINDSIGHT_API_LLM_DEBUG_DUMP_4XX"
	EnvLLMDefaultHeaders       = "HINDSIGHT_API_LLM_DEFAULT_HEADERS"
	EnvLLMExtraBody            = "HINDSIGHT_API_LLM_EXTRA_BODY"
	EnvLLMHTTPLogLevel         = "HINDSIGHT_API_LLM_HTTP_LOG_LEVEL"
	EnvLLMInitialBackoff       = "HINDSIGHT_API_LLM_INITIAL_BACKOFF"
	EnvLLMLiteLLMRouterConfig  = "HINDSIGHT_API_LLM_LITELLMROUTER_CONFIG"
	EnvLLMMaxBackoff           = "HINDSIGHT_API_LLM_MAX_BACKOFF"
	EnvLLMMaxConcurrent        = "HINDSIGHT_API_LLM_MAX_CONCURRENT"
	EnvLLMMaxRetries           = "HINDSIGHT_API_LLM_MAX_RETRIES"
	EnvLLMOpenAIJSONMode       = "HINDSIGHT_API_LLM_OPENAI_COMPATIBLE_JSON_MODE"
	EnvLLMOpenAIServiceTier    = "HINDSIGHT_API_LLM_OPENAI_SERVICE_TIER"
	EnvLLMOutputLanguage       = "HINDSIGHT_API_LLM_OUTPUT_LANGUAGE"
	EnvLLMPromptCacheEnabled   = "HINDSIGHT_API_LLM_PROMPT_CACHE_ENABLED"
	EnvLLMReasoningEffort      = "HINDSIGHT_API_LLM_REASONING_EFFORT"
	EnvLLMSendBankAsUser       = "HINDSIGHT_API_LLM_SEND_BANK_AS_USER"
	EnvLLMStrategy             = "HINDSIGHT_API_LLM_STRATEGY"
	EnvLLMForcedTool           = "HINDSIGHT_API_LLM_STRUCTURED_OUTPUT_FORCED_TOOL"
	EnvLLMSupportsMaxItems     = "HINDSIGHT_API_LLM_SUPPORTS_MAX_ITEMS"
	EnvLLMSupportsStringPattrn = "HINDSIGHT_API_LLM_SUPPORTS_STRING_PATTERN"
	EnvLLMTimeout              = "HINDSIGHT_API_LLM_TIMEOUT"
	EnvLLMTraceEnabled         = "HINDSIGHT_API_LLM_TRACE_ENABLED"
	EnvLLMTraceMaxChars        = "HINDSIGHT_API_LLM_TRACE_MAX_CHARS"
	EnvLLMTraceRetentionDays   = "HINDSIGHT_API_LLM_TRACE_RETENTION_DAYS"
	EnvLLMTraceScopes          = "HINDSIGHT_API_LLM_TRACE_SCOPES"
	EnvLLMVision               = "HINDSIGHT_API_LLM_VISION"

	EnvEmbeddingsProvider        = "HINDSIGHT_API_EMBEDDINGS_PROVIDER"
	EnvEmbeddingsInitialBackoff  = "HINDSIGHT_API_EMBEDDINGS_INITIAL_BACKOFF"
	EnvEmbeddingsLocalAllowMPS   = "HINDSIGHT_API_EMBEDDINGS_LOCAL_ALLOW_MPS"
	EnvEmbeddingsLocalForceCPU   = "HINDSIGHT_API_EMBEDDINGS_LOCAL_FORCE_CPU"
	EnvEmbeddingsLocalModel      = "HINDSIGHT_API_EMBEDDINGS_LOCAL_MODEL"
	EnvEmbeddingsLocalTrustCode  = "HINDSIGHT_API_EMBEDDINGS_LOCAL_TRUST_REMOTE_CODE"
	EnvEmbeddingsMaxBackoff      = "HINDSIGHT_API_EMBEDDINGS_MAX_BACKOFF"
	EnvEmbeddingsMaxConcurrent   = "HINDSIGHT_API_EMBEDDINGS_MAX_CONCURRENT_REQUESTS"
	EnvEmbeddingsMaxInputTokens  = "HINDSIGHT_API_EMBEDDINGS_MAX_INPUT_TOKENS"
	EnvEmbeddingsMaxRetries      = "HINDSIGHT_API_EMBEDDINGS_MAX_RETRIES"
	EnvEmbeddingsOpenAIKey       = "HINDSIGHT_API_EMBEDDINGS_OPENAI_API_KEY"
	EnvEmbeddingsOpenAIBaseURL   = "HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL"
	EnvEmbeddingsOpenAIBatchSize = "HINDSIGHT_API_EMBEDDINGS_OPENAI_BATCH_SIZE"
	EnvEmbeddingsOpenAIDims      = "HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS"
	EnvEmbeddingsOpenAIModel     = "HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL"
	EnvEmbeddingsPassagePrefix   = "HINDSIGHT_API_EMBEDDINGS_PASSAGE_PREFIX"
	EnvEmbeddingsQueryPrefix     = "HINDSIGHT_API_EMBEDDINGS_QUERY_PREFIX"
	EnvEmbeddingsRetryBudget     = "HINDSIGHT_API_EMBEDDINGS_RETRY_BUDGET"
)

// Provider defaults applied when a key resolves to nothing.
const (
	DefaultLLMMaxRetries     = 3
	DefaultLLMInitialBackoff = time.Second
	DefaultLLMMaxBackoff     = 60 * time.Second
	DefaultLLMMaxConcurrent  = 32
	DefaultLLMTimeout        = 120 * time.Second
	DefaultLLMTraceMaxChars  = 4096
	DefaultEmbeddingsBatch   = 64
	DefaultEmbeddingsConcur  = 8
)

// ProviderConfig is the resolved provider configuration. It is a plain value
// with no adapter or transport, so configuration can be resolved and compared
// without a live environment.
type ProviderConfig struct {
	Provider              string
	APIKey                string
	BaseURL               string
	Model                 string
	CacheAffinity         string
	ConnectTimeout        time.Duration
	DebugDump4XX          bool
	DefaultHeaders        map[string]string
	ExtraBody             map[string]any
	HTTPLogLevel          string
	InitialBackoff        time.Duration
	LiteLLMRouterConfig   []string
	MaxBackoff            time.Duration
	MaxConcurrent         int
	MaxRetries            int
	OpenAIJSONMode        *bool
	OpenAIServiceTier     string
	OutputLanguage        string
	PromptCacheEnabled    bool
	ReasoningEffort       string
	SendBankAsUser        bool
	Strategy              string
	ForcedTool            bool
	SupportsMaxItems      bool
	SupportsStringPattern bool
	Timeout               time.Duration
	TraceEnabled          bool
	TraceMaxChars         int
	TraceRetentionDays    int
	TraceScopes           []string
	Vision                *bool

	Embeddings EmbeddingConfig

	// Warnings records operator-visible notes raised during resolution, such
	// as a removed flag that no longer has any effect.
	Warnings []string

	// StrictSchema is the global structured-output strictness, resolved from
	// the strict-schema family. It is materialized here so a consumer reads
	// one resolved boolean rather than re-deriving the precedence.
	StrictSchema bool
}

// EmbeddingConfig is the resolved embedding-provider configuration, resolved
// independently from the chat family.
type EmbeddingConfig struct {
	Provider              string
	OpenAIAPIKey          string
	OpenAIBaseURL         string
	OpenAIModel           string
	OpenAIBatchSize       int
	OpenAIDimensions      int
	MaxInputTokens        int
	PassagePrefix         string
	QueryPrefix           string
	MaxRetries            int
	InitialBackoff        time.Duration
	MaxBackoff            time.Duration
	RetryBudget           time.Duration
	MaxConcurrentRequests int
	LocalModel            string
	LocalForceCPU         bool
	LocalTrustRemoteCode  bool
	AllowMPS              bool
}

// ProviderFields holds every provider key resolved from the environment
// source, so the resolution can be reported and diffed key by key.
type ProviderFields map[string]string

// providerKeys lists every provider key so a test can assert none is dropped.
func providerKeys() []string {
	return []string{
		EnvLLMProvider, EnvLLMAPIKey, EnvLLMBaseURL, EnvLLMModel, EnvLLMCacheAffinity,
		EnvLLMConnectTimeout, EnvLLMDebugDump4XX, EnvLLMDefaultHeaders, EnvLLMExtraBody,
		EnvLLMHTTPLogLevel, EnvLLMInitialBackoff, EnvLLMLiteLLMRouterConfig, EnvLLMMaxBackoff,
		EnvLLMMaxConcurrent, EnvLLMMaxRetries, EnvLLMOpenAIJSONMode, EnvLLMOpenAIServiceTier,
		EnvLLMOutputLanguage, EnvLLMPromptCacheEnabled, EnvLLMReasoningEffort, EnvLLMSendBankAsUser,
		EnvLLMStrategy, EnvLLMForcedTool, EnvLLMSupportsMaxItems, EnvLLMSupportsStringPattrn,
		EnvLLMTimeout, EnvLLMTraceEnabled, EnvLLMTraceMaxChars, EnvLLMTraceRetentionDays,
		EnvLLMTraceScopes, EnvLLMVision,
		EnvEmbeddingsProvider, EnvEmbeddingsInitialBackoff, EnvEmbeddingsLocalAllowMPS,
		EnvEmbeddingsLocalForceCPU, EnvEmbeddingsLocalModel, EnvEmbeddingsLocalTrustCode,
		EnvEmbeddingsMaxBackoff, EnvEmbeddingsMaxConcurrent, EnvEmbeddingsMaxInputTokens,
		EnvEmbeddingsMaxRetries, EnvEmbeddingsOpenAIKey, EnvEmbeddingsOpenAIBaseURL,
		EnvEmbeddingsOpenAIBatchSize, EnvEmbeddingsOpenAIDims, EnvEmbeddingsOpenAIModel,
		EnvEmbeddingsPassagePrefix, EnvEmbeddingsQueryPrefix, EnvEmbeddingsRetryBudget,
	}
}

// ProviderKeys returns every declared provider key. Tests use it to assert
// that resolution covers the full contract.
func ProviderKeys() []string { return append([]string(nil), providerKeys()...) }

// ProviderValues reads every provider key through the supplied lookup so the
// full contract is retained even for keys this unit does not branch on.
func ProviderValues(lookup func(string) (string, bool)) ProviderFields {
	out := ProviderFields{}
	for _, key := range providerKeys() {
		if value, ok := lookup(key); ok {
			out[key] = value
		}
	}
	return out
}

// ResolveProvider resolves the provider configuration from the environment
// source, treating a blank value as unset.
func ResolveProvider(lookup func(string) (string, bool)) ProviderConfig {
	cfg := ProviderConfig{
		MaxRetries:     DefaultLLMMaxRetries,
		InitialBackoff: DefaultLLMInitialBackoff,
		MaxBackoff:     DefaultLLMMaxBackoff,
		MaxConcurrent:  DefaultLLMMaxConcurrent,
		Timeout:        DefaultLLMTimeout,
		TraceMaxChars:  DefaultLLMTraceMaxChars,
		ExtraBody:      map[string]any{},
		DefaultHeaders: map[string]string{},
		Embeddings: EmbeddingConfig{
			OpenAIBatchSize:       DefaultEmbeddingsBatch,
			MaxConcurrentRequests: DefaultEmbeddingsConcur,
			InitialBackoff:        DefaultLLMInitialBackoff,
			MaxBackoff:            DefaultLLMMaxBackoff,
		},
	}
	str := func(key string) string { return stringFromEnv(lookup, key) }

	cfg.Provider = str(EnvLLMProvider)
	cfg.APIKey = str(EnvLLMAPIKey)
	cfg.BaseURL = str(EnvLLMBaseURL)
	cfg.Model = str(EnvLLMModel)
	cfg.CacheAffinity = str(EnvLLMCacheAffinity)
	cfg.ConnectTimeout = durationFromEnv(lookup, EnvLLMConnectTimeout, cfg.ConnectTimeout)
	cfg.DebugDump4XX = boolValue(lookup, EnvLLMDebugDump4XX, false)
	cfg.DefaultHeaders = parseHeaderMap(str(EnvLLMDefaultHeaders))
	cfg.ExtraBody = parseJSONObject(str(EnvLLMExtraBody))
	cfg.HTTPLogLevel = str(EnvLLMHTTPLogLevel)
	cfg.InitialBackoff = durationFromEnv(lookup, EnvLLMInitialBackoff, cfg.InitialBackoff)
	cfg.LiteLLMRouterConfig = splitList(str(EnvLLMLiteLLMRouterConfig))
	cfg.MaxBackoff = durationFromEnv(lookup, EnvLLMMaxBackoff, cfg.MaxBackoff)
	cfg.MaxConcurrent = positiveInt(lookup, EnvLLMMaxConcurrent, cfg.MaxConcurrent)
	cfg.MaxRetries = positiveIntAllowZero(lookup, EnvLLMMaxRetries, cfg.MaxRetries)
	cfg.OpenAIJSONMode = triState(lookup, EnvLLMOpenAIJSONMode)
	cfg.OpenAIServiceTier = str(EnvLLMOpenAIServiceTier)
	cfg.OutputLanguage = str(EnvLLMOutputLanguage)
	cfg.PromptCacheEnabled = boolValue(lookup, EnvLLMPromptCacheEnabled, false)
	cfg.ReasoningEffort = str(EnvLLMReasoningEffort)
	cfg.SendBankAsUser = boolValue(lookup, EnvLLMSendBankAsUser, false)
	cfg.Strategy = str(EnvLLMStrategy)
	cfg.ForcedTool = boolValue(lookup, EnvLLMForcedTool, false)
	cfg.SupportsMaxItems = boolValue(lookup, EnvLLMSupportsMaxItems, false)
	cfg.SupportsStringPattern = boolValue(lookup, EnvLLMSupportsStringPattrn, false)
	cfg.Timeout = durationFromEnv(lookup, EnvLLMTimeout, cfg.Timeout)
	cfg.TraceEnabled = boolValue(lookup, EnvLLMTraceEnabled, false)
	cfg.TraceMaxChars = positiveInt(lookup, EnvLLMTraceMaxChars, cfg.TraceMaxChars)
	cfg.TraceRetentionDays = intFromEnv(lookup, EnvLLMTraceRetentionDays, 0)
	cfg.TraceScopes = splitList(str(EnvLLMTraceScopes))
	cfg.Vision = triState(lookup, EnvLLMVision)

	cfg.Embeddings = resolveEmbeddings(lookup, cfg.Embeddings)
	return cfg
}

// resolveEmbeddings resolves the embedding family independently from chat.
func resolveEmbeddings(lookup func(string) (string, bool), base EmbeddingConfig) EmbeddingConfig {
	c := base
	str := func(key string) string { return stringFromEnv(lookup, key) }
	c.Provider = str(EnvEmbeddingsProvider)
	c.OpenAIAPIKey = str(EnvEmbeddingsOpenAIKey)
	c.OpenAIBaseURL = str(EnvEmbeddingsOpenAIBaseURL)
	c.OpenAIModel = str(EnvEmbeddingsOpenAIModel)
	c.OpenAIBatchSize = positiveInt(lookup, EnvEmbeddingsOpenAIBatchSize, c.OpenAIBatchSize)
	c.OpenAIDimensions = intFromEnv(lookup, EnvEmbeddingsOpenAIDims, 0)
	c.MaxInputTokens = intFromEnv(lookup, EnvEmbeddingsMaxInputTokens, 0)
	c.PassagePrefix = str(EnvEmbeddingsPassagePrefix)
	c.QueryPrefix = str(EnvEmbeddingsQueryPrefix)
	c.MaxRetries = positiveIntAllowZero(lookup, EnvEmbeddingsMaxRetries, c.MaxRetries)
	c.InitialBackoff = durationFromEnv(lookup, EnvEmbeddingsInitialBackoff, c.InitialBackoff)
	c.MaxBackoff = durationFromEnv(lookup, EnvEmbeddingsMaxBackoff, c.MaxBackoff)
	c.RetryBudget = durationFromEnv(lookup, EnvEmbeddingsRetryBudget, 0)
	c.MaxConcurrentRequests = positiveInt(lookup, EnvEmbeddingsMaxConcurrent, c.MaxConcurrentRequests)
	c.LocalModel = str(EnvEmbeddingsLocalModel)
	c.LocalForceCPU = boolValue(lookup, EnvEmbeddingsLocalForceCPU, false)
	c.LocalTrustRemoteCode = boolValue(lookup, EnvEmbeddingsLocalTrustCode, false)
	c.AllowMPS = boolValue(lookup, EnvEmbeddingsLocalAllowMPS, false)
	return c
}

// ProviderWarnings reports the operator-visible notes raised by resolution.
// The removed local accelerator opt-in is read only to warn that it has no
// effect, so a deployment that set it gets an explicit notice rather than a
// silent change of behaviour.
func ProviderWarnings(cfg ProviderConfig) []string {
	warnings := append([]string(nil), cfg.Warnings...)
	if cfg.Embeddings.AllowMPS {
		warnings = append(warnings, EnvEmbeddingsLocalAllowMPS+
			" is set but the local accelerator opt-in was removed and has no effect")
	}
	return warnings
}

// stringFromEnv reads a key, treating a blank value as unset.
func stringFromEnv(lookup func(string) (string, bool), key string) string {
	v, ok := lookup(key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// boolValue reads a boolean key, falling back when absent or unrecognized.
func boolValue(lookup func(string) (string, bool), key string, fallback bool) bool {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	if parsed, valid := parseBoolValue(v); valid {
		return parsed
	}
	return fallback
}

// triState reads a key that must keep unset distinguishable from an explicit
// false. A blank or absent value yields nil.
func triState(lookup func(string) (string, bool), key string) *bool {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	if parsed, valid := parseBoolValue(v); valid {
		return &parsed
	}
	return nil
}

// intFromEnv reads an integer key, falling back when absent or unparseable.
func intFromEnv(lookup func(string) (string, bool), key string, fallback int) int {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// positiveInt reads an integer key constrained to a positive value.
func positiveInt(lookup func(string) (string, bool), key string, fallback int) int {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// positiveIntAllowZero reads a non-negative integer key, so a zero attempt
// budget stays expressible.
func positiveIntAllowZero(lookup func(string) (string, bool), key string, fallback int) int {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// durationFromEnv reads a duration in seconds or a Go duration string.
func durationFromEnv(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
		return d
	}
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
		return time.Duration(seconds * float64(time.Second))
	}
	return fallback
}

// splitList splits a comma-separated value into trimmed, non-empty entries.
func splitList(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	out := []string{}
	for _, part := range strings.Split(trimmed, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseHeaderMap parses a comma-separated key:value header list.
func parseHeaderMap(value string) map[string]string {
	out := map[string]string{}
	for _, pair := range splitList(value) {
		name, val, ok := strings.Cut(pair, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		out[name] = strings.TrimSpace(val)
	}
	return out
}

// parseJSONObject parses a JSON object value, ignoring a malformed one.
func parseJSONObject(value string) map[string]any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return map[string]any{}
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}

// ValidatePositiveInt raises a value error naming an offending key when a key
// that admits only a positive integer carries a non-positive value.
func ValidatePositiveInt(key, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return fmt.Errorf("%s must be an integer, got %q", key, value)
	}
	if n <= 0 {
		return fmt.Errorf("%s must be a positive integer, got %d", key, n)
	}
	return nil
}

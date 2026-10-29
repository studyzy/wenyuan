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
	EnvLLMProvider             = "WENYUAN_API_LLM_PROVIDER"
	EnvLLMAPIKey               = "WENYUAN_API_LLM_API_KEY"
	EnvLLMBaseURL              = "WENYUAN_API_LLM_BASE_URL"
	EnvLLMModel                = "WENYUAN_API_LLM_MODEL"
	EnvLLMCacheAffinity        = "WENYUAN_API_LLM_CACHE_AFFINITY"
	EnvLLMConnectTimeout       = "WENYUAN_API_LLM_CONNECT_TIMEOUT"
	EnvLLMDebugDump4XX         = "WENYUAN_API_LLM_DEBUG_DUMP_4XX"
	EnvLLMDefaultHeaders       = "WENYUAN_API_LLM_DEFAULT_HEADERS"
	EnvLLMExtraBody            = "WENYUAN_API_LLM_EXTRA_BODY"
	EnvLLMHTTPLogLevel         = "WENYUAN_API_LLM_HTTP_LOG_LEVEL"
	EnvLLMInitialBackoff       = "WENYUAN_API_LLM_INITIAL_BACKOFF"
	EnvLLMLiteLLMRouterConfig  = "WENYUAN_API_LLM_LITELLMROUTER_CONFIG"
	EnvLLMMaxBackoff           = "WENYUAN_API_LLM_MAX_BACKOFF"
	EnvLLMMaxConcurrent        = "WENYUAN_API_LLM_MAX_CONCURRENT"
	EnvLLMMaxRetries           = "WENYUAN_API_LLM_MAX_RETRIES"
	EnvLLMOpenAIJSONMode       = "WENYUAN_API_LLM_OPENAI_COMPATIBLE_JSON_MODE"
	EnvLLMOpenAIServiceTier    = "WENYUAN_API_LLM_OPENAI_SERVICE_TIER"
	EnvLLMOutputLanguage       = "WENYUAN_API_LLM_OUTPUT_LANGUAGE"
	EnvLLMPromptCacheEnabled   = "WENYUAN_API_LLM_PROMPT_CACHE_ENABLED"
	EnvLLMReasoningEffort      = "WENYUAN_API_LLM_REASONING_EFFORT"
	EnvLLMSendBankAsUser       = "WENYUAN_API_LLM_SEND_BANK_AS_USER"
	EnvLLMStrategy             = "WENYUAN_API_LLM_STRATEGY"
	EnvLLMForcedTool           = "WENYUAN_API_LLM_STRUCTURED_OUTPUT_FORCED_TOOL"
	EnvLLMSupportsMaxItems     = "WENYUAN_API_LLM_SUPPORTS_MAX_ITEMS"
	EnvLLMSupportsStringPattrn = "WENYUAN_API_LLM_SUPPORTS_STRING_PATTERN"
	EnvLLMTimeout              = "WENYUAN_API_LLM_TIMEOUT"
	EnvLLMTraceEnabled         = "WENYUAN_API_LLM_TRACE_ENABLED"
	EnvLLMTraceExcludeScopes   = "WENYUAN_API_LLM_TRACE_EXCLUDE_SCOPES"
	EnvLLMTraceMaxChars        = "WENYUAN_API_LLM_TRACE_MAX_CHARS"
	EnvLLMTraceRetentionDays   = "WENYUAN_API_LLM_TRACE_RETENTION_DAYS"
	EnvLLMTraceScopes          = "WENYUAN_API_LLM_TRACE_SCOPES"
	EnvLLMVision               = "WENYUAN_API_LLM_VISION"

	EnvEmbeddingsProvider        = "WENYUAN_API_EMBEDDINGS_PROVIDER"
	EnvEmbeddingsInitialBackoff  = "WENYUAN_API_EMBEDDINGS_INITIAL_BACKOFF"
	EnvEmbeddingsLocalAllowMPS   = "WENYUAN_API_EMBEDDINGS_LOCAL_ALLOW_MPS"
	EnvEmbeddingsLocalForceCPU   = "WENYUAN_API_EMBEDDINGS_LOCAL_FORCE_CPU"
	EnvEmbeddingsLocalModel      = "WENYUAN_API_EMBEDDINGS_LOCAL_MODEL"
	EnvEmbeddingsLocalTrustCode  = "WENYUAN_API_EMBEDDINGS_LOCAL_TRUST_REMOTE_CODE"
	EnvEmbeddingsMaxBackoff      = "WENYUAN_API_EMBEDDINGS_MAX_BACKOFF"
	EnvEmbeddingsMaxConcurrent   = "WENYUAN_API_EMBEDDINGS_MAX_CONCURRENT_REQUESTS"
	EnvEmbeddingsMaxInputTokens  = "WENYUAN_API_EMBEDDINGS_MAX_INPUT_TOKENS"
	EnvEmbeddingsMaxRetries      = "WENYUAN_API_EMBEDDINGS_MAX_RETRIES"
	EnvEmbeddingsOpenAIKey       = "WENYUAN_API_EMBEDDINGS_OPENAI_API_KEY"
	EnvEmbeddingsOpenAIBaseURL   = "WENYUAN_API_EMBEDDINGS_OPENAI_BASE_URL"
	EnvEmbeddingsOpenAIBatchSize = "WENYUAN_API_EMBEDDINGS_OPENAI_BATCH_SIZE"
	EnvEmbeddingsOpenAIDims      = "WENYUAN_API_EMBEDDINGS_OPENAI_DIMENSIONS"
	EnvEmbeddingsOpenAIModel     = "WENYUAN_API_EMBEDDINGS_OPENAI_MODEL"
	EnvEmbeddingsPassagePrefix   = "WENYUAN_API_EMBEDDINGS_PASSAGE_PREFIX"
	EnvEmbeddingsQueryPrefix     = "WENYUAN_API_EMBEDDINGS_QUERY_PREFIX"
	EnvEmbeddingsRetryBudget     = "WENYUAN_API_EMBEDDINGS_RETRY_BUDGET"
)

// Provider defaults applied when a key resolves to nothing. They are the
// contract document's own defaults, so an operator leaving a key unset gets the
// same behaviour as the reference.
const (
	DefaultLLMMaxRetries        = 3
	DefaultLLMInitialBackoff    = time.Second
	DefaultLLMMaxBackoff        = 60 * time.Second
	DefaultLLMMaxConcurrent     = 32
	DefaultLLMTimeout           = 120 * time.Second
	DefaultLLMConnectTimeout    = 10 * time.Second
	DefaultLLMHTTPLogLevel      = "WARNING"
	DefaultLLMTraceMaxChars     = 50000
	DefaultLLMTraceRetentionDay = 1
	DefaultEmbeddingsBatch      = 100
	// DefaultEmbeddingsTEIBatchSize is the texts per TEI /embed request. TEI
	// validates this as a hard cap (its own --max-client-batch-size, 32), so a
	// larger value fails the request rather than being clamped, and it is not
	// shared with the OpenAI default.
	DefaultEmbeddingsTEIBatchSize = 32
	DefaultEmbeddingsConcur       = 8
	DefaultEmbeddingsMaxRetries   = 4
	// DefaultEmbeddingsInitialBackoff, DefaultEmbeddingsMaxBackoff and
	// DefaultEmbeddingsRetryBudget are the contract's retry envelope for the
	// embedding scope.
	DefaultEmbeddingsInitialBackoff = 500 * time.Millisecond
	DefaultEmbeddingsMaxBackoff     = 4 * time.Second
	DefaultEmbeddingsRetryBudget    = 15 * time.Second
)

// Reranker defaults applied when a key resolves to nothing, mirroring the
// reference's DEFAULT_RERANKER_* values so an operator leaving a key blank gets
// the same behaviour. DefaultEmbeddingDimension is the reference's
// DEFAULT_EMBEDDING_DIMENSION, the stored vector width when unset.
const (
	DefaultEmbeddingDimension = 384

	DefaultRerankerCandidateDepth        = 300
	DefaultRerankerMaxTokensPerCandidate = 512
	DefaultRerankerMaxRetries            = 3
	DefaultRerankerInitialBackoff        = 500 * time.Millisecond
	DefaultRerankerMaxBackoff            = 4 * time.Second
	DefaultRerankerRetryBudget           = 10 * time.Second
	DefaultRerankerTimeout               = 60 * time.Second

	DefaultRerankerTEIBatchSize      = 32
	DefaultRerankerTEIMaxConcurrent  = 8
	DefaultRerankerTEIHTTPTimeout    = 30 * time.Second
	DefaultRerankerTypeSafeMaxConcur = 8

	DefaultRerankerLocalMaxConcurrent = 4
	DefaultRerankerLocalBatchSize     = 32
	DefaultRerankerLocalTimeout       = 300 * time.Second
	DefaultRerankerFlashRankBatchSize = 32

	DefaultRerankerSiliconflowBaseURL = "https://api.siliconflow.cn/v1"
	DefaultRerankerSiliconflowModel   = "BAAI/bge-reranker-v2-m3"
	DefaultRerankerCohereModel        = "rerank-english-v3.0"
	DefaultRerankerZeroEntropyBaseURL = "https://api.zeroentropy.dev"
	DefaultRerankerZeroEntropyModel   = "zerank-2"
	DefaultRerankerOpenRouterBaseURL  = "https://openrouter.ai/api/v1"
	DefaultRerankerOpenRouterModel    = "cohere/rerank-english-v3.0"
	DefaultRerankerTypeSafeBaseURL    = "https://api.typesafe.ai"
	DefaultRerankerTypeSafeModel      = "jev"
	DefaultRerankerAlibabaModel       = "gte-rerank-v2"
	DefaultRerankerGoogleModel        = "semantic-ranker-default-004"
	DefaultRerankerLiteLLMBaseURL     = "http://localhost:4000"
	DefaultRerankerLiteLLMModel       = "rerank-english-v3.0"
	DefaultRerankerLiteLLMSDKModel    = "cohere/rerank-english-v3.0"
	DefaultRerankerLocalModel         = "BAAI/bge-reranker-v2-m3"
	DefaultRerankerFlashRankModel     = "ms-marco-MiniLM-L-12-v2"
)

// ProviderConfig is the resolved provider configuration. It is a plain value
// with no adapter or transport, so configuration can be resolved and compared
// without a live environment.
type ProviderConfig struct {
	Provider            string
	APIKey              string
	BaseURL             string
	Model               string
	CacheAffinity       string
	ConnectTimeout      time.Duration
	DebugDump4XX        bool
	DefaultHeaders      map[string]string
	ExtraBody           map[string]any
	HTTPLogLevel        string
	InitialBackoff      time.Duration
	LiteLLMRouterConfig []string
	MaxBackoff          time.Duration
	MaxConcurrent       int
	// MaxRetries is the global LLM retry budget.
	MaxRetries int
	// RetainMaxRetries is the retain operation's retry budget, which also bounds
	// how many times a drifted extraction is re-asked with a correction. It
	// falls back to MaxRetries when the retain-scoped key is unset.
	RetainMaxRetries      int
	OpenAIJSONMode        *bool
	OpenAIServiceTier     string
	OutputLanguage        string
	PromptCacheEnabled    bool
	ReasoningEffort       string
	SendBankAsUser        bool
	ForcedTool            bool
	SupportsMaxItems      bool
	SupportsStringPattern bool
	Timeout               time.Duration
	TraceEnabled          bool
	TraceMaxChars         int
	TraceRetentionDays    int
	TraceScopes           []string
	TraceExcludeScopes    []string
	Vision                *bool

	Embeddings EmbeddingConfig

	// Warnings records operator-visible notes raised during resolution, such
	// as a removed flag that no longer has any effect.
	Warnings []string

	// StrictSchema is the global structured-output strictness, resolved from
	// the strict-schema family. It is materialized here so a consumer reads
	// one resolved boolean rather than re-deriving the precedence.
	StrictSchema bool

	// Strategy is the resolved multi-LLM routing strategy for the global chat
	// scope, parsed from WENYUAN_API_LLM_STRATEGY.
	Strategy StrategyConfig
	// StrategyError records a malformed strategy document, in any scope, so
	// startup can fail loudly instead of silently routing to the primary
	// member. It is nil when every configured strategy is well-formed.
	StrategyError error
	// LLMMembers are the indexed extra members (1..N) of the global chain,
	// read from WENYUAN_API_LLM_{n}_*. The unindexed settings are the
	// primary member and stay in the fields above.
	LLMMembers []LLMMemberConfig

	// Per-role strategies and member chains mirror the global scope, read from
	// the corresponding WENYUAN_API_{RETAIN,REFLECT,CONSOLIDATION,
	// MENTAL_MODEL_REFRESH}_LLM_* keys.
	RetainStrategy               StrategyConfig
	ReflectStrategy              StrategyConfig
	ConsolidationStrategy        StrategyConfig
	MentalModelRefreshStrategy   StrategyConfig
	RetainLLMMembers             []LLMMemberConfig
	ReflectLLMMembers            []LLMMemberConfig
	ConsolidationLLMMembers      []LLMMemberConfig
	MentalModelRefreshLLMMembers []LLMMemberConfig

	// Rerank is the resolved reranker chain configuration. The chain is
	// independent from the chat and embedding scopes, so it resolves here and
	// the recall pipeline consumes it directly.
	Rerank RerankerConfig
}

// StrategyConfig is the parsed multi-LLM routing strategy. Mode is one of
// "failover", "round-robin" or "metadata"; Weights belongs to round-robin and
// Routes to metadata, exactly as the reference validates them.
type StrategyConfig struct {
	Mode    string
	Weights []int
	Routes  []MetadataRoute
}

// MetadataRoute sends an item whose metadata[Key] == Value to Member.
type MetadataRoute struct {
	Key    string
	Value  string
	Member int
}

// LLMMemberConfig is one indexed extra member of a multi-LLM chain. It mirrors
// the subset of settings an indexed member supports; the unindexed settings
// remain the primary member (index 0).
type LLMMemberConfig struct {
	Index           int
	Provider        string
	APIKey          string
	Model           string
	BaseURL         string
	ReasoningEffort string
	CacheAffinity   string
	ExtraBody       map[string]any
	DefaultHeaders  map[string]string
	// Timeout and MaxRetries are per-member overrides of the operation's
	// request policy. MaxRetriesSet keeps an explicit zero distinguishable
	// from an unset override that inherits the operation value.
	Timeout       time.Duration
	MaxRetries    int
	MaxRetriesSet bool
}

// RerankerConfig is the resolved reranker chain: the primary member (index 0)
// from the unindexed WENYUAN_API_RERANKER_* settings and any indexed fallback
// members (1..N) from WENYUAN_API_RERANKER_{n}_*.
type RerankerConfig struct {
	Provider           string
	Primary            RerankerMember
	Members            []RerankerMember
	CandidateDepth     int
	CandidateDepthLow  int
	CandidateDepthMid  int
	CandidateDepthHigh int
	SendBankAsHeader   bool
	MaxRetries         int
	InitialBackoff     time.Duration
	MaxBackoff         time.Duration
	RetryBudget        time.Duration
}

// RerankerMember is one reranker in the chain. An indexed member inherits
// nothing from the primary, so every setting it needs is spelled out with its
// own index; unset settings fall back to the same built-in defaults the primary
// uses.
type RerankerMember struct {
	Index int

	Provider              string
	MaxTokensPerCandidate int

	TEIURL           string
	TEIBatchSize     int
	TEIMaxConcurrent int
	TEIHTTPTimeout   time.Duration

	CohereAPIKey  string
	CohereModel   string
	CohereBaseURL string
	CohereTimeout time.Duration

	OpenRouterAPIKey  string
	OpenRouterModel   string
	OpenRouterBaseURL string
	OpenRouterTimeout time.Duration

	ZeroEntropyAPIKey  string
	ZeroEntropyModel   string
	ZeroEntropyBaseURL string
	ZeroEntropyTimeout time.Duration

	SiliconflowAPIKey  string
	SiliconflowModel   string
	SiliconflowBaseURL string
	SiliconflowTimeout time.Duration

	TypeSafeAPIKey          string
	TypeSafeModel           string
	TypeSafeBaseURL         string
	TypeSafeTimeout         time.Duration
	TypeSafeMaxConcurrent   int
	TypeSafePruneCandidates bool

	GoogleModel             string
	GoogleProjectID         string
	GoogleServiceAccountKey string
	GoogleTimeout           time.Duration

	AlibabaAPIKey  string
	AlibabaModel   string
	AlibabaTimeout time.Duration

	LiteLLMAPIBase string
	LiteLLMAPIKey  string
	LiteLLMModel   string
	LiteLLMTimeout time.Duration
	// LiteLLMMaxTokensPerDoc is the deprecated LiteLLM-specific alias of
	// MaxTokensPerCandidate, kept so a deployment using it still resolves.
	LiteLLMMaxTokensPerDoc int

	LiteLLMSDKAPIKey  string
	LiteLLMSDKModel   string
	LiteLLMSDKAPIBase string
	LiteLLMSDKTimeout time.Duration

	LocalModel           string
	LocalForceCPU        bool
	LocalTrustRemoteCode bool
	LocalFP16            bool
	LocalBucketBatching  bool
	LocalMaxConcurrent   int
	LocalBatchSize       int
	LocalTimeout         time.Duration

	FlashRankModel       string
	FlashRankCacheDir    string
	FlashRankCPUMemArena bool
	FlashRankBatchSize   int
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

	// Dimensions is the stored vector width, resolved from
	// WENYUAN_API_EMBEDDING_DIMENSION and defaulting to the reference's
	// DEFAULT_EMBEDDING_DIMENSION when unset.
	Dimensions int

	// Cohere.
	CohereAPIKey           string
	CohereBaseURL          string
	CohereModel            string
	CohereOutputDimensions int

	// TEI.
	TEIURL       string
	TEIBatchSize int

	// Gemini.
	GeminiAPIKey               string
	GeminiModel                string
	GeminiBatchSize            int
	GeminiOutputDimensionality int
	GeminiForceIPv4            bool

	// Vertex AI.
	VertexProjectID         string
	VertexRegion            string
	VertexServiceAccountKey string

	// ZeroEntropy.
	ZeroEntropyAPIKey         string
	ZeroEntropyBaseURL        string
	ZeroEntropyModel          string
	ZeroEntropyBatchSize      int
	ZeroEntropyDimensions     int
	ZeroEntropyEncodingFormat string
	ZeroEntropyLatency        string

	// OpenRouter.
	OpenRouterAPIKey string
	OpenRouterModel  string

	// Requesty.
	RequestyAPIKey string
	RequestyModel  string

	// LiteLLM proxy.
	LiteLLMAPIBase    string
	LiteLLMAPIKey     string
	LiteLLMModel      string
	LiteLLMDimensions int

	// LiteLLM SDK.
	LiteLLMSDKAPIKey           string
	LiteLLMSDKModel            string
	LiteLLMSDKAPIBase          string
	LiteLLMSDKModelID          string
	LiteLLMSDKEncodingFormat   string
	LiteLLMSDKOutputDimensions int
	LiteLLMSDKMaxInputTokens   int
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
		EnvLLMTraceScopes, EnvLLMTraceExcludeScopes, EnvLLMVision,
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
		MaxRetries:       DefaultLLMMaxRetries,
		InitialBackoff:   DefaultLLMInitialBackoff,
		MaxBackoff:       DefaultLLMMaxBackoff,
		MaxConcurrent:    DefaultLLMMaxConcurrent,
		Timeout:          DefaultLLMTimeout,
		ConnectTimeout:   DefaultLLMConnectTimeout,
		HTTPLogLevel:     DefaultLLMHTTPLogLevel,
		TraceMaxChars:    DefaultLLMTraceMaxChars,
		CacheAffinity:    "auto",
		SupportsMaxItems: true,
		ExtraBody:        map[string]any{},
		DefaultHeaders:   map[string]string{},
		Embeddings: EmbeddingConfig{
			OpenAIBatchSize:       DefaultEmbeddingsBatch,
			MaxConcurrentRequests: DefaultEmbeddingsConcur,
			MaxRetries:            DefaultEmbeddingsMaxRetries,
			InitialBackoff:        DefaultEmbeddingsInitialBackoff,
			MaxBackoff:            DefaultEmbeddingsMaxBackoff,
			RetryBudget:           DefaultEmbeddingsRetryBudget,
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
	if v := str(EnvLLMHTTPLogLevel); v != "" {
		cfg.HTTPLogLevel = v
	}
	cfg.InitialBackoff = durationFromEnv(lookup, EnvLLMInitialBackoff, cfg.InitialBackoff)
	cfg.LiteLLMRouterConfig = splitList(str(EnvLLMLiteLLMRouterConfig))
	cfg.MaxBackoff = durationFromEnv(lookup, EnvLLMMaxBackoff, cfg.MaxBackoff)
	cfg.MaxConcurrent = positiveInt(lookup, EnvLLMMaxConcurrent, cfg.MaxConcurrent)
	cfg.MaxRetries = positiveIntAllowZero(lookup, EnvLLMMaxRetries, cfg.MaxRetries)
	cfg.RetainMaxRetries = positiveIntAllowZero(lookup, EnvRetainLLMMaxRetries, cfg.MaxRetries)
	cfg.OpenAIJSONMode = triState(lookup, EnvLLMOpenAIJSONMode)
	cfg.OpenAIServiceTier = str(EnvLLMOpenAIServiceTier)
	cfg.OutputLanguage = str(EnvLLMOutputLanguage)
	cfg.PromptCacheEnabled = boolValue(lookup, EnvLLMPromptCacheEnabled, false)
	cfg.ReasoningEffort = str(EnvLLMReasoningEffort)
	cfg.SendBankAsUser = boolValue(lookup, EnvLLMSendBankAsUser, false)
	cfg.ForcedTool = boolValue(lookup, EnvLLMForcedTool, false)
	cfg.SupportsMaxItems = boolValue(lookup, EnvLLMSupportsMaxItems, true)
	cfg.SupportsStringPattern = boolValue(lookup, EnvLLMSupportsStringPattrn, false)
	cfg.Timeout = durationFromEnv(lookup, EnvLLMTimeout, cfg.Timeout)
	// The reference enables model-request tracing by default
	// (DEFAULT_LLM_TRACE_ENABLED = True); an unset switch must keep parity so the
	// /llm-requests surface reports the calls a deployment actually made.
	cfg.TraceEnabled = boolValue(lookup, EnvLLMTraceEnabled, true)
	cfg.TraceMaxChars = positiveInt(lookup, EnvLLMTraceMaxChars, cfg.TraceMaxChars)
	cfg.TraceRetentionDays = intFromEnv(lookup, EnvLLMTraceRetentionDays, DefaultLLMTraceRetentionDay)
	cfg.TraceScopes = splitList(str(EnvLLMTraceScopes))
	cfg.TraceExcludeScopes = splitList(str(EnvLLMTraceExcludeScopes))
	cfg.Vision = triState(lookup, EnvLLMVision)

	cfg.Embeddings = resolveEmbeddings(lookup, cfg.Embeddings)

	// Multi-member routing: the indexed member lists come first because a
	// round-robin weight vector is validated against its member count.
	cfg.LLMMembers = resolveLLMMembers(lookup, "WENYUAN_API_LLM_")
	cfg.RetainLLMMembers = resolveLLMMembers(lookup, "WENYUAN_API_RETAIN_LLM_")
	cfg.ReflectLLMMembers = resolveLLMMembers(lookup, "WENYUAN_API_REFLECT_LLM_")
	cfg.ConsolidationLLMMembers = resolveLLMMembers(lookup, "WENYUAN_API_CONSOLIDATION_LLM_")
	cfg.MentalModelRefreshLLMMembers = resolveLLMMembers(lookup, "WENYUAN_API_MENTAL_MODEL_REFRESH_LLM_")

	var errs []error
	cfg.Strategy, errs = parseStrategy(str(EnvLLMStrategy), EnvLLMStrategy, len(cfg.LLMMembers), errs)
	cfg.RetainStrategy, errs = parseStrategy(str(EnvRetainLLMStrategy), EnvRetainLLMStrategy, len(cfg.RetainLLMMembers), errs)
	cfg.ReflectStrategy, errs = parseStrategy(str(EnvReflectLLMStrategy), EnvReflectLLMStrategy, len(cfg.ReflectLLMMembers), errs)
	cfg.ConsolidationStrategy, errs = parseStrategy(str(EnvConsolidationStrategy), EnvConsolidationStrategy, len(cfg.ConsolidationLLMMembers), errs)
	cfg.MentalModelRefreshStrategy, errs = parseStrategy(str(EnvMentalModelRefreshLLMStrategy), EnvMentalModelRefreshLLMStrategy, len(cfg.MentalModelRefreshLLMMembers), errs)
	cfg.StrategyError = combineErrors(errs)

	cfg.Rerank = resolveReranker(lookup)

	return cfg
}

// parseStrategy parses one strategy document, appending a malformed one to the
// error list so every strategy in a run is reported rather than only the first.
func parseStrategy(raw, key string, memberCount int, errs []error) (StrategyConfig, []error) {
	strategy, err := parseStrategyConfig(raw, key, memberCount)
	if err != nil {
		errs = append(errs, err)
	}
	return strategy, errs
}

// combineErrors joins the collected strategy errors into one, or nil.
func combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
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
	c.RetryBudget = durationFromEnv(lookup, EnvEmbeddingsRetryBudget, c.RetryBudget)
	c.MaxConcurrentRequests = positiveInt(lookup, EnvEmbeddingsMaxConcurrent, c.MaxConcurrentRequests)
	c.LocalModel = str(EnvEmbeddingsLocalModel)
	c.LocalForceCPU = boolValue(lookup, EnvEmbeddingsLocalForceCPU, false)
	c.LocalTrustRemoteCode = boolValue(lookup, EnvEmbeddingsLocalTrustCode, false)
	c.AllowMPS = boolValue(lookup, EnvEmbeddingsLocalAllowMPS, false)

	c.Dimensions = intFromEnv(lookup, EnvEmbeddingDimension, DefaultEmbeddingDimension)

	c.CohereAPIKey = str(EnvEmbeddingsCohereAPIKey)
	c.CohereBaseURL = str(EnvEmbeddingsCohereBaseURL)
	c.CohereModel = str(EnvEmbeddingsCohereModel)
	c.CohereOutputDimensions = intFromEnv(lookup, EnvEmbeddingsCohereOutputDimensions, 0)

	c.TEIURL = str(EnvEmbeddingsTeiURL)
	c.TEIBatchSize = intFromEnv(lookup, EnvEmbeddingsTeiBatchSize, DefaultEmbeddingsTEIBatchSize)

	c.GeminiAPIKey = str(EnvEmbeddingsGeminiAPIKey)
	c.GeminiModel = str(EnvEmbeddingsGeminiModel)
	c.GeminiBatchSize = intFromEnv(lookup, EnvEmbeddingsGeminiBatchSize, 0)
	c.GeminiOutputDimensionality = intFromEnv(lookup, EnvEmbeddingsGeminiOutputDimensionality, 0)
	c.GeminiForceIPv4 = boolValue(lookup, EnvEmbeddingsGeminiForceIPv4, false)

	c.VertexProjectID = str(EnvEmbeddingsVertexaiProjectID)
	c.VertexRegion = str(EnvEmbeddingsVertexaiRegion)
	c.VertexServiceAccountKey = str(EnvEmbeddingsVertexaiServiceAccountKey)

	c.ZeroEntropyAPIKey = str(EnvEmbeddingsZeroentropyAPIKey)
	c.ZeroEntropyBaseURL = str(EnvEmbeddingsZeroentropyBaseURL)
	c.ZeroEntropyModel = str(EnvEmbeddingsZeroentropyModel)
	c.ZeroEntropyBatchSize = intFromEnv(lookup, EnvEmbeddingsZeroentropyBatchSize, 0)
	c.ZeroEntropyDimensions = intFromEnv(lookup, EnvEmbeddingsZeroentropyDimensions, 0)
	c.ZeroEntropyEncodingFormat = str(EnvEmbeddingsZeroentropyEncodingFormat)
	c.ZeroEntropyLatency = str(EnvEmbeddingsZeroentropyLatency)

	c.OpenRouterAPIKey = str(EnvEmbeddingsOpenrouterAPIKey)
	c.OpenRouterModel = str(EnvEmbeddingsOpenrouterModel)

	c.RequestyAPIKey = str(EnvEmbeddingsRequestyAPIKey)
	c.RequestyModel = str(EnvEmbeddingsRequestyModel)

	c.LiteLLMAPIBase = str(EnvEmbeddingsLitellmAPIBase)
	c.LiteLLMAPIKey = str(EnvEmbeddingsLitellmAPIKey)
	c.LiteLLMModel = str(EnvEmbeddingsLitellmModel)
	c.LiteLLMDimensions = intFromEnv(lookup, EnvEmbeddingsLitellmDimensions, 0)

	c.LiteLLMSDKAPIKey = str(EnvEmbeddingsLitellmSDKAPIKey)
	c.LiteLLMSDKModel = str(EnvEmbeddingsLitellmSDKModel)
	c.LiteLLMSDKAPIBase = str(EnvEmbeddingsLitellmSDKAPIBase)
	c.LiteLLMSDKModelID = str(EnvEmbeddingsLitellmSDKModelID)
	c.LiteLLMSDKEncodingFormat = str(EnvEmbeddingsLitellmSDKEncodingFormat)
	c.LiteLLMSDKOutputDimensions = intFromEnv(lookup, EnvEmbeddingsLitellmSDKOutputDimensions, 0)
	c.LiteLLMSDKMaxInputTokens = intFromEnv(lookup, EnvEmbeddingsLitellmSDKMaxInputTokens, 0)
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

// intFromString parses a trimmed decimal integer, returning 0 for a blank or
// unparseable value.
func intFromString(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
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

// strategyDocument is the decoded shape of a strategy env var. Unknown fields
// are rejected so a misspelled key fails loudly instead of being ignored.
type strategyDocument struct {
	Mode    string              `json:"mode"`
	Weights []int               `json:"weights"`
	Routes  []metadataRouteJSON `json:"routes"`
}

type metadataRouteJSON struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Member int    `json:"member"`
}

// parseStrategyConfig decodes and validates one strategy document, mirroring
// the reference's rules: mode must be failover, round-robin or metadata;
// weights belong to round-robin, are all positive and count one per member;
// routes belong to metadata and must be non-empty with a key and a
// non-negative member. A malformed document yields an error so startup can
// fail rather than silently route everything to the primary member.
func parseStrategyConfig(raw, key string, memberCount int) (StrategyConfig, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return StrategyConfig{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	var doc strategyDocument
	if err := decoder.Decode(&doc); err != nil {
		return StrategyConfig{}, fmt.Errorf("invalid %s: %v", key, err)
	}
	switch doc.Mode {
	case "failover", "round-robin", "metadata":
	default:
		return StrategyConfig{}, fmt.Errorf(
			"invalid %s: mode %q must be one of failover, round-robin, metadata", key, doc.Mode)
	}
	if doc.Weights != nil {
		if doc.Mode != "round-robin" {
			return StrategyConfig{}, fmt.Errorf("invalid %s: weights is only valid with mode \"round-robin\"", key)
		}
		if len(doc.Weights) == 0 {
			return StrategyConfig{}, fmt.Errorf("invalid %s: weights must be a non-empty list of positive integers", key)
		}
		for _, weight := range doc.Weights {
			if weight <= 0 {
				return StrategyConfig{}, fmt.Errorf("invalid %s: weights must be positive integers", key)
			}
		}
		if len(doc.Weights) != memberCount {
			return StrategyConfig{}, fmt.Errorf(
				"invalid %s: got %d weights for %d members", key, len(doc.Weights), memberCount)
		}
	}
	if doc.Mode == "metadata" {
		if len(doc.Routes) == 0 {
			return StrategyConfig{}, fmt.Errorf(
				"invalid %s: routes must be a non-empty list with mode \"metadata\"", key)
		}
	} else if doc.Routes != nil {
		return StrategyConfig{}, fmt.Errorf("invalid %s: routes is only valid with mode \"metadata\"", key)
	}

	strategy := StrategyConfig{Mode: doc.Mode, Weights: doc.Weights}
	for _, route := range doc.Routes {
		if route.Key == "" {
			return StrategyConfig{}, fmt.Errorf("invalid %s: route key must not be empty", key)
		}
		if route.Member < 0 {
			return StrategyConfig{}, fmt.Errorf("invalid %s: route member must be greater than zero", key)
		}
		strategy.Routes = append(strategy.Routes, MetadataRoute(route))
	}
	return strategy, nil
}

// resolveLLMMembers reads the indexed extra members for one env prefix, which
// ends in "LLM_" (e.g. "WENYUAN_API_LLM_"). Members are read from
// <prefix><n>_PROVIDER for n = 1, 2, ... and scanning stops at the first index
// whose _PROVIDER is unset, so indices must be contiguous from 1.
func resolveLLMMembers(lookup func(string) (string, bool), prefix string) []LLMMemberConfig {
	var members []LLMMemberConfig
	for index := 1; ; index++ {
		base := fmt.Sprintf("%s%d_", prefix, index)
		if stringFromEnv(lookup, base+"PROVIDER") == "" {
			break
		}
		members = append(members, resolveLLMMember(lookup, base, index))
	}
	return members
}

// resolveLLMMember reads one indexed extra member. Timeout and MaxRetries are
// optional overrides; MaxRetriesSet keeps an explicit zero distinguishable from
// an unset one.
func resolveLLMMember(lookup func(string) (string, bool), base string, index int) LLMMemberConfig {
	str := func(suffix string) string { return stringFromEnv(lookup, base+suffix) }
	member := LLMMemberConfig{
		Index:           index,
		Provider:        str("PROVIDER"),
		APIKey:          str("API_KEY"),
		Model:           str("MODEL"),
		BaseURL:         str("BASE_URL"),
		ReasoningEffort: str("REASONING_EFFORT"),
		CacheAffinity:   str("CACHE_AFFINITY"),
		ExtraBody:       parseJSONObject(str("EXTRA_BODY")),
		DefaultHeaders:  parseHeaderMap(str("DEFAULT_HEADERS")),
		Timeout:         durationFromEnv(lookup, base+"TIMEOUT", 0),
	}
	if raw, ok := lookup(base + "MAX_RETRIES"); ok && strings.TrimSpace(raw) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			member.MaxRetries = n
			member.MaxRetriesSet = true
		}
	}
	return member
}

// resolveReranker resolves the reranker chain: the shared chain-level keys, the
// primary member from the unindexed WENYUAN_API_RERANKER_* settings, and the
// indexed members from WENYUAN_API_RERANKER_{n}_*.
func resolveReranker(lookup func(string) (string, bool)) RerankerConfig {
	cfg := RerankerConfig{
		MaxRetries:         DefaultRerankerMaxRetries,
		InitialBackoff:     DefaultRerankerInitialBackoff,
		MaxBackoff:         DefaultRerankerMaxBackoff,
		RetryBudget:        DefaultRerankerRetryBudget,
		CandidateDepth:     DefaultRerankerCandidateDepth,
		CandidateDepthLow:  0,
		CandidateDepthMid:  0,
		CandidateDepthHigh: 0,
	}
	cfg.Provider = stringFromEnv(lookup, EnvRerankerProvider)
	cfg.Primary = resolveRerankerMember(lookup, "WENYUAN_API_RERANKER_", 0)
	cfg.Primary.Provider = cfg.Provider
	cfg.SendBankAsHeader = boolValue(lookup, EnvRerankerSendBankAsHeader, false)
	cfg.MaxRetries = positiveIntAllowZero(lookup, EnvRerankerMaxRetries, cfg.MaxRetries)
	cfg.InitialBackoff = durationFromEnv(lookup, EnvRerankerInitialBackoff, cfg.InitialBackoff)
	cfg.MaxBackoff = durationFromEnv(lookup, EnvRerankerMaxBackoff, cfg.MaxBackoff)
	cfg.RetryBudget = durationFromEnv(lookup, EnvRerankerRetryBudget, cfg.RetryBudget)
	cfg.CandidateDepth = intFromEnv(lookup, EnvRerankerMaxCandidates, cfg.CandidateDepth)
	cfg.CandidateDepthLow = intFromEnv(lookup, EnvRerankerMaxCandidatesLow, cfg.CandidateDepthLow)
	cfg.CandidateDepthMid = intFromEnv(lookup, EnvRerankerMaxCandidatesMid, cfg.CandidateDepthMid)
	cfg.CandidateDepthHigh = intFromEnv(lookup, EnvRerankerMaxCandidatesHigh, cfg.CandidateDepthHigh)

	for index := 1; ; index++ {
		base := fmt.Sprintf("WENYUAN_API_RERANKER_%d_", index)
		if stringFromEnv(lookup, base+"PROVIDER") == "" {
			break
		}
		cfg.Members = append(cfg.Members, resolveRerankerMember(lookup, base, index))
	}
	return cfg
}

// resolveRerankerMember reads one reranker member, applying the built-in
// defaults for every setting the member leaves unset. An indexed member
// inherits nothing from the primary, so base is either the primary prefix or an
// indexed prefix.
func resolveRerankerMember(lookup func(string) (string, bool), base string, index int) RerankerMember {
	str := func(suffix string) string { return stringFromEnv(lookup, base+suffix) }
	member := RerankerMember{
		Index:                  index,
		Provider:               str("PROVIDER"),
		MaxTokensPerCandidate:  DefaultRerankerMaxTokensPerCandidate,
		TEIBatchSize:           DefaultRerankerTEIBatchSize,
		TEIMaxConcurrent:       DefaultRerankerTEIMaxConcurrent,
		TEIHTTPTimeout:         DefaultRerankerTEIHTTPTimeout,
		CohereModel:            DefaultRerankerCohereModel,
		CohereTimeout:          DefaultRerankerTimeout,
		OpenRouterBaseURL:      DefaultRerankerOpenRouterBaseURL,
		OpenRouterModel:        DefaultRerankerOpenRouterModel,
		OpenRouterTimeout:      DefaultRerankerTimeout,
		ZeroEntropyBaseURL:     DefaultRerankerZeroEntropyBaseURL,
		ZeroEntropyModel:       DefaultRerankerZeroEntropyModel,
		ZeroEntropyTimeout:     DefaultRerankerTimeout,
		SiliconflowBaseURL:     DefaultRerankerSiliconflowBaseURL,
		SiliconflowModel:       DefaultRerankerSiliconflowModel,
		SiliconflowTimeout:     DefaultRerankerTimeout,
		TypeSafeBaseURL:        DefaultRerankerTypeSafeBaseURL,
		TypeSafeModel:          DefaultRerankerTypeSafeModel,
		TypeSafeTimeout:        DefaultRerankerTimeout,
		TypeSafeMaxConcurrent:  DefaultRerankerTypeSafeMaxConcur,
		AlibabaModel:           DefaultRerankerAlibabaModel,
		AlibabaTimeout:         DefaultRerankerTimeout,
		GoogleModel:            DefaultRerankerGoogleModel,
		GoogleTimeout:          DefaultRerankerTimeout,
		LiteLLMAPIBase:         DefaultRerankerLiteLLMBaseURL,
		LiteLLMModel:           DefaultRerankerLiteLLMModel,
		LiteLLMTimeout:         DefaultRerankerTimeout,
		LiteLLMMaxTokensPerDoc: DefaultRerankerMaxTokensPerCandidate,
		LiteLLMSDKModel:        DefaultRerankerLiteLLMSDKModel,
		LiteLLMSDKTimeout:      DefaultRerankerTimeout,
		LocalModel:             DefaultRerankerLocalModel,
		LocalMaxConcurrent:     DefaultRerankerLocalMaxConcurrent,
		LocalBatchSize:         DefaultRerankerLocalBatchSize,
		LocalTimeout:           DefaultRerankerLocalTimeout,
		FlashRankModel:         DefaultRerankerFlashRankModel,
		FlashRankBatchSize:     DefaultRerankerFlashRankBatchSize,
	}

	// Optional strings keep their defaults when unset; the API keys and URLs
	// with no default stay empty when unset.
	set := func(dst *string, suffix string) {
		if v := str(suffix); v != "" {
			*dst = v
		}
	}
	set(&member.TEIURL, "TEI_URL")
	set(&member.CohereAPIKey, "COHERE_API_KEY")
	set(&member.CohereModel, "COHERE_MODEL")
	set(&member.CohereBaseURL, "COHERE_BASE_URL")
	set(&member.OpenRouterAPIKey, "OPENROUTER_API_KEY")
	set(&member.OpenRouterModel, "OPENROUTER_MODEL")
	set(&member.OpenRouterBaseURL, "OPENROUTER_BASE_URL")
	set(&member.ZeroEntropyAPIKey, "ZEROENTROPY_API_KEY")
	set(&member.ZeroEntropyModel, "ZEROENTROPY_MODEL")
	set(&member.ZeroEntropyBaseURL, "ZEROENTROPY_BASE_URL")
	set(&member.SiliconflowAPIKey, "SILICONFLOW_API_KEY")
	set(&member.SiliconflowModel, "SILICONFLOW_MODEL")
	set(&member.SiliconflowBaseURL, "SILICONFLOW_BASE_URL")
	set(&member.TypeSafeAPIKey, "TYPESAFE_API_KEY")
	set(&member.TypeSafeModel, "TYPESAFE_MODEL")
	set(&member.TypeSafeBaseURL, "TYPESAFE_BASE_URL")
	set(&member.GoogleModel, "GOOGLE_MODEL")
	set(&member.GoogleProjectID, "GOOGLE_PROJECT_ID")
	set(&member.GoogleServiceAccountKey, "GOOGLE_SERVICE_ACCOUNT_KEY")
	set(&member.AlibabaAPIKey, "ALIBABA_API_KEY")
	set(&member.AlibabaModel, "ALIBABA_MODEL")
	set(&member.LiteLLMAPIBase, "LITELLM_API_BASE")
	set(&member.LiteLLMAPIKey, "LITELLM_API_KEY")
	set(&member.LiteLLMModel, "LITELLM_MODEL")
	set(&member.LiteLLMSDKAPIKey, "LITELLM_SDK_API_KEY")
	set(&member.LiteLLMSDKModel, "LITELLM_SDK_MODEL")
	set(&member.LiteLLMSDKAPIBase, "LITELLM_SDK_API_BASE")
	set(&member.LocalModel, "LOCAL_MODEL")
	set(&member.FlashRankModel, "FLASHRANK_MODEL")
	set(&member.FlashRankCacheDir, "FLASHRANK_CACHE_DIR")

	member.MaxTokensPerCandidate = intFromEnv(lookup, base+"MAX_TOKENS_PER_CANDIDATE", member.MaxTokensPerCandidate)
	member.LiteLLMMaxTokensPerDoc = intFromEnv(lookup, base+"LITELLM_MAX_TOKENS_PER_DOC", member.LiteLLMMaxTokensPerDoc)
	member.TEIBatchSize = intFromEnv(lookup, base+"TEI_BATCH_SIZE", member.TEIBatchSize)
	member.TEIMaxConcurrent = intFromEnv(lookup, base+"TEI_MAX_CONCURRENT", member.TEIMaxConcurrent)
	member.TypeSafeMaxConcurrent = intFromEnv(lookup, base+"TYPESAFE_MAX_CONCURRENT", member.TypeSafeMaxConcurrent)
	member.LocalMaxConcurrent = intFromEnv(lookup, base+"LOCAL_MAX_CONCURRENT", member.LocalMaxConcurrent)
	member.LocalBatchSize = intFromEnv(lookup, base+"LOCAL_BATCH_SIZE", member.LocalBatchSize)
	member.FlashRankBatchSize = intFromEnv(lookup, base+"FLASHRANK_BATCH_SIZE", member.FlashRankBatchSize)

	member.TEIHTTPTimeout = durationFromEnv(lookup, base+"TEI_HTTP_TIMEOUT", member.TEIHTTPTimeout)
	member.CohereTimeout = durationFromEnv(lookup, base+"COHERE_TIMEOUT", member.CohereTimeout)
	member.OpenRouterTimeout = durationFromEnv(lookup, base+"OPENROUTER_TIMEOUT", member.OpenRouterTimeout)
	member.ZeroEntropyTimeout = durationFromEnv(lookup, base+"ZEROENTROPY_TIMEOUT", member.ZeroEntropyTimeout)
	member.SiliconflowTimeout = durationFromEnv(lookup, base+"SILICONFLOW_TIMEOUT", member.SiliconflowTimeout)
	member.TypeSafeTimeout = durationFromEnv(lookup, base+"TYPESAFE_TIMEOUT", member.TypeSafeTimeout)
	member.GoogleTimeout = durationFromEnv(lookup, base+"GOOGLE_TIMEOUT", member.GoogleTimeout)
	member.AlibabaTimeout = durationFromEnv(lookup, base+"ALIBABA_TIMEOUT", member.AlibabaTimeout)
	member.LiteLLMTimeout = durationFromEnv(lookup, base+"LITELLM_TIMEOUT", member.LiteLLMTimeout)
	member.LiteLLMSDKTimeout = durationFromEnv(lookup, base+"LITELLM_SDK_TIMEOUT", member.LiteLLMSDKTimeout)
	member.LocalTimeout = durationFromEnv(lookup, base+"LOCAL_TIMEOUT", member.LocalTimeout)

	member.LocalForceCPU = boolValue(lookup, base+"LOCAL_FORCE_CPU", member.LocalForceCPU)
	member.LocalTrustRemoteCode = boolValue(lookup, base+"LOCAL_TRUST_REMOTE_CODE", member.LocalTrustRemoteCode)
	member.LocalFP16 = boolValue(lookup, base+"LOCAL_FP16", member.LocalFP16)
	member.LocalBucketBatching = boolValue(lookup, base+"LOCAL_BUCKET_BATCHING", member.LocalBucketBatching)
	member.TypeSafePruneCandidates = boolValue(lookup, base+"TYPESAFE_PRUNE_CANDIDATES", member.TypeSafePruneCandidates)
	member.FlashRankCPUMemArena = boolValue(lookup, base+"FLASHRANK_CPU_MEM_ARENA", member.FlashRankCPUMemArena)

	return member
}

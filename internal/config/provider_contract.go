package config

import "strings"

// Provider-family classification.
//
// An operator's environment file must be accepted key by key: a copied
// deployment file either changes resolved configuration this build acts on, or
// the process refuses to start and names the key it cannot honour. Nothing in
// these families is silently ignored.
//
// The key names themselves are generated from the contract document into
// provider_env_keys.go by scripts/gen_provider_env_keys.py; this file only
// classifies them.

// providerFamilyOrder fixes the order families are inspected and reported in,
// so a refusal message is stable across runs.
var providerFamilyOrder = []string{
	"llm",
	"retain_llm",
	"reflect_llm",
	"consolidation_llm",
	"mental_model_refresh_llm",
	"embeddings",
	"reranker",
	"litellm",
}

// ProviderFamilyOrder returns the family names in reporting order.
func ProviderFamilyOrder() []string { return append([]string(nil), providerFamilyOrder...) }

// ProviderFamilyKeys returns the enumerated keys of every provider family.
// Resolution and refusal are both checked against this list, so a key added to
// the contract without a decision shows up as a failure.
func ProviderFamilyKeys() map[string][]string {
	out := make(map[string][]string, len(providerFamilyKeys))
	for family, keys := range providerFamilyKeys {
		out[family] = append([]string(nil), keys...)
	}
	return out
}

// resolvedProviderKeys are the family keys this build reads into resolved
// configuration that changes behaviour. Everything else in the families is
// accepted only to be refused by name when it is set.
var resolvedProviderKeys = map[string]bool{
	// The startup model verification is read into the resolved configuration, so
	// the switch that turns it off is honoured rather than refused.
	EnvSkipLLMVerification: true,
	// Chat scope.
	EnvLLMProvider:             true,
	EnvLLMAPIKey:               true,
	EnvLLMBaseURL:              true,
	EnvLLMModel:                true,
	EnvLLMCacheAffinity:        true,
	EnvLLMConnectTimeout:       true,
	EnvLLMDebugDump4XX:         true,
	EnvLLMDefaultHeaders:       true,
	EnvLLMExtraBody:            true,
	EnvLLMHTTPLogLevel:         true,
	EnvLLMInitialBackoff:       true,
	EnvLLMMaxBackoff:           true,
	EnvLLMMaxConcurrent:        true,
	EnvLLMMaxRetries:           true,
	EnvLLMOpenAIJSONMode:       true,
	EnvLLMOpenAIServiceTier:    true,
	EnvLLMOutputLanguage:       true,
	EnvLLMPromptCacheEnabled:   true,
	EnvLLMReasoningEffort:      true,
	EnvLLMSendBankAsUser:       true,
	EnvLLMForcedTool:           true,
	EnvLLMSupportsMaxItems:     true,
	EnvLLMSupportsStringPattrn: true,
	EnvLLMTimeout:              true,
	EnvLLMTraceEnabled:         true,
	EnvLLMTraceMaxChars:        true,
	EnvLLMTraceRetentionDays:   true,
	EnvLLMTraceScopes:          true,
	EnvLLMVision:               true,
	// Multi-LLM routing: the strategy document is parsed and validated, and
	// indexed members resolve through HINDSIGHT_API_LLM_{n}_*.
	EnvLLMStrategy: true,

	// Structured-output strictness and temperature, per operation and global.
	EnvLLMStrictSchema:              true,
	EnvLLMStrictSchemaRetain:        true,
	EnvLLMStrictSchemaReflect:       true,
	EnvLLMStrictSchemaConsolidation: true,
	EnvLLMTemperature:               true,
	EnvLLMTemperatureVerification:   true,
	EnvLLMTemperatureRetain:         true,
	EnvLLMTemperatureReflect:        true,
	EnvLLMTemperatureConsolidation:  true,

	// Embeddings: the global family, the OpenAI-compatible adapter and every
	// vendor family whose keys the resolver reads into EmbeddingConfig. The
	// ONNX and in-process local backends are refused by name because they need
	// a native runtime this build does not ship.
	EnvEmbeddingsProvider:                   true,
	EnvEmbeddingsQueryPrefix:                true,
	EnvEmbeddingsPassagePrefix:              true,
	EnvEmbeddingsMaxConcurrent:              true,
	EnvEmbeddingsMaxRetries:                 true,
	EnvEmbeddingsInitialBackoff:             true,
	EnvEmbeddingsMaxBackoff:                 true,
	EnvEmbeddingsRetryBudget:                true,
	EnvEmbeddingsMaxInputTokens:             true,
	EnvEmbeddingsOpenAIKey:                  true,
	EnvEmbeddingsOpenAIModel:                true,
	EnvEmbeddingsOpenAIBaseURL:              true,
	EnvEmbeddingsOpenAIBatchSize:            true,
	EnvEmbeddingsOpenAIDims:                 true,
	EnvEmbeddingDimension:                   true,
	EnvEmbeddingsCohereAPIKey:               true,
	EnvEmbeddingsCohereModel:                true,
	EnvEmbeddingsCohereBaseURL:              true,
	EnvEmbeddingsCohereOutputDimensions:     true,
	EnvEmbeddingsTeiURL:                     true,
	EnvEmbeddingsTeiBatchSize:               true,
	EnvEmbeddingsGeminiAPIKey:               true,
	EnvEmbeddingsGeminiModel:                true,
	EnvEmbeddingsGeminiOutputDimensionality: true,
	EnvEmbeddingsGeminiForceIPv4:            true,
	EnvEmbeddingsGeminiBatchSize:            true,
	EnvEmbeddingsVertexaiProjectID:          true,
	EnvEmbeddingsVertexaiRegion:             true,
	EnvEmbeddingsVertexaiServiceAccountKey:  true,
	EnvEmbeddingsZeroentropyAPIKey:          true,
	EnvEmbeddingsZeroentropyModel:           true,
	EnvEmbeddingsZeroentropyBaseURL:         true,
	EnvEmbeddingsZeroentropyDimensions:      true,
	EnvEmbeddingsZeroentropyEncodingFormat:  true,
	EnvEmbeddingsZeroentropyLatency:         true,
	EnvEmbeddingsZeroentropyBatchSize:       true,
	EnvEmbeddingsOpenrouterAPIKey:           true,
	EnvEmbeddingsOpenrouterModel:            true,
	EnvEmbeddingsRequestyAPIKey:             true,
	EnvEmbeddingsRequestyModel:              true,
	EnvEmbeddingsLitellmAPIBase:             true,
	EnvEmbeddingsLitellmAPIKey:              true,
	EnvEmbeddingsLitellmModel:               true,
	EnvEmbeddingsLitellmDimensions:          true,
	EnvEmbeddingsLitellmSDKAPIKey:           true,
	EnvEmbeddingsLitellmSDKModel:            true,
	EnvEmbeddingsLitellmSDKModelID:          true,
	EnvEmbeddingsLitellmSDKAPIBase:          true,
	EnvEmbeddingsLitellmSDKOutputDimensions: true,
	EnvEmbeddingsLitellmSDKEncodingFormat:   true,
	EnvEmbeddingsLitellmSDKMaxInputTokens:   true,

	// Reranker: the resolver reads every key of the family into the reranker
	// chain, including the members whose adapter this build refuses at
	// composition time, so the keys are resolved rather than refused here.
	EnvRerankerMaxRetries:              true,
	EnvRerankerInitialBackoff:          true,
	EnvRerankerMaxBackoff:              true,
	EnvRerankerRetryBudget:             true,
	EnvRerankerCohereAPIKey:            true,
	EnvRerankerCohereModel:             true,
	EnvRerankerCohereBaseURL:           true,
	EnvRerankerOpenrouterAPIKey:        true,
	EnvRerankerOpenrouterModel:         true,
	EnvRerankerOpenrouterBaseURL:       true,
	EnvRerankerLitellmAPIBase:          true,
	EnvRerankerLitellmAPIKey:           true,
	EnvRerankerLitellmModel:            true,
	EnvRerankerLitellmMaxTokensPerDoc:  true,
	EnvRerankerMaxTokensPerCandidate:   true,
	EnvRerankerLitellmSDKAPIKey:        true,
	EnvRerankerLitellmSDKModel:         true,
	EnvRerankerLitellmSDKAPIBase:       true,
	EnvRerankerProvider:                true,
	EnvRerankerSendBankAsHeader:        true,
	EnvRerankerLocalModel:              true,
	EnvRerankerLocalForceCPU:           true,
	EnvRerankerLocalMaxConcurrent:      true,
	EnvRerankerLocalTrustRemoteCode:    true,
	EnvRerankerLocalFP16:               true,
	EnvRerankerLocalBucketBatching:     true,
	EnvRerankerLocalBatchSize:          true,
	EnvRerankerLocalTimeout:            true,
	EnvRerankerTeiURL:                  true,
	EnvRerankerTeiBatchSize:            true,
	EnvRerankerTeiMaxConcurrent:        true,
	EnvRerankerTeiHttpTimeout:          true,
	EnvRerankerCohereTimeout:           true,
	EnvRerankerOpenrouterTimeout:       true,
	EnvRerankerZeroentropyTimeout:      true,
	EnvRerankerSiliconflowTimeout:      true,
	EnvRerankerAlibabaTimeout:          true,
	EnvRerankerLitellmTimeout:          true,
	EnvRerankerLitellmSDKTimeout:       true,
	EnvRerankerGoogleTimeout:           true,
	EnvRerankerMaxCandidates:           true,
	EnvRerankerMaxCandidatesLow:        true,
	EnvRerankerMaxCandidatesMid:        true,
	EnvRerankerMaxCandidatesHigh:       true,
	EnvRerankerFlashrankModel:          true,
	EnvRerankerFlashrankCacheDir:       true,
	EnvRerankerFlashrankCPUMemArena:    true,
	EnvRerankerFlashrankBatchSize:      true,
	EnvRerankerZeroentropyAPIKey:       true,
	EnvRerankerZeroentropyModel:        true,
	EnvRerankerZeroentropyBaseURL:      true,
	EnvRerankerSiliconflowAPIKey:       true,
	EnvRerankerSiliconflowModel:        true,
	EnvRerankerSiliconflowBaseURL:      true,
	EnvRerankerTypesafeAPIKey:          true,
	EnvRerankerTypesafeModel:           true,
	EnvRerankerTypesafeBaseURL:         true,
	EnvRerankerTypesafeTimeout:         true,
	EnvRerankerTypesafeMaxConcurrent:   true,
	EnvRerankerTypesafePruneCandidates: true,
	EnvRerankerAlibabaAPIKey:           true,
	EnvRerankerAlibabaModel:            true,
	EnvRerankerGoogleModel:             true,
	EnvRerankerGoogleProjectID:         true,
	EnvRerankerGoogleServiceAccountKey: true,
}

// scopedLLMResolvedSuffixes are the per-role keys that resolve. Provider,
// strategy and the shared chat tuning keys are all read into the role's scope;
// only the litellm router deployment is refused by name.
var scopedLLMResolvedSuffixes = []string{
	"PROVIDER", "API_KEY", "MODEL", "BASE_URL", "MAX_CONCURRENT", "MAX_RETRIES",
	"INITIAL_BACKOFF", "MAX_BACKOFF", "TIMEOUT", "REASONING_EFFORT",
	"EXTRA_BODY", "CACHE_AFFINITY", "STRATEGY",
}

// scopedLLMPrefixes are the per-role chat scopes, keyed by family name.
var scopedLLMPrefixes = map[string]string{
	"retain_llm":               "HINDSIGHT_API_RETAIN_LLM",
	"reflect_llm":              "HINDSIGHT_API_REFLECT_LLM",
	"consolidation_llm":        "HINDSIGHT_API_CONSOLIDATION_LLM",
	"mental_model_refresh_llm": "HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM",
}

// consolidationOnlySuffixes are the two keys only the consolidation role
// carries in the contract.
var consolidationOnlySuffixes = []string{"BATCH_SIZE", "PARALLELISM"}

// resolvedProviderKey reports whether this build reads the key into resolved
// configuration.
func resolvedProviderKey(key string) bool {
	if resolvedProviderKeys[key] {
		return true
	}
	for family, prefix := range scopedLLMPrefixes {
		if !strings.HasPrefix(key, prefix+"_") {
			continue
		}
		suffix := strings.TrimPrefix(key, prefix+"_")
		for _, allowed := range scopedLLMResolvedSuffixes {
			if suffix == allowed {
				return true
			}
		}
		if family == "consolidation_llm" {
			for _, allowed := range consolidationOnlySuffixes {
				if suffix == allowed {
					return true
				}
			}
		}
	}
	return false
}

// falseDefaultProviderKeys are the boolean keys whose built-in value is false.
// For those, only a truthy value is a configuration the operator actually
// asked for, so an explicit "false" copied from a deployment file is not
// treated as a refusal.
var falseDefaultProviderKeys = map[string]bool{
	EnvSkipLLMVerification:             true,
	EnvEmbeddingsLocalForceCPU:         true,
	EnvEmbeddingsLocalTrustCode:        true,
	EnvEmbeddingsOnnxCPUMemArena:       true,
	EnvRerankerSendBankAsHeader:        true,
	EnvRerankerLocalForceCPU:           true,
	EnvRerankerLocalTrustRemoteCode:    true,
	EnvRerankerLocalFP16:               true,
	EnvRerankerLocalBucketBatching:     true,
	EnvRerankerTypesafePruneCandidates: true,
	EnvRerankerFlashrankCPUMemArena:    true,
	EnvEmbeddingsGeminiForceIPv4:       true,
}

// providerKeyConfigured reports whether a set value is a configuration the
// operator asked for. For a false-default boolean, an explicit false is the
// built-in value and is not a request to enable anything.
func providerKeyConfigured(key, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if falseDefaultProviderKeys[key] {
		enabled, ok := parseBoolValue(value)
		return !ok || enabled
	}
	return true
}

// UnsupportedProviderKeys returns every configured provider-family key this
// build cannot act on, in family order. It is the single source of the
// startup refusal, so an operator who configured a backend this build does not
// ship is told which key is inert instead of running with a silent no-op.
func (c Config) UnsupportedProviderKeys() []string {
	var out []string
	seen := map[string]bool{}
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}
	for _, family := range providerFamilyOrder {
		for _, key := range providerFamilyKeys[family] {
			if resolvedProviderKey(key) {
				continue
			}
			if providerKeyConfigured(key, c.rawEnv[key]) {
				add(key)
			}
		}
	}
	// A refusal can also come from a resolved field the caller set directly
	// (a flag, a test, or a wired-down default) rather than through the
	// environment.
	if len(c.Provider.LiteLLMRouterConfig) > 0 {
		add(EnvLLMLiteLLMRouterConfig)
	}
	if strings.TrimSpace(c.Provider.Embeddings.LocalModel) != "" {
		add(EnvEmbeddingsLocalModel)
	}
	if c.Provider.Embeddings.LocalForceCPU {
		// Local embeddings need the local backend, which is not shipped.
		add(EnvEmbeddingsLocalForceCPU)
	}
	if c.Provider.Embeddings.LocalTrustRemoteCode {
		add(EnvEmbeddingsLocalTrustCode)
	}
	if strings.TrimSpace(c.Provider.Embeddings.Provider) != "" &&
		!supportEmbeddingProvider(c.Provider.Embeddings.Provider) {
		add(EnvEmbeddingsProvider)
	}
	return out
}

// ProviderRefusalReason is the short operator-facing note explaining what a
// refused key asked for.
func ProviderRefusalReason(key string) string {
	for _, rule := range providerRefusalReasons {
		if strings.HasPrefix(key, rule.prefix) {
			return rule.reason
		}
	}
	return "this build does not implement this setting"
}

// providerRefusalReasons maps a key prefix to the note reported with it.
var providerRefusalReasons = []struct {
	prefix string
	reason string
}{
	{"HINDSIGHT_API_EMBEDDINGS_ONNX", "the ONNX embedding backend needs a native runtime this build does not ship"},
	{"HINDSIGHT_API_EMBEDDINGS_LOCAL", "the in-process local embedding backend needs a native runtime this build does not ship"},
	{"HINDSIGHT_API_LITELLM", "the litellm provider is not implemented"},
	{"HINDSIGHT_API_LLM_GROQ_SERVICE_TIER", "the Groq service tier is not implemented"},
	{"HINDSIGHT_API_LLM_BEDROCK_SERVICE_TIER", "the Bedrock service tier is not implemented"},
	{"HINDSIGHT_API_LLM_GEMINI_SERVICE_TIER", "the Gemini service tier is not implemented"},
	{"HINDSIGHT_API_LLM_GROQ", "the Groq service tier is not implemented"},
	{"HINDSIGHT_API_LLM_OLLAMA", "the Ollama request tuning is not implemented"},
	{"HINDSIGHT_API_LLM_CODEX_HOME", "the Codex CLI provider is not implemented"},
	{"HINDSIGHT_API_LLM_VERTEXAI", "the Vertex AI provider is not implemented"},
	{"HINDSIGHT_API_LLM_GEMINI_SAFETY_SETTINGS", "the Gemini safety-settings override is not implemented"},
	{"HINDSIGHT_API_LLM_LITELLMROUTER_CONFIG", "the litellm router deployment is not implemented"},
	{"HINDSIGHT_API_RETAIN_LLM_LITELLMROUTER_CONFIG", "the litellm router deployment is not implemented"},
	{"HINDSIGHT_API_REFLECT_LLM_LITELLMROUTER_CONFIG", "the litellm router deployment is not implemented"},
	{"HINDSIGHT_API_CONSOLIDATION_LLM_LITELLMROUTER_CONFIG", "the litellm router deployment is not implemented"},
	{"HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM_LITELLMROUTER_CONFIG", "the litellm router deployment is not implemented"},
}

// recordUnsupportedProviderKeys folds the family refusals into the shared
// unsupported-key set so a single startup validation reports every inert
// setting, provider or not. Keys of a family this build does not implement are
// never read by a resolver, so their values are read here, once, and kept in
// rawEnv for reporting.
func (c *Config) recordUnsupportedProviderKeys(lookup func(string) (string, bool)) {
	if c.rawEnv == nil {
		c.rawEnv = map[string]string{}
	}
	for _, family := range providerFamilyOrder {
		for _, key := range providerFamilyKeys[family] {
			if _, recorded := c.rawEnv[key]; recorded {
				continue
			}
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				c.rawEnv[key] = value
			}
		}
	}
	keys := c.UnsupportedProviderKeys()
	if len(keys) == 0 {
		return
	}
	if c.Unsupported == nil {
		c.Unsupported = &Unsupported{}
	}
	for _, key := range keys {
		c.Unsupported.Add(key, ProviderRefusalReason(key))
	}
}

// ValidateSupported reports every configured setting this build cannot honour:
// the runtime keys recorded during resolution plus the provider-family keys,
// including a resolved provider field a caller set directly rather than
// through the environment. It is the single check the composition root runs,
// so there is exactly one refusal path.
func (c Config) ValidateSupported() error {
	unsupported := &Unsupported{}
	if c.Unsupported != nil {
		unsupported.Keys = append(unsupported.Keys, c.Unsupported.Keys...)
	}
	for _, key := range c.UnsupportedProviderKeys() {
		unsupported.Add(key, ProviderRefusalReason(key))
	}
	return unsupported.Err()
}

// supportEmbeddingProvider reports whether an embedding provider name has a
// backend in this build. The ONNX and in-process local backends and the
// litellm SDK and OpenAI Codex providers are rejected by name because they need
// a runtime this build does not ship.
func supportEmbeddingProvider(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai", "openrouter", "requesty", "litellm", "cohere", "tei",
		"zeroentropy", "gemini", "google", "none", "mock":
		return true
	default:
		return false
	}
}

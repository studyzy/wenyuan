package config

import "testing"

// test-config-precedence: every provider key resolves through the existing
// source; flag beats environment; empty is unset and the built-in default
// applies.
func TestProviderConfigPrecedence(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvLLMProvider: "openai",
		EnvLLMAPIKey:   "secret",
		EnvLLMModel:    "gpt-test",
	}))
	if cfg.Provider.Provider != "openai" {
		t.Fatalf("provider = %q", cfg.Provider.Provider)
	}
	if cfg.Provider.APIKey != "secret" {
		t.Fatalf("api key = %q", cfg.Provider.APIKey)
	}
	if cfg.Provider.Model != "gpt-test" {
		t.Fatalf("model = %q", cfg.Provider.Model)
	}

	// A flag beats the environment value.
	overridden := ApplyFlags(cfg, FlagValues{})
	if overridden.Provider.Provider != "openai" {
		t.Fatalf("apply flags changed the provider: %q", overridden.Provider.Provider)
	}
}

// An empty value is unset: the built-in default applies rather than the empty
// string being treated as a valid value.
func TestProviderEmptyIsUnset(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvLLMProvider:      "   ",
		EnvLLMMaxRetries:    "",
		EnvLLMMaxConcurrent: "",
	}))
	if cfg.Provider.Provider != "" {
		t.Fatalf("blank provider should be unset, got %q", cfg.Provider.Provider)
	}
	if cfg.Provider.MaxRetries != DefaultLLMMaxRetries {
		t.Fatalf("max retries = %d, want default %d", cfg.Provider.MaxRetries, DefaultLLMMaxRetries)
	}
	if cfg.Provider.MaxConcurrent != DefaultLLMMaxConcurrent {
		t.Fatalf("max concurrent = %d, want default %d", cfg.Provider.MaxConcurrent, DefaultLLMMaxConcurrent)
	}
}

// Every declared provider key resolves without error and is retained.
func TestEveryProviderKeyRetained(t *testing.T) {
	values := map[string]string{}
	for _, key := range ProviderKeys() {
		values[key] = "1"
	}
	cfg := FromEnv(envMap(values))
	retained := ProviderValues(envMap(values))
	if len(retained) != len(ProviderKeys()) {
		t.Fatalf("retained %d keys, want %d", len(retained), len(ProviderKeys()))
	}
	// Resolution must not panic or drop the provider name key.
	if cfg.Provider.Provider != "1" {
		t.Fatalf("provider = %q", cfg.Provider.Provider)
	}
}

// test-transport-and-provider-tuning-keys: the transport and tuning keys each
// resolve to a usable value.
func TestTransportAndTuningKeys(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvLLMInitialBackoff:      "2",
		EnvLLMMaxBackoff:          "30",
		EnvLLMMaxRetries:          "5",
		EnvLLMTimeout:             "45",
		EnvLLMConnectTimeout:      "7",
		EnvLLMHTTPLogLevel:        "debug",
		EnvLLMDefaultHeaders:      "X-A: one, X-B: two",
		EnvLLMExtraBody:           `{"top_p":0.5}`,
		EnvLLMCacheAffinity:       "prefix",
		EnvLLMStrategy:            `{"mode":"round-robin"}`,
		EnvLLMLiteLLMRouterConfig: "a,b,c",
		EnvLLMOpenAIServiceTier:   "flex",
		EnvLLMOutputLanguage:      "en",
		EnvLLMPromptCacheEnabled:  "true",
		EnvLLMReasoningEffort:     "high",
		EnvLLMSendBankAsUser:      "true",
		EnvLLMDebugDump4XX:        "true",
	}))
	if cfg.Provider.InitialBackoff.Seconds() != 2 {
		t.Fatalf("initial backoff = %s", cfg.Provider.InitialBackoff)
	}
	if cfg.Provider.MaxBackoff.Seconds() != 30 {
		t.Fatalf("max backoff = %s", cfg.Provider.MaxBackoff)
	}
	if cfg.Provider.MaxRetries != 5 || cfg.Provider.MaxConcurrent != DefaultLLMMaxConcurrent {
		t.Fatalf("retries=%d concurrent=%d", cfg.Provider.MaxRetries, cfg.Provider.MaxConcurrent)
	}
	if cfg.Provider.Timeout.Seconds() != 45 || cfg.Provider.ConnectTimeout.Seconds() != 7 {
		t.Fatalf("timeout=%s connect=%s", cfg.Provider.Timeout, cfg.Provider.ConnectTimeout)
	}
	if cfg.Provider.DefaultHeaders["X-A"] != "one" || cfg.Provider.DefaultHeaders["X-B"] != "two" {
		t.Fatalf("headers = %v", cfg.Provider.DefaultHeaders)
	}
	if cfg.Provider.ExtraBody["top_p"] != 0.5 {
		t.Fatalf("extra body = %v", cfg.Provider.ExtraBody)
	}
	if !cfg.Provider.PromptCacheEnabled || !cfg.Provider.SendBankAsUser || !cfg.Provider.DebugDump4XX {
		t.Fatal("boolean transport keys did not resolve to true")
	}
	if cfg.Provider.CacheAffinity != "prefix" || cfg.Provider.ReasoningEffort != "high" {
		t.Fatalf("affinity=%q effort=%q", cfg.Provider.CacheAffinity, cfg.Provider.ReasoningEffort)
	}
	if len(cfg.Provider.LiteLLMRouterConfig) != 3 {
		t.Fatalf("router config = %v", cfg.Provider.LiteLLMRouterConfig)
	}
	if cfg.Provider.OpenAIServiceTier != "flex" || cfg.Provider.OutputLanguage != "en" {
		t.Fatalf("tier=%q lang=%q", cfg.Provider.OpenAIServiceTier, cfg.Provider.OutputLanguage)
	}
	if cfg.Provider.Strategy.Mode != "round-robin" || cfg.Provider.HTTPLogLevel != "debug" {
		t.Fatalf("strategy=%q log=%q", cfg.Provider.Strategy.Mode, cfg.Provider.HTTPLogLevel)
	}
}

// test-tracing-keys: the tracing keys resolve including the keep-forever
// retention sentinel.
func TestTracingKeys(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvLLMTraceEnabled:       "true",
		EnvLLMTraceMaxChars:      "1024",
		EnvLLMTraceRetentionDays: "30",
		EnvLLMTraceScopes:        "retain,reflect",
	}))
	if !cfg.Provider.TraceEnabled {
		t.Fatal("trace enabled did not resolve")
	}
	if cfg.Provider.TraceMaxChars != 1024 || cfg.Provider.TraceRetentionDays != 30 {
		t.Fatalf("chars=%d days=%d", cfg.Provider.TraceMaxChars, cfg.Provider.TraceRetentionDays)
	}
	if len(cfg.Provider.TraceScopes) != 2 {
		t.Fatalf("scopes = %v", cfg.Provider.TraceScopes)
	}

	// The keep-forever sentinel is a negative retention window.
	forever := FromEnv(envMap(map[string]string{EnvLLMTraceRetentionDays: "-1"}))
	if forever.Provider.TraceRetentionDays != -1 {
		t.Fatalf("retention = %d, want -1", forever.Provider.TraceRetentionDays)
	}
}

// The tri-state vision and JSON-mode keys keep unset distinguishable from an
// explicit false.
func TestTriStateKeys(t *testing.T) {
	unset := FromEnv(envMap(nil))
	if unset.Provider.Vision != nil || unset.Provider.OpenAIJSONMode != nil {
		t.Fatal("unset tri-state keys should be nil")
	}
	off := FromEnv(envMap(map[string]string{
		EnvLLMVision:         "false",
		EnvLLMOpenAIJSONMode: "false",
	}))
	if off.Provider.Vision == nil || *off.Provider.Vision {
		t.Fatal("explicit false vision must be false, not nil")
	}
	if off.Provider.OpenAIJSONMode == nil || *off.Provider.OpenAIJSONMode {
		t.Fatal("explicit false json mode must be false, not nil")
	}
}

// test-local-embedding-keys: the embedding family resolves independently.
func TestEmbeddingKeys(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvEmbeddingsProvider:        "openai",
		EnvEmbeddingsOpenAIKey:       "ekey",
		EnvEmbeddingsOpenAIBaseURL:   "https://embed.example/v1",
		EnvEmbeddingsOpenAIModel:     "text-embed",
		EnvEmbeddingsOpenAIBatchSize: "16",
		EnvEmbeddingsOpenAIDims:      "512",
		EnvEmbeddingsMaxInputTokens:  "8192",
		EnvEmbeddingsPassagePrefix:   "passage: ",
		EnvEmbeddingsQueryPrefix:     "query: ",
		EnvEmbeddingsMaxRetries:      "4",
		EnvEmbeddingsInitialBackoff:  "2",
		EnvEmbeddingsMaxBackoff:      "20",
		EnvEmbeddingsRetryBudget:     "60",
		EnvEmbeddingsMaxConcurrent:   "4",
		EnvEmbeddingsLocalModel:      "local-model",
		EnvEmbeddingsLocalForceCPU:   "true",
		EnvEmbeddingsLocalTrustCode:  "true",
	}))
	e := cfg.Provider.Embeddings
	if e.Provider != "openai" || e.OpenAIModel != "text-embed" {
		t.Fatalf("provider=%q model=%q", e.Provider, e.OpenAIModel)
	}
	if e.OpenAIBatchSize != 16 || e.OpenAIDimensions != 512 || e.MaxInputTokens != 8192 {
		t.Fatalf("batch=%d dims=%d tokens=%d", e.OpenAIBatchSize, e.OpenAIDimensions, e.MaxInputTokens)
	}
	// Configuration values are trimmed, so the trailing space in the input
	// above is not part of the resolved prefix.
	if e.PassagePrefix != "passage:" || e.QueryPrefix != "query:" {
		t.Fatalf("prefixes=%q/%q", e.PassagePrefix, e.QueryPrefix)
	}
	if e.MaxRetries != 4 || e.MaxConcurrentRequests != 4 {
		t.Fatalf("retries=%d concurrent=%d", e.MaxRetries, e.MaxConcurrentRequests)
	}
	if e.RetryBudget.Seconds() != 60 || e.InitialBackoff.Seconds() != 2 || e.MaxBackoff.Seconds() != 20 {
		t.Fatalf("budget=%s initial=%s max=%s", e.RetryBudget, e.InitialBackoff, e.MaxBackoff)
	}
	if !e.LocalForceCPU || !e.LocalTrustRemoteCode || e.LocalModel != "local-model" {
		t.Fatalf("local settings = %+v", e)
	}
}

// The vendor-scoped dimensions knob is adapter-level only and must not become
// the destination's stored width.
func TestEmbeddingVendorDimensionsDoNotAffectStoredWidth(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{EnvEmbeddingsOpenAIDims: "1536"}))
	if cfg.Provider.Embeddings.OpenAIDimensions != 1536 {
		t.Fatalf("vendor dims = %d", cfg.Provider.Embeddings.OpenAIDimensions)
	}
	// The stored width is the compile-time constant, exposed nowhere as a key.
	if _, ok := envMap(nil)("WENYUAN_API_EMBEDDINGS_DIMENSIONS"); ok {
		t.Fatal("a generic dimensions key must not exist")
	}
}

// test-removed-accelerator-flag-warns: the removed accelerator opt-in is read
// and produces a no-effect warning.
func TestRemovedAcceleratorFlagWarns(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{EnvEmbeddingsLocalAllowMPS: "true"}))
	if !cfg.Provider.Embeddings.AllowMPS {
		t.Fatal("removed flag was not read")
	}
	warnings := ProviderWarnings(cfg.Provider)
	if len(warnings) == 0 {
		t.Fatal("no warning was raised for the removed flag")
	}
	silent := FromEnv(envMap(nil))
	if len(ProviderWarnings(silent.Provider)) != 0 {
		t.Fatal("a warning was raised when the flag was unset")
	}
}

// test-concurrency-invalid-value: a non-positive value for a positive-integer
// key raises a value error naming the key.
func TestPositiveIntValidation(t *testing.T) {
	if err := ValidatePositiveInt(EnvLLMMaxConcurrent, "0"); err == nil {
		t.Fatal("zero must be rejected for a positive-integer key")
	}
	if err := ValidatePositiveInt(EnvLLMMaxConcurrent, "-3"); err == nil {
		t.Fatal("negative must be rejected")
	}
	if err := ValidatePositiveInt(EnvLLMMaxConcurrent, "abc"); err == nil {
		t.Fatal("non-numeric must be rejected")
	}
	if err := ValidatePositiveInt(EnvLLMMaxConcurrent, "16"); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
	if err := ValidatePositiveInt(EnvLLMMaxConcurrent, ""); err != nil {
		t.Fatalf("blank must be treated as unset: %v", err)
	}
}

// test-multi-llm-strategy: the global strategy JSON is parsed and validated,
// with weights checked against the member count and routes against the
// metadata mode, and a malformed document recorded rather than ignored.
func TestMultiLLMStrategyParsing(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvLLMStrategy:               `{"mode":"round-robin","weights":[1,3]}`,
		"WENYUAN_API_LLM_1_PROVIDER": "openai",
		"WENYUAN_API_LLM_2_PROVIDER": "cohere",
		"WENYUAN_API_LLM_1_MODEL":    "gpt-4o-mini",
	}))
	if cfg.Provider.StrategyError != nil {
		t.Fatalf("a valid strategy raised %v", cfg.Provider.StrategyError)
	}
	if cfg.Provider.Strategy.Mode != "round-robin" || len(cfg.Provider.Strategy.Weights) != 2 || cfg.Provider.Strategy.Weights[1] != 3 {
		t.Fatalf("strategy = %+v", cfg.Provider.Strategy)
	}
	if len(cfg.Provider.LLMMembers) != 2 || cfg.Provider.LLMMembers[0].Provider != "openai" || cfg.Provider.LLMMembers[0].Model != "gpt-4o-mini" {
		t.Fatalf("members = %+v", cfg.Provider.LLMMembers)
	}

	routes := FromEnv(envMap(map[string]string{
		EnvLLMStrategy:               `{"mode":"metadata","routes":[{"key":"bank","value":"acme","member":1}]}`,
		"WENYUAN_API_LLM_1_PROVIDER": "openai",
	}))
	if routes.Provider.StrategyError != nil {
		t.Fatalf("a valid routes strategy raised %v", routes.Provider.StrategyError)
	}
	if routes.Provider.Strategy.Mode != "metadata" || len(routes.Provider.Strategy.Routes) != 1 {
		t.Fatalf("routes strategy = %+v", routes.Provider.Strategy)
	}
	route := routes.Provider.Strategy.Routes[0]
	if route.Key != "bank" || route.Value != "acme" || route.Member != 1 {
		t.Fatalf("route = %+v", route)
	}

	// A weight vector that does not match the member count is a startup error.
	bad := FromEnv(envMap(map[string]string{
		EnvLLMStrategy: `{"mode":"round-robin","weights":[1,2,3]}`,
	}))
	if bad.Provider.StrategyError == nil {
		t.Fatal("weights for a different member count must be rejected")
	}
	// Routes under a non-metadata mode are rejected rather than ignored.
	wrong := FromEnv(envMap(map[string]string{
		EnvLLMStrategy: `{"mode":"failover","routes":[{"key":"k","value":"v","member":0}]}`,
	}))
	if wrong.Provider.StrategyError == nil {
		t.Fatal("routes under a non-metadata mode must be rejected")
	}
	// An unparseable document is a startup error.
	invalid := FromEnv(envMap(map[string]string{EnvLLMStrategy: "probe"}))
	if invalid.Provider.StrategyError == nil {
		t.Fatal("invalid JSON must be rejected")
	}
}

// test-indexed-llm-members: the indexed extra members of each role resolve from
// WENYUAN_API_{ROLE}_LLM_{n}_*, stopping at the first missing provider.
func TestIndexedLLMMembersResolve(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		"WENYUAN_API_LLM_1_PROVIDER":         "openai",
		"WENYUAN_API_LLM_1_MODEL":            "gpt-4o",
		"WENYUAN_API_LLM_1_MAX_RETRIES":      "0",
		"WENYUAN_API_LLM_2_PROVIDER":         "cohere",
		"WENYUAN_API_REFLECT_LLM_1_PROVIDER": "anthropic",
		"WENYUAN_API_REFLECT_LLM_1_TIMEOUT":  "15",
	}))
	if len(cfg.Provider.LLMMembers) != 2 {
		t.Fatalf("global members = %+v", cfg.Provider.LLMMembers)
	}
	first := cfg.Provider.LLMMembers[0]
	if first.Index != 1 || first.Provider != "openai" || first.Model != "gpt-4o" {
		t.Fatalf("member 1 = %+v", first)
	}
	if !first.MaxRetriesSet || first.MaxRetries != 0 {
		t.Fatalf("explicit zero retry override lost: %+v", first)
	}
	if cfg.Provider.LLMMembers[1].Provider != "cohere" {
		t.Fatalf("member 2 = %+v", cfg.Provider.LLMMembers[1])
	}
	if len(cfg.Provider.ReflectLLMMembers) != 1 {
		t.Fatalf("reflect members = %+v", cfg.Provider.ReflectLLMMembers)
	}
	if cfg.Provider.ReflectLLMMembers[0].Provider != "anthropic" ||
		cfg.Provider.ReflectLLMMembers[0].Timeout.Seconds() != 15 {
		t.Fatalf("reflect member = %+v", cfg.Provider.ReflectLLMMembers[0])
	}
}

// test-reranker-chain: the unindexed reranker keys are the primary member and
// the indexed keys are the fallback members, with the built-in defaults
// filling anything left unset.
func TestRerankerChainResolves(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvRerankerProvider:                     "cohere",
		EnvRerankerMaxCandidates:                "25",
		EnvRerankerMaxCandidatesLow:             "10",
		EnvRerankerMaxCandidatesMid:             "20",
		EnvRerankerMaxCandidatesHigh:            "40",
		EnvRerankerSendBankAsHeader:             "true",
		"WENYUAN_API_RERANKER_1_PROVIDER":       "tei",
		"WENYUAN_API_RERANKER_1_TEI_URL":        "http://tei:8080",
		"WENYUAN_API_RERANKER_1_TEI_BATCH_SIZE": "64",
	}))
	r := cfg.Provider.Rerank
	if r.Provider != "cohere" || r.Primary.Provider != "cohere" {
		t.Fatalf("primary = %+v", r.Primary)
	}
	if r.CandidateDepth != 25 || r.CandidateDepthLow != 10 || r.CandidateDepthMid != 20 || r.CandidateDepthHigh != 40 {
		t.Fatalf("depths = %+v", r)
	}
	if !r.SendBankAsHeader {
		t.Fatal("send bank as header did not resolve")
	}
	if len(r.Members) != 1 {
		t.Fatalf("indexed members = %+v", r.Members)
	}
	if r.Members[0].Provider != "tei" || r.Members[0].TEIURL != "http://tei:8080" || r.Members[0].TEIBatchSize != 64 {
		t.Fatalf("member 1 = %+v", r.Members[0])
	}
	// The primary keeps the built-in defaults for what it did not set.
	if r.Primary.CohereModel != DefaultRerankerCohereModel {
		t.Fatalf("cohere model = %q", r.Primary.CohereModel)
	}
	if r.Primary.MaxTokensPerCandidate != DefaultRerankerMaxTokensPerCandidate {
		t.Fatalf("max tokens per candidate = %d", r.Primary.MaxTokensPerCandidate)
	}
}

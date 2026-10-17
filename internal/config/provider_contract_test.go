package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestProviderFamilyKeysAreDisjointAndWellFormed guards the generated contract
// list itself: a family member appears once, and every name is an environment
// key.
func TestProviderFamilyKeysAreDisjointAndWellFormed(t *testing.T) {
	seen := map[string]string{}
	total := 0
	for _, family := range ProviderFamilyOrder() {
		keys := ProviderFamilyKeys()[family]
		if len(keys) == 0 {
			t.Fatalf("family %q has no keys; the generated contract is empty", family)
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, "HINDSIGHT_API_") {
				t.Fatalf("family %q carries a non-contract name %q", family, key)
			}
			if other, dup := seen[key]; dup {
				t.Fatalf("key %q appears in both %q and %q", key, other, family)
			}
			seen[key] = family
			total++
		}
	}
	if total < 200 {
		t.Fatalf("only %d contract keys enumerated; the families look truncated", total)
	}
}

// TestProviderFamilyContract is the acceptance test for the provider families.
//
// Every key the contract document declares for the chat LLM scope, the four
// per-role LLM scopes, the embeddings families, the reranker families and the
// global litellm credentials is exercised. Each one must either move resolved
// configuration, or the build must refuse to start and name the key. A key that
// does neither is a silent no-op and fails here, so a newly added upstream key
// shows up as a failure instead of being ignored.
func TestProviderFamilyContract(t *testing.T) {
	baseline := normalizeConfig(FromEnv(envMap(nil)))

	for _, family := range ProviderFamilyOrder() {
		keys := ProviderFamilyKeys()[family]
		t.Run(family, func(t *testing.T) {
			for _, key := range keys {
				key := key
				t.Run(key, func(t *testing.T) {
					value := providerProbeValue(key)
					cfg := FromEnv(envMap(map[string]string{key: value}))

					refused := containsString(cfg.UnsupportedProviderKeys(), key)
					if resolvedProviderKey(key) {
						if refused {
							t.Fatalf("%s is classified as resolved but was refused", key)
						}
						if reflect.DeepEqual(normalizeConfig(cfg), baseline) {
							t.Fatalf("resolved key %s=%q did not change the resolved configuration", key, value)
						}
						return
					}
					if !refused {
						t.Fatalf("key %s=%q is neither resolved nor refused: it is silently ignored", key, value)
					}
					// The refusal must be reported through the shared
					// startup mechanism too, not only through this helper.
					if err := cfg.ValidateUnsupported(); err == nil ||
						!strings.Contains(err.Error(), key) {
						t.Fatalf("startup validation did not refuse %s: %v", key, err)
					}
					if ProviderRefusalReason(key) == "" {
						t.Fatalf("refused key %s has no operator-facing reason", key)
					}
				})
			}
		})
	}
}

// TestProviderProbeLeavesNoFamilyUnclassified fails when a generated key is
// classified in neither direction, which is the signal that the contract gained
// a key without a decision.
func TestProviderProbeLeavesNoFamilyUnclassified(t *testing.T) {
	for _, family := range ProviderFamilyOrder() {
		for _, key := range ProviderFamilyKeys()[family] {
			cfg := FromEnv(envMap(map[string]string{key: providerProbeValue(key)}))
			resolved := !reflect.DeepEqual(normalizeConfig(cfg), normalizeConfig(FromEnv(envMap(nil))))
			refused := containsString(cfg.UnsupportedProviderKeys(), key)
			if !resolved && !refused {
				t.Fatalf("%s/%s: no resolution and no refusal", family, key)
			}
		}
	}
}

// TestUnsupportedEmbeddingProviderNamesTheKey covers the selection key: an
// embedding backend this build does not ship is refused, and the refusal names
// the key rather than only the accepted values.
func TestUnsupportedEmbeddingProviderNamesTheKey(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{EnvEmbeddingsProvider: "onnx"}))
	if !containsString(cfg.UnsupportedProviderKeys(), EnvEmbeddingsProvider) {
		t.Fatalf("an unimplemented embedding provider was not refused")
	}
	err := cfg.ValidateUnsupported()
	if err == nil || !strings.Contains(err.Error(), EnvEmbeddingsProvider) {
		t.Fatalf("startup refusal did not name %s: %v", EnvEmbeddingsProvider, err)
	}

	supported := FromEnv(envMap(map[string]string{EnvEmbeddingsProvider: "openai"}))
	if containsString(supported.UnsupportedProviderKeys(), EnvEmbeddingsProvider) {
		t.Fatal("the implemented embedding provider was refused")
	}
}

// TestPerRoleScopesResolve proves the role scopes carry their own values,
// including the provider and strategy routing keys that now resolve, and that
// only the unimplemented router deployment is refused rather than dropped.
func TestPerRoleScopesResolve(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvRetainLLMModel:              "retain-model",
		EnvReflectLLMTimeout:           "9",
		EnvMentalModelRefreshLLMAPIKey: "mmr-key",
		EnvConsolidationBaseURL:        "https://consolidate.example/v1",
		EnvReflectLLMProvider:          "openai",
		EnvRetainLLMStrategy:           `{"mode":"failover"}`,
	}))
	if cfg.RetainLLM.Model == nil || *cfg.RetainLLM.Model != "retain-model" {
		t.Fatalf("retain model = %v", cfg.RetainLLM.Model)
	}
	if cfg.ReflectLLM.Timeout == nil || cfg.ReflectLLM.Timeout.Seconds() != 9 {
		t.Fatalf("reflect timeout = %v", cfg.ReflectLLM.Timeout)
	}
	if cfg.MentalModelRefreshLLM.APIKey == nil || *cfg.MentalModelRefreshLLM.APIKey != "mmr-key" {
		t.Fatalf("mental-model-refresh key = %v", cfg.MentalModelRefreshLLM.APIKey)
	}
	if cfg.Consolidation.BaseURL == nil || *cfg.Consolidation.BaseURL != "https://consolidate.example/v1" {
		t.Fatalf("consolidation base url = %v", cfg.Consolidation.BaseURL)
	}
	// The routing keys of a role now resolve instead of being refused.
	if cfg.ReflectLLM.Provider == nil || *cfg.ReflectLLM.Provider != "openai" {
		t.Fatalf("reflect provider = %v", cfg.ReflectLLM.Provider)
	}
	if cfg.Provider.RetainStrategy.Mode != "failover" {
		t.Fatalf("retain strategy = %+v", cfg.Provider.RetainStrategy)
	}
	if cfg.Provider.StrategyError != nil {
		t.Fatalf("a valid role strategy raised %v", cfg.Provider.StrategyError)
	}
	refused := cfg.UnsupportedProviderKeys()
	if containsString(refused, EnvReflectLLMProvider) || containsString(refused, EnvRetainLLMStrategy) {
		t.Fatalf("role routing keys were still refused: %v", refused)
	}

	// The role's router deployment is still refused by name.
	router := FromEnv(envMap(map[string]string{
		EnvConsolidationLiteLLMRouterConfig: "deployment-1",
	}))
	if !containsString(router.UnsupportedProviderKeys(), EnvConsolidationLiteLLMRouterConfig) {
		t.Fatalf("the role router deployment was not refused: %v", router.UnsupportedProviderKeys())
	}
}

// normalizeConfig zeroes the fields that differ between two resolutions of the
// same logical configuration (the recorded environment and the unsupported-key
// accumulator), so a comparison reflects configuration and not bookkeeping.
func normalizeConfig(c Config) Config {
	c.rawEnv = nil
	c.Unsupported = nil
	return c
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// providerProbeValue returns a value the family key's resolver acts on, chosen
// from the key name so the table needs no per-key literal for every family
// member.
func providerProbeValue(key string) string {
	if key == EnvEmbeddingsProvider {
		// The one embedding backend this build ships, so the selection key
		// resolves rather than being refused.
		return "openai"
	}
	if strings.HasSuffix(key, "EXTRA_BODY") {
		return `{"top_p":0.5}`
	}
	if key == EnvLLMDefaultHeaders {
		return "X-Probe: one"
	}
	if key == EnvLLMGeminiSafetySettings {
		// The safety-settings override is a JSON list; the probe must be a
		// literal the resolver acts on, not a bare token that is not JSON.
		return `[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_NONE"}]`
	}
	if strings.Contains(key, "TEMPERATURE") {
		return "0.5"
	}
	// A key whose built-in value is true is probed with false, so the probe
	// still proves the key is read.
	for _, trueDefault := range []string{EnvLLMSupportsMaxItems, EnvLLMTraceEnabled} {
		if key == trueDefault {
			return "false"
		}
	}
	for _, token := range []string{
		"ENABLED", "FORCE_CPU", "TRUST_REMOTE_CODE", "NORMALIZE", "BUCKET_BATCHING",
		"FP16", "PRUNE_CANDIDATES", "SEND_BANK_AS_HEADER", "SEND_BANK_AS_USER",
		"SUPPORTS_MAX_ITEMS", "SUPPORTS_STRING_PATTERN", "FORCED_TOOL", "VISION",
		"DEBUG_DUMP_4XX", "STRICT_SCHEMA", "JSON_MODE", "FORCE_IPV4", "MEM_ARENA",
		"SKIP",
	} {
		if strings.Contains(key, token) {
			return "true"
		}
	}
	for _, token := range []string{
		"MAX_CONCURRENT", "MAX_RETRIES", "BATCH_SIZE", "DIMENSION", "DIMS",
		"MAX_INPUT_TOKENS", "NUM_CTX", "MAX_CANDIDATES", "MAX_TOKENS", "PARALLELISM",
		"TIMEOUT", "BACKOFF", "RETRY_BUDGET", "CUDA_DEVICE_ID", "MAX_CHARS",
		"RETENTION_DAYS",
	} {
		if strings.Contains(key, token) {
			return "7"
		}
	}
	return "probe"
}

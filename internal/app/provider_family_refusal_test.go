package app

import (
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
)

// TestProviderFamilyKeysRefusedAtStartup proves the contract for a
// configuration copied from a deployment: a provider-family setting this build
// cannot honour stops composition and names the key, rather than being accepted
// and ignored.
func TestProviderFamilyKeysRefusedAtStartup(t *testing.T) {
	cases := []map[string]string{
		{config.EnvEmbeddingsProvider: "onnx"},
		{config.EnvEmbeddingsOnnxModelID: "intfloat/multilingual-e5-small"},
		{config.EnvEmbeddingsLocalModel: "BAAI/bge-small-en-v1.5"},
		{config.EnvLitellmAPIKey: "litellm-key"},
		{config.EnvLLMGeminiServiceTier: "flex"},
		{config.EnvSkipLLMVerification: "true"},
	}
	for _, env := range cases {
		cfg := config.FromEnv(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		})
		_, err := ResolveProvider(cfg)
		if err == nil {
			t.Fatalf("%v must fail startup", env)
		}
		if !strings.Contains(err.Error(), "not supported by this build") {
			t.Fatalf("error for %v = %v, want the explicit refusal", env, err)
		}
		for key := range env {
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("refusal for %v did not name %s: %v", env, key, err)
			}
		}
	}
}

// TestUnimplementedProviderSelectionNamesItsKey covers the chat selection key:
// a provider name with no adapter must name the key to change, not only the
// names that would work.
func TestUnimplementedProviderSelectionNamesItsKey(t *testing.T) {
	cfg := config.FromEnv(func(key string) (string, bool) {
		if key == config.EnvLLMProvider {
			return "cohere", true
		}
		return "", false
	})
	_, err := ResolveProvider(cfg)
	if err == nil {
		t.Fatal("an unimplemented provider selection must fail startup")
	}
	if !strings.Contains(err.Error(), config.EnvLLMProvider) {
		t.Fatalf("error = %v, want it to name %s", err, config.EnvLLMProvider)
	}
}

// TestSupportedProviderFamilyKeysCompose proves the refusal does not overreach:
// the provider-family settings this build does honour compose without error.
func TestSupportedProviderFamilyKeysCompose(t *testing.T) {
	cfg := config.FromEnv(func(key string) (string, bool) {
		values := map[string]string{
			config.EnvLLMProvider:           "mock",
			config.EnvLLMModel:              "gpt-4o-mini",
			config.EnvLLMSupportsMaxItems:   "true",
			config.EnvEmbeddingsProvider:    "openai",
			config.EnvEmbeddingsOpenAIModel: "text-embedding-3-small",
			config.EnvReflectLLMModel:       "gpt-4o",
			// The reranker, embedding-dimension and multi-LLM strategy keys
			// are honoured now: they resolve into configuration instead of
			// being refused.
			config.EnvRerankerProvider:              "cohere",
			config.EnvRerankerLocalModel:            "cross-encoder/ms-marco-MiniLM-L-6-v2",
			config.EnvEmbeddingDimension:            "384",
			config.EnvLLMStrategy:                   `{"mode":"failover"}`,
			config.EnvReflectLLMProvider:            "openai",
			config.EnvMentalModelRefreshLLMStrategy: `{"mode":"failover"}`,
		}
		value, ok := values[key]
		return value, ok && value != ""
	})
	if _, err := ResolveProvider(cfg); err != nil {
		t.Fatalf("an honoured provider configuration must compose: %v", err)
	}
}

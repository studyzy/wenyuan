// Package embed builds the embedding backends the storage area reaches through
// the provider embedding boundary. A deployment configures embeddings
// separately from its chat model, so the adapters here are selected by provider
// name.
//
// Every member that speaks a REST protocol is implemented here; the members
// that need a native runtime (onnx, local sentence-transformers) have no
// adapter and are refused by name at composition time.
package embed

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Defaults applied when the configuration leaves a value unset.
const (
	DefaultBatchSize        = 100
	DefaultMaxConcurrent    = 8
	DefaultTEIBatchSize     = 32
	DefaultCohereModel      = "embed-english-v3.0"
	DefaultGeminiModel      = "text-embedding-004"
	DefaultZeroEntropyModel = "zembed-1"
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultOpenRouterURL    = "https://openrouter.ai/api/v1"
	DefaultRequestyURL      = "https://router.requesty.ai/v1"
	DefaultCohereBaseURL    = "https://api.cohere.com"
	DefaultZeroEntropyURL   = "https://api.zeroentropy.dev"
	DefaultLiteLLMBaseURL   = "http://localhost:4000"
	DefaultGeminiBaseURL    = "https://generativelanguage.googleapis.com"
)

// Embedder is the embedding capability the storage boundary consumes. It is
// narrower than provider.Provider because an embedding backend serves only
// embeddings.
type Embedder interface {
	// Name is the configured provider name.
	Name() string
	// Embed generates one vector per input text, in input order.
	Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error)
	// Dimensions is the width this backend guarantees for every vector.
	Dimensions() int
}

// Config is the resolved embedding configuration for one deployment.
type Config struct {
	// Provider selects the backend.
	Provider string
	// APIKey authenticates the vendor-scoped embedding endpoint.
	APIKey string
	// BaseURL is the embedding endpoint; empty means the adapter default.
	BaseURL string
	// Model is the embedding model identifier.
	Model string
	// BatchSize is how many inputs go in one provider request.
	BatchSize int
	// Dimensions is the resolved output width; zero means the adapter's own.
	Dimensions int
	// MaxInputTokens caps each input's length.
	MaxInputTokens int
	// PassagePrefix is prepended to stored passages.
	PassagePrefix string
	// QueryPrefix is prepended to query text.
	QueryPrefix string
	// MaxConcurrent bounds in-flight requests per encode call.
	MaxConcurrent int
	// Timeout bounds one request.
	Timeout time.Duration
	// Retry is the retry tuning for embedding calls.
	Retry provider.RetryPolicy
}

// acceptedProviders names every embedding provider the boundary recognizes, so
// an unknown selection is reported with the full set rather than a bare
// rejection.
var acceptedProviders = []string{
	"openai", "openrouter", "requesty", "litellm", "cohere", "tei", "zeroentropy", "gemini",
}

// refusedProviders need a native runtime or an SDK that this build does not
// carry, so they are refused by name rather than silently mis-served.
var refusedProviders = map[string]bool{
	"onnx":         true,
	"local":        true,
	"openai-codex": true,
	"litellm-sdk":  true,
	"google":       true,
}

// New builds the configured embedding backend. It returns the Embedder the
// storage area consumes, or an error naming the offending configuration key
// when the backend cannot be honoured.
//
// The returned Embedder's Dimensions reports the configured width when one was
// set, and the boundary's fixed width otherwise: the destination's vector
// column is that width, so a deployment leaving the key blank still stores a
// matching vector.
func New(cfg Config) (Embedder, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" {
		return nil, nil
	}
	if refusedProviders[name] {
		return nil, fmt.Errorf("embed: %s is not supported by this build (requires %s)", name, keyProvider)
	}
	switch name {
	case "openai":
		return newOpenAICompat(cfg, name, cfg.BaseURL, keyOpenAIAPIKey)
	case "openrouter":
		base := cfg.BaseURL
		if base == "" {
			base = DefaultOpenRouterURL
		}
		return newOpenAICompat(cfg, name, base, keyOpenRouterAPIKey)
	case "requesty":
		base := cfg.BaseURL
		if base == "" {
			base = DefaultRequestyURL
		}
		return newOpenAICompat(cfg, name, base, keyRequestyAPIKey)
	case "litellm":
		base := cfg.BaseURL
		if base == "" {
			base = DefaultLiteLLMBaseURL
		}
		return newOpenAICompat(cfg, name, base, "")
	case "cohere":
		return newCohere(cfg)
	case "tei":
		return newTEI(cfg)
	case "zeroentropy":
		return newZeroEntropy(cfg)
	case "gemini":
		return newGemini(cfg)
	default:
		return nil, &provider.InvalidProviderError{Name: name, Accepted: acceptedProviders}
	}
}

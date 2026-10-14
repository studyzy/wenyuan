package app

import (
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/anthropic"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/provider/openai"
)

// ProviderRegistry returns the provider registry the composition root builds
// the active provider from. The no-model selection, the OpenAI-compatible
// adapter (which serves every vendor speaking that protocol) and the native
// Anthropic adapter are registered; the OAuth CLI-tool providers are not, as
// they need an interactive login this service does not host.
func ProviderRegistry() *provider.Registry {
	reg := provider.NewRegistry()
	reg.Register(provider.Registration{
		Name:               none.NameNone,
		RequiresCredential: false,
		Build: func(settings provider.Settings) (provider.Provider, error) {
			return none.New(), nil
		},
	})
	openai.Register(reg)
	anthropic.Register(reg)
	return reg
}

// ResolveProvider selects the active provider from the resolved configuration
// and builds it once, so a selection error surfaces at composition time
// rather than at first call. Every consumer reaches the boundary through this
// single function.
func ResolveProvider(cfg config.Config) (provider.Provider, error) {
	pc := cfg.Provider
	selected := pc.Provider
	if selected == "" {
		// No provider configured is the documented no-model mode, not an
		// error: the store keeps serving deterministically.
		selected = none.NameNone
	}
	settings := ProviderSettings(pc)
	settings.Provider = selected
	return provider.Select(ProviderRegistry(), settings)
}

// ProviderSettings maps the resolved provider configuration onto the
// boundary's pure settings value. It performs no I/O and holds no handle, so
// it can be built and compared in tests.
func ProviderSettings(pc config.ProviderConfig) provider.Settings {
	settings := provider.Settings{
		Provider:              pc.Provider,
		APIKey:                pc.APIKey,
		BaseURL:               pc.BaseURL,
		Model:                 pc.Model,
		Timeout:               pc.Timeout,
		ConnectTimeout:        pc.ConnectTimeout,
		Concurrency:           provider.ConcurrencyPolicy{Global: pc.MaxConcurrent},
		ReasoningEffort:       pc.ReasoningEffort,
		StrictSchema:          pc.StrictSchema,
		JSONMode:              pc.OpenAIJSONMode,
		ServiceTier:           pc.OpenAIServiceTier,
		OutputLanguage:        pc.OutputLanguage,
		SendBankAsUser:        pc.SendBankAsUser,
		CacheAffinity:         pc.CacheAffinity,
		PromptCacheEnabled:    pc.PromptCacheEnabled,
		ExtraBody:             pc.ExtraBody,
		Headers:               pc.DefaultHeaders,
		Vision:                pc.Vision,
		SupportsMaxItems:      pc.SupportsMaxItems,
		SupportsStringPattern: pc.SupportsStringPattern,
		ForcedTool:            pc.ForcedTool,
		Retry: provider.RetryPolicy{
			MaxRetries:     pc.MaxRetries,
			InitialBackoff: pc.InitialBackoff,
			MaxBackoff:     pc.MaxBackoff,
		},
	}
	if pc.Embeddings.Provider != "" || pc.Embeddings.OpenAIModel != "" {
		settings.Embedding = &provider.EmbeddingSettings{
			Provider:              pc.Embeddings.Provider,
			APIKey:                pc.Embeddings.OpenAIAPIKey,
			BaseURL:               pc.Embeddings.OpenAIBaseURL,
			Model:                 pc.Embeddings.OpenAIModel,
			BatchSize:             pc.Embeddings.OpenAIBatchSize,
			VendorDimensions:      pc.Embeddings.OpenAIDimensions,
			MaxInputTokens:        pc.Embeddings.MaxInputTokens,
			PassagePrefix:         pc.Embeddings.PassagePrefix,
			QueryPrefix:           pc.Embeddings.QueryPrefix,
			MaxConcurrentRequests: pc.Embeddings.MaxConcurrentRequests,
			LocalModel:            pc.Embeddings.LocalModel,
			LocalForceCPU:         pc.Embeddings.LocalForceCPU,
			LocalTrustRemoteCode:  pc.Embeddings.LocalTrustRemoteCode,
			AllowMPS:              pc.Embeddings.AllowMPS,
			AllowMPSWasSet:        pc.Embeddings.AllowMPS,
			Retry: provider.RetryPolicy{
				MaxRetries:     pc.Embeddings.MaxRetries,
				InitialBackoff: pc.Embeddings.InitialBackoff,
				MaxBackoff:     pc.Embeddings.MaxBackoff,
				Budget:         pc.Embeddings.RetryBudget,
			},
		}
	}
	return settings
}

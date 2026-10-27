package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/anthropic"
	"github.com/studyzy/wenyuan/internal/provider/mock"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/provider/openai"
	"github.com/studyzy/wenyuan/internal/provider/routing"
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
	reg.Register(provider.Registration{
		Name:               mock.NameMock,
		RequiresCredential: false,
		Build: func(settings provider.Settings) (provider.Provider, error) {
			return &mock.Provider{}, nil
		},
	})
	return reg
}

// ResolveProvider selects the active provider from the resolved configuration
// and builds it once, so a selection error surfaces at composition time
// rather than at first call. Every consumer reaches the boundary through this
// single function.
func ResolveProvider(cfg config.Config) (provider.Provider, error) {
	pc := cfg.Provider
	// A setting this build cannot honour fails startup rather than being
	// silently ignored: an operator who configured a routing strategy or a
	// local embedding model must be told it is doing nothing, because the
	// alternative is a deployment that believes it routes across models and
	// does not.
	if err := unsupportedProviderKeys(cfg); err != nil {
		return nil, err
	}
	selected := pc.Provider
	if selected == "" {
		// No provider configured is the documented no-model mode, not an
		// error: the store keeps serving deterministically.
		selected = none.NameNone
	}
	settings := ProviderSettings(pc)
	settings.Provider = selected
	reg := ProviderRegistry()
	built, err := provider.Select(reg, settings)
	if err != nil && provider.IsInvalidProvider(err) {
		// A name this build has no adapter for must name the key the operator
		// has to change, not merely the list of names that would work.
		return nil, fmt.Errorf("%s: %w", config.EnvLLMProvider, err)
	}
	if err != nil {
		return nil, err
	}
	// A malformed routing-strategy document fails startup rather than silently
	// routing nothing, which is what the reference does.
	if cfg.Provider.StrategyError != nil {
		return nil, cfg.Provider.StrategyError
	}
	// When a strategy is configured, cross the primary with every indexed
	// member: the chain is what routes a request across them.
	strategy, routed := routingStrategy(cfg.Provider.Strategy, len(cfg.Provider.LLMMembers))
	if !routed {
		return built, nil
	}
	members, err := chatChainMembers(reg, cfg, built)
	if err != nil {
		return nil, err
	}
	return routing.New(members, strategy)
}

// VerifyModelProvider probes the configured model provider once at startup, so
// an unreachable endpoint fails composition instead of surfacing on the first
// request. The provider's own VerifyConnection is the probe: it already issues
// the vendor-correct minimal request, so startup adds a deadline around it
// rather than a second, parallel smoke test.
//
// The probe is bounded by the model-init timeout, which defaults to the
// reference's 300s; a non-positive value leaves the probe unbounded rather than
// expiring it immediately. WENYUAN_API_SKIP_LLM_VERIFICATION turns the probe
// off entirely — an operator running against a provider that is temporarily
// down can still start the server and let it recover — and a skipped probe
// reports no failure.
//
// The no-model and mock selections are deliberately not probed: their
// VerifyConnection is a no-op success, and treating "no model configured" as a
// connectivity failure would refuse the one deployment shape that has nothing
// to connect to.
func VerifyModelProvider(ctx context.Context, cfg config.Config, p provider.Provider) error {
	if cfg.SkipLLMVerification {
		return nil
	}
	if p == nil || !probeableProvider(p.Name()) {
		return nil
	}
	probeCtx := ctx
	if cfg.ModelInitTimeout > 0 {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, cfg.ModelInitTimeout)
		defer cancel()
	}
	if err := p.VerifyConnection(probeCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("model provider %q did not verify within %s (%s): %w",
				p.Name(), cfg.ModelInitTimeout, config.EnvModelInitTimeout, err)
		}
		return fmt.Errorf("model provider %q failed startup verification: %w", p.Name(), err)
	}
	return nil
}

// probeableProvider reports whether a provider selection is worth probing. The
// no-model and mock selections are valid composition states with nothing to
// reach, so they are the two names startup does not probe.
func probeableProvider(name string) bool {
	return name != none.NameNone && name != mock.NameMock
}

// SupportsVision resolves whether the configured write model can read inline
// image content, mirroring the reference's supports_vision(): an explicit
// WENYUAN_API_LLM_VISION wins, otherwise the provider's own answer — the
// OpenAI adapter claims support only for OpenAI itself, the native Anthropic
// adapter claims it, and every gateway or proxy is unknown, which a retain
// refuses rather than silently dropping the image.
func SupportsVision(cfg config.Config) bool {
	if cfg.Provider.Vision != nil {
		return *cfg.Provider.Vision
	}
	switch cfg.Provider.Provider {
	case "openai", "anthropic", mock.NameMock:
		return true
	default:
		return false
	}
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

// unsupportedProviderKeys reports the provider settings this build parses but
// cannot act on. It is the explicit answer to a silent no-op: the provider
// family classification lives in the configuration package, next to the key
// list it is derived from, and this call site only forwards it.
func unsupportedProviderKeys(cfg config.Config) error {
	return cfg.ValidateSupported()
}

// reflectModelConfigured reports whether the deployment has a model for the
// reflect pipeline. The reference refuses reflect and mental-model refresh when
// the provider is "none" (WENYUAN_API_LLM_PROVIDER=none), so the composition
// root uses this to arm both availability gates; a deployment with no model
// therefore behaves like the reference rather than falling back to the
// deterministic loop.
func reflectModelConfigured(p provider.Provider) bool {
	return p != nil && p.Name() != none.NameNone
}

package app

import (
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/embed"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/provider/rerank"
	"github.com/studyzy/wenyuan/internal/provider/routing"
)

// resolveEmbedder builds the configured embedding backend. Embeddings are
// configured independently of the chat model, so the backend is selected by the
// EMBEDDINGS_PROVIDER family rather than by the active chat provider. A
// deployment that configured no embedding provider yields nil, which leaves
// every unit unembedded and recall on its non-vector arms.
//
// The resolved dimension is validated against the stored column width: the
// column is fixed by the migration series, so a configuration that asks for a
// different width is refused by name rather than silently failing to store.
func resolveEmbedder(cfg config.Config) (embed.Embedder, error) {
	e := cfg.Provider.Embeddings
	if strings.TrimSpace(e.Provider) == "" && strings.TrimSpace(e.OpenAIModel) == "" {
		return nil, nil
	}
	if e.Dimensions != 0 && e.Dimensions != provider.EmbeddingDimensions {
		return nil, fmt.Errorf(
			"%s resolves the embedding width to %d but the stored vector column is %d; the migration series fixes the column width, so this deployment must run with %d or the schema must be regenerated",
			config.EnvEmbeddingDimension, e.Dimensions, provider.EmbeddingDimensions, provider.EmbeddingDimensions)
	}
	settings := embeddingConfigFor(e)
	return embed.New(settings)
}

// resolveReranker builds the configured cross-encoder chain. The reranker is
// configured independently of the chat model, so it is selected by the
// RERANKER_PROVIDER family. An unconfigured reranker yields nil, which leaves
// recall on its best-per-arm fallback.
func resolveReranker(cfg config.Config) (provider.Reranker, error) {
	r := cfg.Provider.Rerank
	if strings.TrimSpace(r.Provider) == "" {
		return nil, nil
	}
	members := make([]rerank.Member, 0, len(r.Members))
	for _, m := range r.Members {
		members = append(members, rerankMember(m))
	}
	return rerank.New(rerank.Config{
		Provider:         r.Provider,
		Primary:          rerankMember(r.Primary),
		Members:          members,
		CandidateDepth:   r.CandidateDepth,
		SendBankAsHeader: r.SendBankAsHeader,
		Retry: provider.RetryPolicy{
			MaxRetries:     r.MaxRetries,
			InitialBackoff: r.InitialBackoff,
			MaxBackoff:     r.MaxBackoff,
			Budget:         r.RetryBudget,
		},
	})
}

// rerankMember maps one resolved reranker member onto the adapter's settings,
// choosing the credential, endpoint and model the member's own provider reads.
func rerankMember(m config.RerankerMember) rerank.Member {
	out := rerank.Member{
		Index:                 m.Index,
		Provider:              m.Provider,
		MaxTokensPerCandidate: m.MaxTokensPerCandidate,
	}
	switch strings.ToLower(strings.TrimSpace(m.Provider)) {
	case "tei":
		out.BaseURL = m.TEIURL
		out.MaxConcurrent = m.TEIMaxConcurrent
		out.Timeout = m.TEIHTTPTimeout
	case "cohere":
		out.APIKey, out.Model, out.BaseURL = m.CohereAPIKey, m.CohereModel, m.CohereBaseURL
		out.Timeout = m.CohereTimeout
	case "openrouter":
		out.APIKey, out.Model, out.BaseURL = m.OpenRouterAPIKey, m.OpenRouterModel, m.OpenRouterBaseURL
		out.Timeout = m.OpenRouterTimeout
	case "zeroentropy":
		out.APIKey, out.Model, out.BaseURL = m.ZeroEntropyAPIKey, m.ZeroEntropyModel, m.ZeroEntropyBaseURL
		out.Timeout = m.ZeroEntropyTimeout
	case "siliconflow":
		out.APIKey, out.Model, out.BaseURL = m.SiliconflowAPIKey, m.SiliconflowModel, m.SiliconflowBaseURL
		out.Timeout = m.SiliconflowTimeout
	case "typesafe":
		out.APIKey, out.Model, out.BaseURL = m.TypeSafeAPIKey, m.TypeSafeModel, m.TypeSafeBaseURL
		out.PruneCandidates = m.TypeSafePruneCandidates
		out.MaxConcurrent = m.TypeSafeMaxConcurrent
		out.Timeout = m.TypeSafeTimeout
	case "litellm":
		out.APIKey, out.Model, out.BaseURL = m.LiteLLMAPIKey, m.LiteLLMModel, m.LiteLLMAPIBase
		out.Timeout = m.LiteLLMTimeout
	case "litellm-sdk":
		out.APIKey, out.Model, out.BaseURL = m.LiteLLMSDKAPIKey, m.LiteLLMSDKModel, m.LiteLLMSDKAPIBase
		out.Timeout = m.LiteLLMSDKTimeout
	case "google":
		out.Model = m.GoogleModel
		out.Timeout = m.GoogleTimeout
	case "alibaba":
		out.APIKey, out.Model = m.AlibabaAPIKey, m.AlibabaModel
		out.Timeout = m.AlibabaTimeout
	case "local":
		out.Model = m.LocalModel
	case "flashrank":
		out.Model = m.FlashRankModel
	}
	return out
}

// embeddingConfigFor maps the resolved embedding configuration onto the
// backend's settings, choosing the credential, endpoint and model the selected
// provider reads.
func embeddingConfigFor(e config.EmbeddingConfig) embed.Config {
	out := embed.Config{
		Provider:       e.Provider,
		Dimensions:     e.Dimensions,
		MaxInputTokens: e.MaxInputTokens,
		PassagePrefix:  e.PassagePrefix,
		QueryPrefix:    e.QueryPrefix,
		MaxConcurrent:  e.MaxConcurrentRequests,
		Retry: provider.RetryPolicy{
			MaxRetries:     e.MaxRetries,
			InitialBackoff: e.InitialBackoff,
			MaxBackoff:     e.MaxBackoff,
			Budget:         e.RetryBudget,
		},
	}
	switch strings.ToLower(strings.TrimSpace(e.Provider)) {
	case "", "openai":
		// The shared batch key is the OpenAI family's; every other provider
		// carries its own batch setting and its adapter's own default, which
		// differ from OpenAI's (TEI caps at 32 and validates the cap), so the
		// seed must not leak across the family boundary.
		out.BatchSize = e.OpenAIBatchSize
		out.APIKey, out.BaseURL, out.Model = e.OpenAIAPIKey, e.OpenAIBaseURL, e.OpenAIModel
		if e.OpenAIDimensions > 0 {
			out.Dimensions = e.OpenAIDimensions
		}
	case "openrouter":
		out.APIKey, out.Model = e.OpenRouterAPIKey, e.OpenRouterModel
	case "requesty":
		out.APIKey, out.Model = e.RequestyAPIKey, e.RequestyModel
	case "litellm":
		out.APIKey, out.BaseURL, out.Model = e.LiteLLMAPIKey, e.LiteLLMAPIBase, e.LiteLLMModel
		if e.LiteLLMDimensions > 0 {
			out.Dimensions = e.LiteLLMDimensions
		}
	case "cohere":
		out.APIKey, out.BaseURL, out.Model = e.CohereAPIKey, e.CohereBaseURL, e.CohereModel
		if e.CohereOutputDimensions > 0 {
			out.Dimensions = e.CohereOutputDimensions
		}
	case "tei":
		out.BaseURL = e.TEIURL
		if e.TEIBatchSize > 0 {
			out.BatchSize = e.TEIBatchSize
		}
	case "zeroentropy":
		out.APIKey, out.BaseURL, out.Model = e.ZeroEntropyAPIKey, e.ZeroEntropyBaseURL, e.ZeroEntropyModel
		if e.ZeroEntropyDimensions > 0 {
			out.Dimensions = e.ZeroEntropyDimensions
		}
	case "gemini", "google":
		out.APIKey, out.Model = e.GeminiAPIKey, e.GeminiModel
		if e.GeminiBatchSize > 0 {
			out.BatchSize = e.GeminiBatchSize
		}
		if e.GeminiOutputDimensionality > 0 {
			out.Dimensions = e.GeminiOutputDimensionality
		}
	}
	return out
}

// routingStrategy maps a resolved strategy onto the routing package's value and
// reports whether a chain should be built at all. A strategy with no mode, or a
// chain of a single member, keeps the plain provider.
func routingStrategy(s config.StrategyConfig, memberCount int) (routing.Strategy, bool) {
	if strings.TrimSpace(s.Mode) == "" || memberCount == 0 {
		return routing.Strategy{}, false
	}
	out := routing.Strategy{Mode: s.Mode, Weights: s.Weights}
	for _, r := range s.Routes {
		out.Routes = append(out.Routes, routing.Route{Key: r.Key, Value: r.Value, Member: r.Member})
	}
	return out, true
}

// chatChainMembers builds the ordered member list of the chat chain: the
// primary first, then the configured indexed members in declared order.
func chatChainMembers(reg *provider.Registry, cfg config.Config, primary provider.Provider) ([]provider.Provider, error) {
	members := []provider.Provider{primary}
	for _, m := range cfg.Provider.LLMMembers {
		settings := settingsForMember(cfg.Provider, m)
		built, err := provider.Select(reg, settings)
		if err != nil {
			return nil, fmt.Errorf("multi-LLM member %d (%s): %w", m.Index, m.Provider, err)
		}
		members = append(members, built)
	}
	return members, nil
}

// settingsForMember derives one indexed member's settings from the operation's
// resolved chat settings, overriding what the member itself carries. The
// per-member request policy (timeout, retries) is its own when it set one, so a
// member's internal retry budget is the configured count and not the primary's.
func settingsForMember(pc config.ProviderConfig, m config.LLMMemberConfig) provider.Settings {
	settings := ProviderSettings(pc)
	settings.Embedding = nil
	settings.Rerank = nil
	settings.Provider = m.Provider
	if m.APIKey != "" {
		settings.APIKey = m.APIKey
	}
	if m.BaseURL != "" {
		settings.BaseURL = m.BaseURL
	}
	if m.Model != "" {
		settings.Model = m.Model
	}
	if m.ReasoningEffort != "" {
		settings.ReasoningEffort = m.ReasoningEffort
	}
	if m.CacheAffinity != "" {
		settings.CacheAffinity = m.CacheAffinity
	}
	if m.ExtraBody != nil {
		settings.ExtraBody = m.ExtraBody
	}
	if m.DefaultHeaders != nil {
		settings.Headers = m.DefaultHeaders
	}
	if m.Timeout > 0 {
		settings.Timeout = m.Timeout
	}
	if m.MaxRetriesSet {
		settings.Retry.MaxRetries = m.MaxRetries
	}
	return settings
}

// recallRerankerWiring selects the reranker the recall pipeline uses and the
// flags derived from it. A dedicated RERANKER-family backend wins; otherwise
// the chat provider's own cross-encoder (when it has one) is used, preserving
// the behaviour a deployment that configured no reranker family had.
//
// The returned flags let the pipeline interpret the scores correctly:
// scoresByRankPosition suppresses publishing them and rejects a reranker floor,
// prunes drops candidates the reranker marked 0.0, and seedFromFused seeds the
// base from the fused order instead of the reranker's own valueless output.
func recallRerankerWiring(cfg config.Config, chat provider.Provider, dedicated provider.Reranker) (memoryops.RecallReranker, int, bool, bool, bool) {
	r := dedicated
	byRank, prunes, seed := false, false, false
	if r != nil {
		if s, ok := r.(interface{ ScoresByRankPosition() bool }); ok {
			byRank = s.ScoresByRankPosition()
		}
		if p, ok := r.(interface{ PrunesCandidates() bool }); ok {
			prunes = p.PrunesCandidates()
		}
		if f, ok := r.(interface{ SeedsFromFused() bool }); ok {
			seed = f.SeedsFromFused()
		}
	} else {
		r = provider.ProviderReranker(chat)
	}
	if r == nil || chat == nil || chat.Name() == none.NameNone {
		return nil, 0, false, false, false
	}
	return rerankScorer{reranker: r}, cfg.Provider.Rerank.CandidateDepth, byRank, prunes, seed
}

// Package rerank builds the cross-encoder backends the recall pipeline reaches
// through provider.Reranker. A deployment configures the reranker separately
// from its chat model, so the adapters here are selected by provider name
// rather than by the active chat provider.
//
// Every member that speaks a network protocol is implemented here; the members
// that need a native runtime (local sentence-transformers, flashrank, jina-mlx)
// or a vendor SDK (litellm, google, alibaba) have no adapter and are refused by
// name at composition time.
package rerank

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Defaults applied when the configuration leaves a value unset. They mirror the
// reference's own defaults so an operator leaving a key blank gets the same
// behaviour.
const (
	DefaultCandidateDepth         = 50
	DefaultMaxTokensPerCandidate  = 512
	DefaultTimeout                = 60 * time.Second
	DefaultMaxConcurrent          = 8
	DefaultSiliconflowBaseURL     = "https://api.siliconflow.cn/v1"
	DefaultSiliconflowModel       = "BAAI/bge-reranker-v2-m3"
	DefaultCohereModel            = "rerank-english-v3.0"
	DefaultZeroEntropyModel       = "zerank-2"
	DefaultZeroEntropyBaseURL     = "https://api.zeroentropy.dev"
	DefaultOpenRouterBaseURL      = "https://openrouter.ai/api/v1"
	DefaultOpenRouterModel        = "cohere/rerank-english-v3.0"
	DefaultTypeSafeBaseURL        = "https://api.typesafe.ai"
	DefaultTypeSafeModel          = "jev"
	DefaultTEIBatchSize           = 32
	DefaultLiteLLMBaseURL         = "http://localhost:4000"
	DefaultLiteLLMModel           = "rerank-english-v3.0"
	DefaultLiteLLMSDKModel        = "cohere/rerank-english-v3.0"
	DefaultGoogleModel            = "semantic-ranker-default-004"
	DefaultAlibabaModel           = "gte-rerank-v2"
	DefaultFlashRankModel         = "ms-marco-MiniLM-L-12-v2"
	DefaultLocalModel             = "BAAI/bge-reranker-v2-m3"
	DefaultJinaMLXModel           = "jinaai/jina-reranker-v2-base-multilingual"
	DefaultMaxQuestionTokens      = 26000
	DefaultTypeSafeMaxOptions     = 250
	DefaultTypeSafeShortlist      = 12
	DefaultLiteLLMMaxTokensPerDoc = 512
)

// Member is one entry in the reranker chain. Member 0 is the primary
// (the unindexed HINDSIGHT_API_RERANKER_* settings); members 1..N come from the
// indexed HINDSIGHT_API_RERANKER_<n>_* settings and inherit nothing from the
// primary.
type Member struct {
	// Index is the member's position; 0 is the primary.
	Index int
	// Provider is the selected rerank provider name.
	Provider string
	// APIKey authenticates the vendor-scoped endpoint.
	APIKey string
	// BaseURL is the endpoint; empty lets the adapter apply its default.
	BaseURL string
	// Model is the reranking model identifier; empty means the adapter default.
	Model string
	// Timeout bounds one request.
	Timeout time.Duration
	// MaxConcurrent bounds simultaneous calls to this member.
	MaxConcurrent int
	// PruneCandidates asks a reranker that can cut its pool to also report how
	// far relevance extends (TypeSafe only).
	PruneCandidates bool
	// MaxTokensPerCandidate caps each candidate's length; zero disables
	// truncation.
	MaxTokensPerCandidate int
}

// Config is the resolved reranker configuration for one deployment.
type Config struct {
	// Provider selects the primary member's backend.
	Provider string
	// Primary is the resolved primary member (index 0).
	Primary Member
	// Members are the indexed extra members (1..N) in declared order.
	Members []Member
	// CandidateDepth bounds how many fused candidates reach the reranker.
	CandidateDepth int
	// Retry is the retry tuning for rerank calls.
	Retry provider.RetryPolicy
	// SendBankAsHeader adds the calling bank to each request header.
	SendBankAsHeader bool
}

// RankPositionReranker is the optional interface a reranker implements when the
// numbers it returns are positions in its own ordering rather than relevance
// scores. A caller must not treat such a value as a calibrated relevance: a
// position says "ahead of the candidates below it", nothing about absolute
// quality, and two calls are not comparable.
type RankPositionReranker interface {
	// ScoresByRankPosition reports that the returned values are rank positions.
	ScoresByRankPosition() bool
}

// candidatePruner is the optional interface a reranker implements when it marks
// the candidates it judged irrelevant with a score of exactly 0.0, which tells
// the caller it may drop them.
type candidatePruner interface {
	// PrunesCandidates reports whether zero means "leave this candidate out".
	PrunesCandidates() bool
}

// New builds the configured reranker chain. It returns the provider.Reranker
// the recall pipeline consumes, or an error naming the offending configuration
// key when a member cannot be honoured.
//
// The chain tries its members in declared order and returns the first usable
// set of scores, so an unreachable primary costs ranking quality rather than
// the whole recall. A deterministic output-too-long refusal is not retried
// against the next member: the input, not the backend, is at fault.
func New(cfg Config) (provider.Reranker, error) {
	primary := cfg.Primary
	if strings.TrimSpace(primary.Provider) == "" {
		primary.Provider = cfg.Provider
	}
	if strings.TrimSpace(primary.Provider) == "" {
		return nil, nil
	}
	primary.Index = 0
	built, err := buildMember(cfg, primary)
	if err != nil {
		return nil, err
	}
	members := []provider.Reranker{built}
	for offset, member := range cfg.Members {
		member.Index = offset + 1
		next, err := buildMember(cfg, member)
		if err != nil {
			return nil, err
		}
		members = append(members, next)
	}
	return &chain{members: members, primary: built}, nil
}

// chain is the ordered failover chain over one primary and its fallbacks.
type chain struct {
	members []provider.Reranker
	primary provider.Reranker
}

// Rerank returns the first member's usable scores, surfacing the last error
// when no member answers.
func (c *chain) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	var last error
	for _, member := range c.members {
		result, err := member.Rerank(ctx, req)
		if err != nil {
			if provider.IsOutputTooLong(err) {
				return provider.RerankResult{}, err
			}
			last = err
			continue
		}
		if len(result.Scores) != len(req.Documents) {
			last = fmt.Errorf("rerank: member returned %d scores for %d documents", len(result.Scores), len(req.Documents))
			continue
		}
		return result, nil
	}
	if last == nil {
		last = fmt.Errorf("rerank: no member was configured")
	}
	return provider.RerankResult{}, last
}

// ScoresByRankPosition reports whether the primary member's scores are rank
// positions. It follows the primary, not whichever member last served: the
// caller sets a flag from this and must interpret every result the same way.
func (c *chain) ScoresByRankPosition() bool {
	scorer, ok := c.primary.(RankPositionReranker)
	return ok && scorer.ScoresByRankPosition()
}

// PrunesCandidates reports whether the primary member drops candidates it
// judges irrelevant, marking them with a score of exactly 0.0.
func (c *chain) PrunesCandidates() bool {
	pruner, ok := c.primary.(candidatePruner)
	return ok && pruner.PrunesCandidates()
}

// SeedsFromFused reports whether the primary member is the RRF passthrough,
// whose returned values carry no ordering of their own. The caller seeds the
// base score from the fused ordering in that case rather than normalising an
// output that means nothing.
func (c *chain) SeedsFromFused() bool {
	seeder, ok := c.primary.(fusedSeeder)
	return ok && seeder.SeedsFromFused()
}

// fusedSeeder is implemented by a backend whose output carries no ordering.
type fusedSeeder interface {
	SeedsFromFused() bool
}

// buildMember constructs one member's backend and applies its per-candidate
// token cap.
func buildMember(cfg Config, member Member) (provider.Reranker, error) {
	built, err := buildBackend(cfg, member)
	if err != nil {
		return nil, err
	}
	if member.MaxTokensPerCandidate > 0 {
		built = capped{inner: built, maxTokens: member.MaxTokensPerCandidate}
	}
	return built, nil
}

// envName is the operator-facing variable this member reads suffix from: the
// bare name for the primary, the index-prefixed name for a fallback.
func (m Member) envName(suffix string) string {
	if m.Index == 0 {
		return "HINDSIGHT_API_RERANKER_" + suffix
	}
	return fmt.Sprintf("HINDSIGHT_API_RERANKER_%d_%s", m.Index, suffix)
}

// refusedMembers are the provider names this build has no adapter for, with the
// runtime or SDK each would need. They are refused rather than approximated.
var refusedMembers = map[string]string{
	"local":       "an in-process sentence-transformers runtime",
	"flashrank":   "an in-process ONNX runtime",
	"jina-mlx":    "an in-process MLX runtime",
	"litellm":     "the LiteLLM proxy stack",
	"litellm-sdk": "the LiteLLM SDK",
	"google":      "the Google Vertex AI ranking SDK",
	"alibaba":     "the Alibaba Cloud DashScope SDK",
}

func buildBackend(cfg Config, member Member) (provider.Reranker, error) {
	name := strings.ToLower(strings.TrimSpace(member.Provider))
	if need, ok := refusedMembers[name]; ok {
		return nil, fmt.Errorf(
			"rerank: %s=%q needs %s and is not supported by this build",
			member.envName("PROVIDER"), name, need,
		)
	}
	switch name {
	case "siliconflow":
		key, err := member.requireKey("SILICONFLOW_API_KEY")
		if err != nil {
			return nil, err
		}
		base := defaultString(member.BaseURL, DefaultSiliconflowBaseURL)
		return &cohereClient{
			poster:      newPoster(member.Timeout, cfg.Retry, cfg.SendBankAsHeader, member.MaxConcurrent),
			url:         strings.TrimRight(base, "/") + "/rerank",
			apiKey:      key,
			model:       defaultString(member.Model, DefaultSiliconflowModel),
			includeTopN: true,
		}, nil
	case "zeroentropy":
		key, err := member.requireKey("ZEROENTROPY_API_KEY")
		if err != nil {
			return nil, err
		}
		base := defaultString(member.BaseURL, DefaultZeroEntropyBaseURL)
		return &cohereClient{
			poster:      newPoster(member.Timeout, cfg.Retry, cfg.SendBankAsHeader, member.MaxConcurrent),
			url:         strings.TrimRight(base, "/") + "/v1/models/rerank",
			apiKey:      key,
			model:       defaultString(member.Model, DefaultZeroEntropyModel),
			includeTopN: true,
		}, nil
	case "openrouter":
		key, err := member.requireKey("OPENROUTER_API_KEY")
		if err != nil {
			return nil, err
		}
		base := defaultString(member.BaseURL, DefaultOpenRouterBaseURL)
		return &cohereClient{
			poster:      newPoster(member.Timeout, cfg.Retry, cfg.SendBankAsHeader, member.MaxConcurrent),
			url:         strings.TrimRight(base, "/") + "/rerank",
			apiKey:      key,
			model:       defaultString(member.Model, DefaultOpenRouterModel),
			includeTopN: true,
		}, nil
	case "cohere":
		key, err := member.requireKey("COHERE_API_KEY")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(member.BaseURL) == "" {
			return nil, fmt.Errorf(
				"rerank: %s is required when %s is \"cohere\"; the native Cohere SDK is not a REST protocol this build implements",
				member.envName("COHERE_BASE_URL"), member.envName("PROVIDER"),
			)
		}
		// The configured base already carries the full invoke path, and the
		// contract of such hosts rejects top_n.
		return &cohereClient{
			poster: newPoster(member.Timeout, cfg.Retry, cfg.SendBankAsHeader, member.MaxConcurrent),
			url:    member.BaseURL,
			apiKey: key,
			model:  defaultString(member.Model, DefaultCohereModel),
		}, nil
	case "tei":
		if strings.TrimSpace(member.BaseURL) == "" {
			return nil, fmt.Errorf(
				"rerank: %s is required when %s is \"tei\"",
				member.envName("TEI_URL"), member.envName("PROVIDER"),
			)
		}
		return &teiClient{
			poster: newPoster(member.Timeout, cfg.Retry, cfg.SendBankAsHeader, member.MaxConcurrent),
			url:    strings.TrimRight(member.BaseURL, "/") + "/rerank",
		}, nil
	case "typesafe":
		key, err := member.requireKey("TYPESAFE_API_KEY")
		if err != nil {
			return nil, err
		}
		base := defaultString(member.BaseURL, DefaultTypeSafeBaseURL)
		return &typesafeClient{
			poster:           newPoster(member.Timeout, cfg.Retry, cfg.SendBankAsHeader, member.MaxConcurrent),
			url:              strings.TrimRight(base, "/") + typeSafeSystemOnePath,
			apiKey:           key,
			model:            defaultString(member.Model, DefaultTypeSafeModel),
			prune:            member.PruneCandidates,
			maxOptions:       DefaultTypeSafeMaxOptions,
			shortlist:        DefaultTypeSafeShortlist,
			maxQuestionToken: DefaultMaxQuestionTokens,
		}, nil
	case "rrf":
		return rrfReranker{}, nil
	default:
		return nil, fmt.Errorf(
			"rerank: %s=%q is not supported by this build",
			member.envName("PROVIDER"), name,
		)
	}
}

// requireKey returns the member's credential or names the key it is missing.
func (m Member) requireKey(keySuffix string) (string, error) {
	if strings.TrimSpace(m.APIKey) == "" {
		return "", fmt.Errorf(
			"rerank: %s is required when %s is %q",
			m.envName(keySuffix), m.envName("PROVIDER"), m.Provider,
		)
	}
	return m.APIKey, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// rrfReranker preserves the caller's fused order: it hands back descending
// positions so the candidate the caller ranked first stays first. It performs
// no network call, so it is never worth retrying and never fails.
type rrfReranker struct{}

// ScoresByRankPosition reports that the values are positions, not relevance:
// the top candidate is 1.0 on every recall, so they are not published.
func (rrfReranker) ScoresByRankPosition() bool { return true }

// SeedsFromFused reports that the output carries no ordering of its own.
func (rrfReranker) SeedsFromFused() bool { return true }

func (rrfReranker) Rerank(_ context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	count := len(req.Documents)
	scores := make([]float64, count)
	for i := range scores {
		scores[i] = float64(count-i) / float64(count)
	}
	return provider.RerankResult{Scores: scores}, nil
}

// capped applies a member's per-candidate token cap before the request reaches
// the backend. Where the reference counts tokens with the backend's own
// tokenizer, this build uses the whitespace estimate in tokenEstimate; the cap
// only bounds request size, so the approximation is safe.
type capped struct {
	inner     provider.Reranker
	maxTokens int
}

func (c capped) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	capped := make([]string, len(req.Documents))
	changed := false
	for i, doc := range req.Documents {
		capped[i] = truncateTokens(doc, c.maxTokens)
		if capped[i] != doc {
			changed = true
		}
	}
	if changed {
		req.Documents = capped
	}
	return c.inner.Rerank(ctx, req)
}

// ScoresByRankPosition forwards the inner backend's answer so wrapping a member
// never hides its rank-position or pruning contract.
func (c capped) ScoresByRankPosition() bool {
	scorer, ok := c.inner.(RankPositionReranker)
	return ok && scorer.ScoresByRankPosition()
}

// PrunesCandidates forwards the inner backend's answer.
func (c capped) PrunesCandidates() bool {
	pruner, ok := c.inner.(candidatePruner)
	return ok && pruner.PrunesCandidates()
}

// SeedsFromFused forwards the inner backend's answer.
func (c capped) SeedsFromFused() bool {
	seeder, ok := c.inner.(fusedSeeder)
	return ok && seeder.SeedsFromFused()
}

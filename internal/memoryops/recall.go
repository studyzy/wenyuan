package memoryops

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// defaultBudgetLevel is the recall budget applied when the request omits one.
const defaultBudgetLevel = storage.BudgetMid

// defaultTagMatchMode is the tag-matching mode applied when the request omits
// one.
const defaultTagMatchMode = storage.TagsMatchAny

// Recall serves the memory recall route: a single synchronous JSON read with no
// streaming variant.
func (h *Handlers) Recall(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	var req RecallRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		// A malformed tag-group filter gets its own message: the body is valid
		// JSON, so "not valid JSON" would send the caller looking in the wrong
		// place.
		if errors.Is(err, ErrMalformedTagGroup) {
			httpx.WriteValidationFor(w, r, tagGroupFailure())
			return
		}
		httpx.WriteValidationFor(w, r, httpx.BodyDecodeFailure(err, body))
		return
	}
	if failure := ValidateRecall(req); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	if h.RerankerScoresByRankPosition && hasRerankerFloor(req.MinScores) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"min_scores.reranker is not supported with a rank-position reranker: its scores are rank "+
				"positions within each result set, so a floor would keep a fixed share of results regardless "+
				"of relevance. Use min_scores.final, or a pointwise reranker."))
		return
	}
	query, httpErr := h.buildRecallQuery(bankID, req)
	if httpErr != nil {
		httpx.WriteErrorFor(w, r, httpErr)
		return
	}
	query = applyRecallTagScope(r, query)
	// With an embedding model configured, the query text is embedded so the
	// semantic arm can rank by vector similarity. A failed embedding degrades
	// to the lexical path rather than failing the read, because recall is an
	// enhancement surface and the non-vector arms still answer.
	if h.Embeddings != nil {
		if text := strings.TrimSpace(req.Query); text != "" {
			// The query is bounded before it reaches the embedder, one of the
			// sinks a degenerate query used to crash. The store bounds it again
			// before retrieval; doing it here keeps the two agreed.
			if limiter, ok := h.Store.(interface{ RecallQueryTokenLimit() int }); ok {
				text = storage.TruncateQueryToTokens(text, limiter.RecallQueryTokenLimit())
			}
			if vector, err := h.Embeddings.EmbedQuery(r.Context(), text); err == nil {
				query.QueryEmbedding = vector
			}
		}
	}
	// A bank may turn recall arms off in its own configuration; those arms are
	// merged with the process-level exclusions. The configuration is read fresh
	// rather than from the process-local bank cache, so a change another server
	// wrote takes effect on the next request. The tenant's overrides layer under
	// the bank's own, and the bank still wins any key it names.
	if bankConfig, err := h.Store.BankConfig(r.Context(), bankID); err == nil {
		resolved := config.ResolveLayers(bankConfig, httpx.TenantOverridesFrom(r), nil)
		bank := storage.Bank{Config: resolved}
		query.DisabledArms = append(query.DisabledArms, bank.RecallDisabledArms()...)
	}
	// The cross-encoder is an optional precision pass, so a bank that turned it
	// off is answered from the fused order rather than refused. The gate runs
	// here, after the bank's own configuration has been merged in, because the
	// bank is what turned it off.
	if disabledArm(query.DisabledArms, storage.ArmReranker) {
		query.Rerank = nil
		query.RerankDepth = 0
	}
	result, err := h.Store.Recall(r.Context(), query)
	if err != nil {
		h.writeReadError(w, r, err)
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BuildRecallResponse(result, req))
}

// buildRecallQuery resolves a validated request into the storage recall query,
// returning a bad-request HTTP error for a malformed query-time anchor or a
// rejected reranker-floor combination.
func (h *Handlers) buildRecallQuery(bankID string, req RecallRequest) (storage.RecallQuery, *httpx.HTTPError) {
	queryTimestamp, err := parseQueryTimestamp(req.QueryTimestamp)
	if err != nil {
		return storage.RecallQuery{}, httpx.NewHTTPError(httpx.KindBadRequest,
			"query_timestamp must be an ISO-8601 date string")
	}
	window, err := parseTemporalWindow(req.TemporalWindow)
	if err != nil {
		return storage.RecallQuery{}, httpx.NewHTTPError(httpx.KindBadRequest,
			"temporal_window bounds must be ISO-8601 date strings")
	}
	// An explicit window always wins: the caller who named a range has stated
	// what they mean, and a phrase in the question must not override it. With
	// no window, a relative phrase in the query supplies one, anchored to the
	// caller's reference time when they supplied one.
	if window == nil {
		reference := time.Now().UTC()
		if queryTimestamp != nil {
			reference = *queryTimestamp
		}
		window = temporalWindowFromQuery(req.Query, reference)
	}
	query := storage.RecallQuery{
		BankID:             bankID,
		Query:              req.Query,
		Types:              req.Types,
		PreferObservations: req.PreferObservations,
		Budget:             budgetOrDefault(req.Budget),
		MaxTokens:          derefInt(req.MaxTokens),
		MinScores:          req.MinScores,
		Tags:               req.Tags,
		TagsMatch:          tagsMatchOrDefault(req.TagsMatch),
		TagGroups:          toStorageTagGroups(req.TagGroups.Groups),
		Temporal:           window,
		QueryTimestamp:     queryTimestamp,
		StrategyBoosts:     h.RecallStrategyBoosts,
		Recency:            h.RecallRecency,
		DisabledArms:       h.DisabledRecallArms,
	}
	if h.Reranker != nil {
		query.Rerank = h.Reranker.Rerank
		query.RerankDepth = h.RerankDepth
		// A rank-position reranker echoes ranks rather than relevance, so the
		// fused order is what seeds the score instead of a min-max normalisation
		// of those echoes.
		query.RerankRankPosition = h.RerankerScoresByRankPosition
		query.RerankPrunesCandidates = h.RerankerPrunesCandidates
		query.RerankSeedFromFused = h.RerankerSeedFromFused
	}
	if req.Include != nil {
		// Entity states default to included: the include decoder leaves Entities
		// non-nil unless the caller explicitly nulls it, so an omitted key still
		// means include. Chunks and source facts stay excluded unless supplied.
		query.IncludeEntities = req.Include.Entities != nil
		query.IncludeChunks = req.Include.Chunks != nil
		query.IncludeSourceFacts = req.Include.SourceFacts != nil
	} else {
		query.IncludeEntities = true
	}
	return query, nil
}

// parseQueryTimestamp parses the optional query-time anchor, reading a naive
// value as UTC. An empty anchor is absent.
func parseQueryTimestamp(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	t, err := parseISODate(value)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// hasRerankerFloor reports whether the request sets a reranker score floor.
func hasRerankerFloor(minScores map[string]float64) bool {
	_, ok := minScores["reranker"]
	return ok
}

// derefInt returns the pointed-to int or zero when absent.
func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// budgetOrDefault returns the budget level, defaulting to mid.
func budgetOrDefault(budget string) string {
	if strings.TrimSpace(budget) == "" {
		return defaultBudgetLevel
	}
	return budget
}

// tagsMatchOrDefault returns the tag-matching mode, defaulting to any.
func tagsMatchOrDefault(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return defaultTagMatchMode
	}
	return mode
}

// disabledArm reports whether the named arm or pass is turned off.
func disabledArm(arms []string, target string) bool {
	for _, arm := range arms {
		if arm == target {
			return true
		}
	}
	return false
}

package memoryops

import (
	"net/http"
	"strings"
	"time"

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
	if err := decodeJSON(r, &req); err != nil {
		httpx.WriteValidationFor(w, r, bodyFailure())
		return
	}
	if failure := ValidateRecall(req); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	if h.RerankerScoresByRankPosition && hasRerankerFloor(req.MinScores) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"a reranker score floor cannot be combined with a rank-position reranker"))
		return
	}
	query, httpErr := h.buildRecallQuery(bankID, req)
	if httpErr != nil {
		httpx.WriteErrorFor(w, r, httpErr)
		return
	}
	result, err := h.Store.Recall(r.Context(), query)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapReadError(err))
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
		DisabledArms:       h.DisabledRecallArms,
		Limit:              req.Limit,
		Offset:             req.Offset,
	}
	if req.Include != nil {
		query.IncludeEntities = req.Include.Entities != nil
		query.IncludeChunks = req.Include.Chunks != nil
		query.IncludeSourceFacts = req.Include.SourceFacts != nil
	} else {
		// Entity states are included by default; chunks and source facts are
		// excluded unless their sub-object is supplied.
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

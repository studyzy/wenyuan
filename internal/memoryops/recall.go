package memoryops

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// defaultBudgetLevel is the recall budget applied when the request omits one.
const defaultBudgetLevel = storage.BudgetMid

// defaultTagMatchMode is the tag-matching mode applied when the request omits
// one.
const defaultTagMatchMode = storage.TagsMatchAny

// Include token-budget defaults. The reference declares each as a plain field
// default, so an omitted budget is bounded rather than unlimited: 4096 for the
// fact list, 500 for entity observations, 8192 for chunks, 4096 total and -1
// (unlimited) per observation for source facts.
const (
	defaultRecallMaxTokens                 = 4096
	defaultEntityMaxTokens                 = 500
	defaultChunkMaxTokens                  = 8192
	defaultSourceFactsMaxTokens            = 4096
	defaultSourceFactsPerObservationTokens = -1
)

// recallInclude is the resolved include configuration: whether each side
// channel is on and the token budget that bounds it.
type recallInclude struct {
	entities                 bool
	entityTokens             int
	chunks                   bool
	chunkTokens              int
	sourceFacts              bool
	sourceFactTokens         int
	sourceFactPerObservation int
}

// recallMaxTokens returns the request's fact token budget, defaulting an
// omitted one to the reference's 4096. An explicit non-positive budget means no
// facts, which the reference also returns for 0 or a negative value.
func recallMaxTokens(req RecallRequest) int {
	if req.MaxTokens != nil {
		return *req.MaxTokens
	}
	return defaultRecallMaxTokens
}

// resolveRecallInclude applies the reference's include defaults: the entities
// sub-object is on unless explicitly nulled, chunks and source facts are off
// unless supplied, and each channel's omitted token budget falls back to the
// reference's own default.
func resolveRecallInclude(include *IncludeRequest) recallInclude {
	resolved := recallInclude{
		entityTokens:             defaultEntityMaxTokens,
		chunkTokens:              defaultChunkMaxTokens,
		sourceFactTokens:         defaultSourceFactsMaxTokens,
		sourceFactPerObservation: defaultSourceFactsPerObservationTokens,
	}
	if include == nil {
		resolved.entities = true
		return resolved
	}
	resolved.entities = include.Entities != nil
	if include.Entities != nil && include.Entities.MaxTokens != nil {
		resolved.entityTokens = *include.Entities.MaxTokens
	}
	resolved.chunks = include.Chunks != nil
	if include.Chunks != nil && include.Chunks.MaxTokens != nil {
		resolved.chunkTokens = *include.Chunks.MaxTokens
	}
	resolved.sourceFacts = include.SourceFacts != nil
	if include.SourceFacts != nil {
		if include.SourceFacts.MaxTokens != nil {
			resolved.sourceFactTokens = *include.SourceFacts.MaxTokens
		}
		if include.SourceFacts.MaxTokensPerObservation != nil {
			resolved.sourceFactPerObservation = *include.SourceFacts.MaxTokensPerObservation
		}
	}
	return resolved
}

// Recall serves the memory recall route: a single synchronous JSON read with no
// streaming variant.
func (h *Handlers) Recall(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	// The query embedding and the rerank are model calls this run makes, so they
	// are attributed to the bank and the operation in the model-request trace —
	// without this a row lands with no bank and the per-bank trace listing never
	// shows it.
	r = r.WithContext(provider.WithTraceContext(r.Context(), provider.TraceContext{BankID: bankID, Operation: "recall"}))
	var req RecallRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		// A malformed tag-group filter gets its own message: the body is valid
		// JSON, so "not valid JSON" would send the caller looking in the wrong
		// place.
		if errors.Is(err, ErrMalformedTagGroup) {
			httpx.WriteValidationFailure(w, tagGroupFailure())
			return
		}
		httpx.WriteValidationFailure(w, recallDecodeFailure(r, &req, body, err))
		return
	}
	// The engine's fact-type selection differs from the request-model shape: an
	// absent or empty selection means every recallable type, "opinion" is a fact
	// type the engine dropped and filters out silently, and a selection left
	// empty by that filter answers an empty result. The filter runs before
	// validation so the dropped value is not reported as an unsupported one;
	// values outside the accepted set are refused afterwards, with the engine's
	// own plain detail rather than a field-level item.
	requestedTypes := req.Types
	keptTypes, invalidTypes := recallFactTypeSelection(requestedTypes)
	if len(requestedTypes) > 0 {
		req.Types = keptTypes
	}
	if failure := ValidateRecall(req, requestBodyObject(body)); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	if len(invalidTypes) > 0 {
		sort.Strings(invalidTypes)
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{
			Status: http.StatusUnprocessableEntity,
			Detail: "Invalid fact type(s): " + strings.Join(invalidTypes, ", ") +
				". Must be one of: experience, observation, world",
		})
		return
	}
	if len(requestedTypes) > 0 && len(keptTypes) == 0 {
		// Every requested type was "opinion", which the engine drops: the answer
		// is an empty result, produced without touching the bank.
		response := BuildRecallResponse(storage.RecallResult{}, req)
		response.Trace = nil
		httpx.WriteJSON(w, http.StatusOK, response)
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
		// The bank's own recall_prefer_observations is the middle layer: a
		// request that stated the member already decided, and the deployment's
		// value is the fallback this overrides.
		if preferred, ok := resolved["recall_prefer_observations"].(bool); ok {
			query.PreferObservations = req.PreferObservationsValue(preferred)
		}
	}
	// The cross-encoder is an optional precision pass, so a bank that turned it
	// off is answered from the fused order rather than refused. The gate runs
	// here, after the bank's own configuration has been merged in, because the
	// bank is what turned it off.
	if disabledArm(query.DisabledArms, storage.ArmReranker) {
		query.Rerank = nil
		query.RerankDepth = 0
	}
	// The retrieval and its response assembly are one observed operation: the
	// reference's block wraps both (api/http.py:6243-6367), so a request that
	// fails in the engine counts as a failure and an abandoned one does not
	// count at all. Validation has already run, so a refused request is never
	// recorded here — the reference refuses those before the block opens.
	budget, maxTokens := recallOperationLabels(req)
	var response RecallResponse
	err = serveOperation(r.Context(), operationRecall, budget, maxTokens, func() error {
		result, err := h.Store.Recall(r.Context(), query)
		if err != nil {
			return err
		}
		// A non-positive token budget asks for no facts (the documented way to
		// want chunks without them), and the reference's entity channel is
		// resolved from the returned facts — so with nothing to resolve it
		// reports null rather than the entities of a list that was cut away.
		if recallMaxTokens(req) <= 0 {
			result.EntityStates = nil
		}
		response = BuildRecallResponse(result, req)
		return nil
	})
	if err != nil {
		// An abandoned recall stops work and is answered 499 with the
		// reference's disconnect reason rather than a server fault.
		if httpx.WriteClientDisconnected(w, r) {
			return
		}
		h.writeReadError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

// buildRecallQuery resolves a validated request into the storage recall query,
// returning a bad-request HTTP error for a malformed query-time anchor or a
// rejected reranker-floor combination.
func (h *Handlers) buildRecallQuery(bankID string, req RecallRequest) (storage.RecallQuery, *httpx.HTTPError) {
	queryTimestamp, err := parseQueryTimestamp(req.QueryTimestamp)
	if err != nil {
		// The reference wraps the parser's own message, so a caller sees which
		// component of its value was refused.
		return storage.RecallQuery{}, httpx.NewHTTPError(httpx.KindBadRequest,
			"Invalid query_timestamp format. Expected ISO format (e.g., '2023-05-30T23:40:00'): "+err.Error())
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
	inc := resolveRecallInclude(req.Include)
	query := storage.RecallQuery{
		BankID:             bankID,
		Query:              req.Query,
		Types:              req.Types,
		PreferObservations: req.PreferObservationsValue(h.RecallPreferObservations),
		Budget:             budgetOrDefault(req.Budget),
		MaxTokens:          recallMaxTokens(req),
		MinScores:          req.MinScores,
		Tags:               req.Tags,
		TagsMatch:          tagsMatchOrDefault(req.TagsMatch),
		TagGroups:          toStorageTagGroups(req.TagGroups.Groups),
		Temporal:           window,
		QueryTimestamp:     queryTimestamp,
		StrategyBoosts:     h.RecallStrategyBoosts,
		Recency:            h.RecallRecency,
		DisabledArms:       h.DisabledRecallArms,
		IncludeEntities:    inc.entities,
		IncludeChunks:      inc.chunks,
		IncludeSourceFacts: inc.sourceFacts,
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
	return query, nil
}

// recallFactTypeSelection applies the engine's fact-type rules to a request
// selection: an absent or empty selection is left to the store's own default
// (every recallable type), "opinion" — removed upstream — is dropped, and the
// values outside the accepted set are collected separately so the caller can
// refuse them with the engine's own wording. The kept values preserve the
// caller's order; a selection that was non-empty and is left empty by the
// opinion filter means the answer is empty.
func recallFactTypeSelection(types []string) (kept, invalid []string) {
	if len(types) == 0 {
		return nil, nil
	}
	for _, factType := range types {
		switch {
		case factType == "opinion":
			continue
		case containsValue(acceptedFactTypes, factType):
			kept = append(kept, factType)
		default:
			invalid = append(invalid, factType)
		}
	}
	return kept, invalid
}

// parseQueryTimestamp parses the optional query-time anchor, reading a naive
// value as UTC. An empty anchor is absent.
func parseQueryTimestamp(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	t, err := parseStdlibDateTime(value)
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

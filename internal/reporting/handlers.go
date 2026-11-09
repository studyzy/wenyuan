package reporting

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// Handlers serves the reporting, memory sub-route, knowledge-base, observation
// and consolidation families.
type Handlers struct {
	Store storage.Store
	// TeamScope, when non-nil, resolves whether a bank-scoped read should be
	// served from a team's shared bank instead of the caller's own. A nil value
	// is a deployment without teams, where every read stays on its own bank.
	TeamScope TeamScopeResolver
	// APIVersion and Features back the versioned /v1/version route, which
	// reports the same payload as the root version endpoint.
	APIVersion string
	Features   map[string]bool
	// BankLLMHealthEnabled gates the per-bank /health/llm route. When false the
	// route reports not-found, matching the reference gate.
	BankLLMHealthEnabled bool
	// DryRunExtractDisabled turns the dry-run extraction route off, which the
	// reference spells WENYUAN_API_ENABLE_DRY_RUN_EXTRACT=false: the route
	// answers 404 and its sentence. It is the negative form so a handler set
	// built without wiring keeps the feature on, as the reference defaults it.
	DryRunExtractDisabled bool
	// Prober, when non-nil, is probed by the LLM health route. A nil prober
	// reports every operation as not configured.
	Prober LLMProber
	// Extraction, when non-nil, lets the dry-run extract route report the facts
	// a retain would produce without storing anything. A nil extractor reports
	// no facts.
	Extraction storage.FactExtraction
	// Embeddings, when non-nil, embeds a knowledge-search query so the storage
	// area can fuse its vector arm with the text arm. A nil generator leaves
	// knowledge search on its text arm alone, which is what a deployment with
	// no embedding model gets.
	Embeddings storage.EmbeddingGenerator
	// RetainExtractCausalLinks is the deployment's causal-extraction switch for
	// the retention previews. Nil means the reference's default, which extracts
	// causal relations.
	RetainExtractCausalLinks *bool
	// RetainOptionalFactDimensions is the deployment's switch for letting the
	// four descriptive fact dimensions be omitted, read by the retention
	// previews. A bank may not override it.
	RetainOptionalFactDimensions bool
	// LLMOutputLanguage is the deployment's forced output language. The previews
	// report it as the llm_output_language block, the same setting the request
	// path applies to every model call.
	LLMOutputLanguage string
	// ProcessDefaults is the process layer of a bank's configuration, used where
	// this area resolves a value the same way the bank-config read does (the
	// knowledge-page default trigger). Nil means the reference's shipped
	// defaults.
	ProcessDefaults map[string]any
}

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers { return &Handlers{Store: store} }

// BindRoutes registers every route in the family.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	// Observability.
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixStats), httpx.QueryParams(h.Stats, "refresh"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixStatsTimeseries), httpx.QueryParams(h.Timeseries, "period", "time_field"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixHealthLLM), h.HealthLLM)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAuditLogs), httpx.QueryParams(h.AuditLogs,
		"action", "transport", "start_date", "end_date", "limit", "offset"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAuditLogMember), h.AuditLog)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAuditLogsStats), httpx.QueryParams(h.AuditLogStats, "action", "period"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixLLMRequests), httpx.QueryParams(h.LLMRequests,
		"status", "operation", "scope", "call_type", "provider", "trace_id", "document_id", "memory_id", "group",
		"start_date", "end_date", "limit", "offset"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixLLMRequestsStats), httpx.QueryParams(h.LLMRequestStats,
		"operation", "call_type", "period"))

	// Observations and consolidation.
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixObservations), h.DeleteObservations)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixObservationScopes), httpx.QueryParams(h.ObservationScopes, "limit", "offset"))
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixMemoryObservations), h.ClearMemoryObservations)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixConsolidatePreview), h.ConsolidationPreview)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixConsolidateRecover), h.ConsolidationRecover)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixConsolidate), h.Consolidate)

	// Memory sub-routes.
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMemoriesDryRunExtract), h.DryRunExtract)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoriesList), httpx.QueryParams(h.ListMemories,
		"type", "q", "consolidation_state", "state", "document_id", "entity_id", "tags", "tags_match",
		"time_field", "start_date", "end_date", "limit", "offset", "scope"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoryMember), h.GetMemory)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixMemoryMember), h.UpdateMemory)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoryHistory), h.MemoryHistory)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixPromptsPreview), h.PromptPreview)

	// Knowledge base.
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgeTree), httpx.QueryParams(h.KnowledgeTree,
		"tags", "tags_match", "tag_groups"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixKnowledgeFolders), h.CreateKnowledgeFolder)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixKnowledgePages), h.CreateKnowledgePage)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgeExport), h.KnowledgeExport)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgeSearch), httpx.QueryParams(h.KnowledgeSearch,
		"q", "limit", "tags", "tags_match", "tag_groups"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgePageMember), h.KnowledgePage)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixKnowledgeNodeMember), h.UpdateKnowledgeNode)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixKnowledgeNodeMember), h.DeleteKnowledgeNode)

	// Contract remainder that reports rather than mutates.
	handle(http.MethodGet, httpx.SuffixBankTemplateSchema, h.BankTemplateSchema)
	handle(http.MethodGet, httpx.SuffixVersionV1, h.Version)
}

// Stats reports one bank's operational summary.
func (h *Handlers) Stats(w http.ResponseWriter, r *http.Request) {
	// Bank-wide read: a tag-scoped caller may not run it at all, and the
	// reference refuses before it even checks the bank exists.
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's statistics") {
		return
	}
	bankID, ok := h.requireBank(w, r, storage.PreferTeam)
	if !ok {
		return
	}
	var stats storage.BankStats
	var err error
	refresh, ok := strictBoolParam(w, r, "refresh", false)
	if !ok {
		return
	}
	if refresh {
		if refresher, ok := h.Store.(storage.BankStatsRefresher); ok {
			stats, err = refresher.BankStatsRefreshing(r.Context(), bankID, true)
		} else {
			stats, err = h.Store.BankStats(r.Context(), bankID)
		}
	} else {
		stats, err = h.Store.BankStats(r.Context(), bankID)
	}
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, BankStatsResponse{
		BankID: stats.BankID, TotalNodes: stats.TotalNodes, TotalLinks: stats.TotalLinks,
		TotalDocuments: stats.TotalDocuments, NodesByFactType: stats.NodesByFactType,
		LinksByLinkType: stats.LinksByLinkType, LinksByFactType: stats.LinksByFactType,
		LinksBreakdown: stats.LinksBreakdown, PendingOperations: stats.PendingOperations,
		FailedOperations: stats.FailedOperations, OperationsByStatus: stats.OperationsByStatus,
		LastConsolidatedAt:   httpx.FormatTimestampPtr(stats.LastConsolidatedAt),
		LastMemoryWriteAt:    httpx.FormatTimestampPtr(stats.LastMemoryWriteAt),
		PendingConsolidation: stats.PendingConsolidation,
		FailedConsolidation:  stats.FailedConsolidation,
		TotalObservations:    stats.TotalObservations,
	})
}

// Timeseries buckets a bank's memory counts.
func (h *Handlers) Timeseries(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r, storage.PreferTeam)
	if !ok {
		return
	}
	period := strings.TrimSpace(httpx.Query(r, "period"))
	timeField := storage.ResolveTimeseriesTimeField(strings.TrimSpace(httpx.Query(r, "time_field")))
	cfg := resolveTimeseriesPeriod(period)
	// The caller's resolved read scope narrows the counted set, so a confined
	// reader charts only the memories it may read. The reference resolves the
	// scope before the query and passes it as the tag groups
	// (engine/memory_engine.py:17046,17070).
	series, err := h.Store.MemoriesTimeseries(r.Context(), bankID, cfg.period, timeField, memoryops.ScopeFromRequest(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := padTimeseriesBuckets(series.Buckets, cfg, time.Now().UTC())
	httpx.WriteJSON(w, http.StatusOK, MemoriesTimeseriesResponse{
		BankID: bankID, Period: cfg.period, Trunc: cfg.trunc,
		TimeField: timeField, Buckets: buckets,
	})
}

// timeseriesPeriodConfig describes how one period slices the time axis.
type timeseriesPeriodConfig struct {
	period string
	trunc  string
	step   time.Duration
	count  int
}

// resolveTimeseriesPeriod maps a period name onto its bucket config. The window
// table lives in the storage layer, which is the one place both the query and
// the response take it from.
func resolveTimeseriesPeriod(period string) timeseriesPeriodConfig {
	window := storage.ResolveTimeseriesWindow(period)
	return timeseriesPeriodConfig{period: window.Period, trunc: window.Trunc, step: window.Step, count: window.Count}
}

// padTimeseriesBuckets rebuilds the store's buckets onto the canonical UTC grid
// for the period, zero-filling empty slots as the reference does.
func padTimeseriesBuckets(rows []storage.TimeseriesBucket, cfg timeseriesPeriodConfig, now time.Time) []TimeseriesBucket {
	end := truncateToWidth(now, cfg.trunc)
	byKey := make(map[string]*TimeseriesBucket, cfg.count)
	out := make([]TimeseriesBucket, 0, cfg.count)
	for i := 0; i < cfg.count; i++ {
		slot := end.Add(-cfg.step * time.Duration(cfg.count-1-i))
		out = append(out, TimeseriesBucket{Time: httpx.FormatTimestamp(slot)})
		byKey[out[len(out)-1].Time] = &out[len(out)-1]
	}
	for _, row := range rows {
		key := httpx.FormatTimestamp(truncateToWidth(row.Time, cfg.trunc))
		bucket, ok := byKey[key]
		if !ok {
			continue
		}
		bucket.World += row.World
		bucket.Experience += row.Experience
		bucket.Observation += row.Observation
	}
	return out
}

// truncateToWidth floors an instant to a minute/hour/day boundary in UTC.
func truncateToWidth(value time.Time, width string) time.Time {
	utc := value.UTC()
	switch width {
	case "minute":
		return utc.Truncate(time.Minute)
	case "hour":
		return utc.Truncate(time.Hour)
	default:
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	}
}

// LLMProber probes the model path for the bank health route. It is a narrow
// boundary so the reporting area does not import a provider.
type LLMProber interface {
	Probe(ctx context.Context) error
}

// llmHealthStatuses are the operation names the probe reports, matching the
// reference's per-operation roster.
var llmHealthOperations = []string{"retain", "consolidation", "reflect"}

// HealthLLM reports whether the bank's model path is usable, probing the
// provider once and reporting the same result per operation.
func (h *Handlers) HealthLLM(w http.ResponseWriter, r *http.Request) {
	// The disabled check comes first: it reads a process setting, so it is a
	// property of the deployment rather than of the bank, and the reference
	// answers it before the bank is looked up (api/http.py:6695-6699) -- an
	// unknown bank on a deployment with the check off is "disabled", not
	// "not found".
	if !h.BankLLMHealthEnabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
			"Bank LLM health check is disabled. Set WENYUAN_API_ENABLE_BANK_LLM_HEALTH=true to enable."))
		return
	}
	// The bank need not exist: the reference's check_bank_llm
	// (engine/memory_engine.py:16346-16392) probes the three deployment-level
	// LLM configurations and never calls _require_bank_exists, so an unknown
	// bank answers 200 with the same operation list a known one does. What the
	// probe reads is process configuration, not bank data. An empty bank id is
	// still the shared missing-bank 404.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	status, probedOK, latencyMS := h.probeLLM(r.Context())
	operations := make([]LLMHealthOperation, 0, len(llmHealthOperations))
	for _, name := range llmHealthOperations {
		operations = append(operations, LLMHealthOperation{
			Operation: name, OK: probedOK, Status: status, LatencyMS: latencyMS,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, BankLLMHealthResponse{BankID: bankID, Operations: operations})
}

// probeLLM runs the configured probe and classifies the result. A deployment
// with no prober reports every operation as not configured rather than failing
// the route.
func (h *Handlers) probeLLM(ctx context.Context) (status string, ok bool, latencyMS *float64) {
	if h.Prober == nil {
		return "not_configured", false, nil
	}
	start := time.Now()
	err := h.Prober.Probe(ctx)
	ms := roundMillis(time.Since(start))
	if err == nil {
		return "connected", true, &ms
	}
	return classifyProbeError(err), false, &ms
}

// classifyProbeError maps a probe failure onto the reference's status
// vocabulary.
func classifyProbeError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not available"), strings.Contains(message, "not_configured"),
		strings.Contains(message, "no provider"), strings.Contains(message, "none"):
		return "not_configured"
	case strings.Contains(message, "auth"), strings.Contains(message, "401"), strings.Contains(message, "403"),
		strings.Contains(message, "api key"), strings.Contains(message, "unauthorized"):
		return "auth_failed"
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "timeout"
	default:
		return "unreachable"
	}
}

// roundMillis rounds a duration to whole milliseconds as a float, matching the
// reference's latency field.
func roundMillis(d time.Duration) float64 {
	return float64(d.Milliseconds())
}

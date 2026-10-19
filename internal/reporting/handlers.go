package reporting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// Handlers serves the reporting, memory sub-route, knowledge-base, observation
// and consolidation families.
type Handlers struct {
	Store storage.Store
	// APIVersion and Features back the versioned /v1/version route, which
	// reports the same payload as the root version endpoint.
	APIVersion string
	Features   map[string]bool
	// BankLLMHealthEnabled gates the per-bank /health/llm route. When false the
	// route reports not-found, matching the reference gate.
	BankLLMHealthEnabled bool
	// DryRunExtractDisabled turns the dry-run extraction route off, which the
	// reference spells HINDSIGHT_API_ENABLE_DRY_RUN_EXTRACT=false: the route
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
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAuditLogsStats), httpx.QueryParams(h.AuditLogStats, "action", "period"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixLLMRequests), httpx.QueryParams(h.LLMRequests,
		"status", "operation", "scope", "provider", "trace_id", "document_id", "memory_id", "group",
		"start_date", "end_date", "limit", "offset"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixLLMRequestsStats), httpx.QueryParams(h.LLMRequestStats, "operation", "period"))

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
		"time_field", "start_date", "end_date", "limit", "offset"))
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
	bankID, ok := h.requireBank(w, r)
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
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	period := strings.TrimSpace(httpx.Query(r, "period"))
	timeField := storage.ResolveTimeseriesTimeField(strings.TrimSpace(httpx.Query(r, "time_field")))
	cfg := resolveTimeseriesPeriod(period)
	series, err := h.Store.MemoriesTimeseries(r.Context(), bankID, cfg.period, timeField)
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
			"Bank LLM health check is disabled. Set HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH=true to enable."))
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	status, ok, latencyMS := h.probeLLM(r.Context())
	operations := make([]LLMHealthOperation, 0, len(llmHealthOperations))
	for _, name := range llmHealthOperations {
		operations = append(operations, LLMHealthOperation{
			Operation: name, OK: ok, Status: status, LatencyMS: latencyMS,
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

// AuditLogs returns one page of the audit trail.
func (h *Handlers) AuditLogs(w http.ResponseWriter, r *http.Request) {
	// The reference validates the paging query before the handler runs.
	limit, ok := boundedIntParam(w, r, "limit", 50, 1, 500)
	if !ok {
		return
	}
	offset, ok := boundedIntParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	// The date parameters are bound in the reference's handler *signature*
	// (`_parse_iso_datetime`, api/http.py:10583-10586 and 10659-10662), which
	// FastAPI resolves before the body runs -- so a malformed date is answered
	// 400 before the bank is looked up, and an unknown bank with a bad date is a
	// 400 rather than a 404.
	startDate, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	endDate, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}

	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// The reference reports a missing bank before refusing a scoped caller.
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's audit log") {
		return
	}
	page, err := h.Store.ListAuditLog(r.Context(), storage.AuditQuery{
		BankID:    bankID,
		Action:    httpx.Query(r, "action"),
		Transport: httpx.Query(r, "transport"),
		StartDate: startDate,
		EndDate:   endDate,
		Limit:     limit, Offset: offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]AuditLogEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		startedAt := httpx.FormatTimestamp(entry.StartedAt)
		metadata := entry.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		item := AuditLogEntry{
			ID: entry.ID, Action: entry.Action, Transport: entry.Transport,
			BankID: nullableString(entry.BankID), StartedAt: &startedAt,
			EndedAt: httpx.FormatTimestampPtr(entry.EndedAt),
			Request: entry.Request, Response: entry.Response, Metadata: metadata,
		}
		if entry.EndedAt != nil {
			ms := int(entry.EndedAt.Sub(entry.StartedAt).Milliseconds())
			item.DurationMS = &ms
		}
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, AuditLogListResponse{
		BankID: bankID, Total: page.Total, Limit: limit, Offset: offset, Items: items,
	})
}

// AuditLogStats buckets the audit trail.
func (h *Handlers) AuditLogStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's audit log") {
		return
	}
	// The default applies only when the key is absent: `?period=` binds the
	// empty string, which is what the response echoes back.
	period := chartPeriod(r)
	stats, err := h.Store.AuditLogStats(r.Context(), bankID, period, httpx.Query(r, "action"))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := make([]AuditLogStatsBucket, 0, len(stats.Bucket))
	for _, bucket := range stats.Bucket {
		buckets = append(buckets, AuditLogStatsBucket{
			Time: httpx.FormatTimestamp(bucket.Time), Actions: bucket.Actions, Total: bucket.Total,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, AuditLogStatsResponse{
		BankID: stats.BankID, Period: stats.Period, Trunc: stats.Trunc,
		Start: httpx.FormatTimestamp(stats.Start), Buckets: buckets,
	})
}

// LLMRequests returns one page of the model-request trace.
func (h *Handlers) LLMRequests(w http.ResponseWriter, r *http.Request) {
	// The reference validates the paging query before the handler runs.
	limit, ok := boundedIntParam(w, r, "limit", 50, 1, 500)
	if !ok {
		return
	}
	offset, ok := boundedIntParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	// The date parameters are bound in the reference's handler *signature*
	// (`_parse_iso_datetime`, api/http.py:10583-10586 and 10659-10662), which
	// FastAPI resolves before the body runs -- so a malformed date is answered
	// 400 before the bank is looked up, and an unknown bank with a bad date is a
	// 400 rather than a 404.
	startDate, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	endDate, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}

	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// The reference reports a missing bank before refusing a scoped caller.
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's LLM requests") {
		return
	}
	params := r.URL.Query()
	_, documentIDSet := params["document_id"]
	_, memoryIDSet := params["memory_id"]
	group, ok := strictBoolParam(w, r, "group", false)
	if !ok {
		return
	}
	page, err := h.Store.ListLLMRequests(r.Context(), storage.LLMRequestQuery{
		BankID: bankID,
		// The reference treats these as truthy strings (`if value:`), so a
		// whitespace-only value filters on that literal value rather than being
		// dropped. Only document_id/memory_id are presence-gated, matching the
		// reference's `is not None` test, which makes an empty value a filter.
		Status:        httpx.Query(r, "status"),
		Operation:     httpx.Query(r, "operation"),
		Scope:         httpx.Query(r, "scope"),
		Provider:      httpx.Query(r, "provider"),
		TraceID:       httpx.Query(r, "trace_id"),
		DocumentID:    httpx.Query(r, "document_id"),
		DocumentIDSet: documentIDSet,
		MemoryID:      httpx.Query(r, "memory_id"),
		MemoryIDSet:   memoryIDSet,
		Group:         group,
		StartDate:     startDate,
		EndDate:       endDate,
		Limit:         limit, Offset: offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]LLMRequestEntry, 0, len(page.Requests))
	for _, request := range page.Requests {
		startedAt := httpx.FormatTimestamp(request.StartedAt)
		llmInfo := request.LLMInfo
		if llmInfo == nil {
			llmInfo = map[string]any{}
		}
		metadata := request.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		items = append(items, LLMRequestEntry{
			ID: request.ID, BankID: nullableString(request.BankID), Operation: nullableString(request.Operation),
			Scope: nullableString(request.Scope), TraceID: nullableString(request.TraceID),
			SpanID: nullableString(request.SpanID), ParentSpanID: nullableString(request.ParentSpanID),
			Provider: nullableString(request.Provider), Model: nullableString(request.Model),
			Status: request.Status, StartedAt: &startedAt, EndedAt: httpx.FormatTimestampPtr(request.EndedAt),
			DurationMS: request.DurationMS, InputTokens: request.InputTokens,
			OutputTokens: request.OutputTokens, CachedTokens: request.CachedTokens,
			ThoughtsTokens: request.ThoughtsTokens, TotalTokens: request.TotalTokens,
			Input: request.Input, Output: request.Output, Error: nullableString(request.Error),
			LLMInfo: llmInfo, Metadata: metadata,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, LLMRequestListResponse{
		BankID: bankID, Total: page.Total, Limit: limit, Offset: offset, Items: items,
	})
}

// LLMRequestStats buckets the model-request trace.
func (h *Handlers) LLMRequestStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's LLM requests") {
		return
	}
	period := chartPeriod(r)
	stats, err := h.Store.LLMRequestStats(r.Context(), bankID, period, httpx.Query(r, "operation"))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := make([]LLMRequestStatsBucket, 0, len(stats.Bucket))
	for _, bucket := range stats.Bucket {
		buckets = append(buckets, LLMRequestStatsBucket{
			Time: httpx.FormatTimestamp(bucket.Time), Statuses: bucket.Statuses, Total: bucket.Total,
			Tokens: LLMTokenSums{
				Input: bucket.Tokens.InputTokens, Output: bucket.Tokens.OutputTokens,
				Cached: bucket.Tokens.CachedTokens, Thoughts: bucket.Tokens.ThoughtsTokens,
				Total: bucket.Tokens.TotalTokens,
			},
		})
	}
	httpx.WriteJSON(w, http.StatusOK, LLMRequestStatsResponse{
		BankID: stats.BankID, Period: stats.Period, Trunc: stats.Trunc,
		Start: httpx.FormatTimestamp(stats.Start), Buckets: buckets,
	})
}

// DeleteObservations removes a bank's observations.
func (h *Handlers) DeleteObservations(w http.ResponseWriter, r *http.Request) {
	// A bank-wide clear reaches every observation regardless of tags, so a
	// tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "clear the bank's observations") {
		return
	}
	// The reference takes no bank-existence check on this route: an unknown bank
	// simply matches no rows and clears zero observations, so a delete against a
	// missing bank is a 200 with deleted_count 0 rather than a 404.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	removed, err := h.Store.DeleteObservations(r.Context(), bankID, "")
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// Clearing observations changes the counts the bank summary reports, so the
	// cached summary is dropped; the reference invalidates it here too, or a poll
	// in the next TTL window would serve observations that no longer exist. The
	// memory-scoped clear below deliberately does not, matching the reference.
	invalidateBankStats(r.Context(), h.Store, bankID)
	httpx.WriteJSON(w, http.StatusOK, DeleteResponse{
		Success: true, Message: fmt.Sprintf("Cleared %d observations", removed), DeletedCount: removed,
	})
}

// ObservationScopes lists a bank's observation scopes.
func (h *Handlers) ObservationScopes(w http.ResponseWriter, r *http.Request) {
	// The reference validates the query before the handler runs, so an
	// out-of-range limit/offset is a 422 even for an unknown bank.
	limit, ok := boundedIntParam(w, r, "limit", 100, 0, 1000)
	if !ok {
		return
	}
	offset, ok := boundedIntParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListObservationScopes(r.Context(), bankID, limit, offset)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	scopes := make([]ObservationScopeItem, 0, len(page.Scopes))
	for _, scope := range page.Scopes {
		tags := scope.Tags
		if tags == nil {
			tags = []string{}
		}
		scopes = append(scopes, ObservationScopeItem{Tags: tags, Count: scope.Count})
	}
	httpx.WriteJSON(w, http.StatusOK, ObservationScopesResponse{
		Scopes: scopes, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// ConsolidationPreview reports which of the bank's existing observation scopes
// each draft consolidation strategy would apply to.
func (h *Handlers) ConsolidationPreview(w http.ResponseWriter, r *http.Request) {
	// The reference validates the body before the handler body runs and the
	// bank existence check lives inside the engine call, so a malformed body is
	// rejected before an unknown bank is reported.
	body, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	httpx.RecordUnknownBodyFields(r, body, &consolidationScopesPreviewRequest{})
	if len(body) == 0 {
		writeRequiredBodyFailure(w, r, httpx.ErrEmptyBody)
		return
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, body))
		return
	}
	// A body that is the JSON literal null reads as an absent required body: the
	// framework reports the field missing rather than a value it cannot extract
	// fields from. A list or scalar body keeps the model_attributes_type item.
	if raw == nil {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body"}, nil),
		}})
		return
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			consolidationModelAttributesType([]string{"body"}, raw),
		}})
		return
	}
	req, failures := decodeConsolidationPreview(obj)
	if len(failures) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: failures})
		return
	}

	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	scopePage, err := h.Store.ListObservationScopes(r.Context(), bankID, 10000, 0)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	scopes := make([]scopePreview, 0, len(scopePage.Scopes))
	for _, scope := range scopePage.Scopes {
		tags := scope.Tags
		if tags == nil {
			tags = []string{}
		}
		scopes = append(scopes, scopePreview{Tags: tags, Count: scope.Count})
	}
	preview := buildConsolidationPreview(req.Strategies, scopes, req.SampleLimit, scopePage.Total <= len(scopes))
	httpx.WriteJSON(w, http.StatusOK, preview)
}

// ConsolidationRecover retries a bank's failed consolidations.
func (h *Handlers) ConsolidationRecover(w http.ResponseWriter, r *http.Request) {
	// A bank-wide consolidation retry reaches every memory regardless of tags,
	// so a tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "retry the bank's failed consolidation") {
		return
	}
	// The reference's retry is one UPDATE filtered by bank_id: it never checks
	// that the bank exists, so an unknown bank retries nothing and answers 200
	// with a zero count rather than 404ing.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	retried, err := h.Store.RecoverConsolidations(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, RecoverConsolidationResponse{RetriedCount: retried})
}

// Consolidate queues a consolidation pass for one bank.
func (h *Handlers) Consolidate(w http.ResponseWriter, r *http.Request) {
	// The reference validates the optional body before the handler runs, so a
	// malformed or wrong-shaped body is a 422 even for an unknown bank.
	scopes, scoped, ok := parseConsolidationRequest(w, r)
	if !ok {
		return
	}
	// A bank-wide consolidation reaches every memory regardless of tags, so a
	// tag-scoped caller may not run it at all.
	if httpx.RefuseBankWideIfScoped(w, r, "run consolidation on the bank") {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var op storage.Operation
	var deduplicated bool
	var err error
	if scoped {
		op, deduplicated, err = enqueueScopedConsolidation(r.Context(), h.Store, bankID, scopes)
	} else {
		op, deduplicated, err = h.Store.EnqueueConsolidation(r.Context(), bankID)
	}
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ConsolidationResponse{
		OperationID: op.OperationID, Deduplicated: deduplicated,
	})
}

// scopedConsolidationEnqueuer is the optional capability a store provides to
// submit a scoped consolidation whose scope list may be empty.
type scopedConsolidationEnqueuer interface {
	EnqueueConsolidationScoped(ctx context.Context, bankID string, observationScopes [][]string) (storage.Operation, bool, error)
}

// enqueueScopedConsolidation submits a scoped consolidation pass. A store that
// implements the scoped capability is asked directly, so an explicit but empty
// scope list stays a scoped (never deduplicated) run; a store without it falls
// back to the variadic enqueue with a non-nil list, which carries the scopes and
// likewise never deduplicates.
func enqueueScopedConsolidation(ctx context.Context, store storage.Store, bankID string, scopes [][]string) (storage.Operation, bool, error) {
	if enqueuer, ok := store.(scopedConsolidationEnqueuer); ok {
		return enqueuer.EnqueueConsolidationScoped(ctx, bankID, scopes)
	}
	if scopes == nil {
		scopes = [][]string{}
	}
	return store.EnqueueConsolidation(ctx, bankID, scopes...)
}

// invalidateBankStats drops a bank's cached summary through the optional
// capability a store provides, so the next stats read recomputes.
func invalidateBankStats(ctx context.Context, store storage.Store, bankID string) {
	if invalidator, ok := store.(interface {
		InvalidateBankStats(ctx context.Context, bankID string)
	}); ok {
		invalidator.InvalidateBankStats(ctx, bankID)
	}
}

// consolidationRequest is the consolidation route's optional body: the tag
// scopes to consolidate, each scope a list of tags. The declaration is the
// same one the served contract publishes, so the shared decoder's walk reports
// a rejected body with the reference's own items.
type consolidationRequest struct {
	ObservationScopes [][]string `json:"observation_scopes"`
}

// parseConsolidationRequest reads the optional consolidation body and returns
// its observation scopes together with whether the body was a scoped submit. An
// absent or null body yields no scopes and reports scoped=false (a full-bank
// pass); a body that names observation_scopes — even an empty list — reports
// scoped=true, since the reference treats a present list as a targeted run that
// must not deduplicate against a pending full-bank pass. A body the reference's
// model rejects is answered with that model's items through the shared decoder.
func parseConsolidationRequest(w http.ResponseWriter, r *http.Request) ([][]string, bool, bool) {
	body, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return nil, false, false
	}
	trimmed := strings.TrimSpace(string(body))
	// The declared body is optional and nullable, so an absent body and the
	// JSON literal null both mean "the whole bank" rather than a missing field.
	if trimmed == "" || trimmed == "null" {
		return nil, false, true
	}
	// The walk reports every value the model refuses — a body that is not an
	// object, a member that is not a list of lists of strings — with the
	// vocabulary, location and input the reference serves.
	if items := httpx.ValidationItemsAgainstRequest(r, &consolidationRequest{}, body); len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return nil, false, false
	}
	var req consolidationRequest
	if _, err := httpx.DecodeLax(body, &req); err != nil {
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, body))
		return nil, false, false
	}
	// A present list is a scoped submit whether or not it is empty: an empty
	// list is a targeted run matching no memory, which the reference submits
	// without deduplication, not a full-bank sweep. A null or absent member
	// decodes to a nil slice and stays a full-bank pass.
	return req.ObservationScopes, req.ObservationScopes != nil, true
}

// parseDateParam reads an optional ISO-8601 query parameter, writing a
// bad-request error and reporting false when the value is not a datetime. It
// mirrors the reference's _parse_iso_datetime: date-only and offset-less forms
// are accepted (a naive value is read as UTC), and an absent parameter is
// nothing while a present-but-empty one is a bad request.
func parseDateParam(w http.ResponseWriter, r *http.Request, key string) (*time.Time, bool) {
	if _, present := r.URL.Query()[key]; !present {
		return nil, true
	}
	raw := httpx.Query(r, key)
	parsed, err := parseISODateTime(raw)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid %s: '%s' is not an ISO-8601 datetime.", key, raw)))
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

// parseISODateTime parses the ISO-8601 forms Python's datetime.fromisoformat
// accepts: a date-only value, an offset-less datetime (read as UTC), fractional
// seconds, and either a trailing "Z" or a numeric offset. The space separator is
// taken too, which fromisoformat also admits.
func parseISODateTime(value string) (time.Time, error) {
	normalized := value
	if strings.HasSuffix(normalized, "Z") {
		normalized = normalized[:len(normalized)-1] + "+00:00"
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("value is not an ISO-8601 datetime")
}

// strictBoolParam reads a boolean query parameter the way the reference's
// framework coerces one: an absent parameter takes the default, the lax spelling
// set is accepted, and any other value is the framework's bool_parsing failure
// rather than a silent false. It reports whether it produced a value; on false
// the failure has been written.
func strictBoolParam(w http.ResponseWriter, r *http.Request, name string, def bool) (bool, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return def, true
	}
	// The reference binds the LAST occurrence of a repeated scalar parameter.
	raw := values[len(values)-1]
	switch strings.ToLower(raw) {
	case "true", "1", "yes", "on", "t", "y":
		return true, true
	case "false", "0", "no", "off", "f", "n":
		return false, true
	}
	httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.TypeMismatch("bool_parsing", []string{"query", name}, raw),
	}})
	return false, false
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// requireOrCreateBank resolves the bank, creating it lazily when absent — the
// reference's knowledge-node create paths do this, so a page can be the first
// thing a bank holds without the caller creating the bank beforehand.
func (h *Handlers) requireOrCreateBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return "", false
	}
	return bankID, true
}

// boundedIntParam reads an optional integer query parameter with an inclusive
// [min,max] bound (a negative max means unbounded above), defaulting when absent.
// A malformed, non-integer, or out-of-range value is rejected with the
// framework's 422 validation envelope before the handler body runs, matching how
// FastAPI validates a typed Query before the route function.
func boundedIntParam(w http.ResponseWriter, r *http.Request, name string, def, min, max int) (int, bool) {
	raw := httpx.Query(r, name)
	if raw == "" {
		return def, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("int_parsing", []string{"query", name}, raw),
		}})
		return 0, false
	}
	if value < min {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"query", name}, raw, min),
		}})
		return 0, false
	}
	if max >= 0 && value > max {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.LessThanEqual([]string{"query", name}, raw, max),
		}})
		return 0, false
	}
	return value, true
}

// readOptionalBody reads a request body with a size bound, returning the raw
// bytes (possibly empty) so a caller can tell an absent body from a present one.
func readOptionalBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("request body must be JSON: %w", err)
	}
	return body, nil
}

// writeRequiredBodyFailure emits the framework's 422 body for a missing or
// malformed required JSON body.
func writeRequiredBodyFailure(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, nil))
}

// writeQueryEnumFailure emits the framework's 422 body for a query parameter
// whose value is outside its declared enum. Both callers (tags_match,
// time_field) are declared as `Literal` in the reference, whose rejected-value
// item is `literal_error` rather than `enum`, so the kind is emitted as such.
func writeQueryEnumFailure(w http.ResponseWriter, r *http.Request, name, input string, allowed []string) {
	httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Literal([]string{"query", name}, input, allowed),
	}})
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case isHTTPError(err):
		var httpErr *httpx.HTTPError
		_ = errors.As(err, &httpErr)
		return httpErr
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrMemoryNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "memory not found")
	case errors.Is(err, storage.ErrKnowledgeNodeNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "knowledge node not found")
	case errors.Is(err, storage.ErrAuditEntryNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "audit entry not found")
	case errors.Is(err, storage.ErrMentalModelNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "mental model not found")
	case errors.Is(err, storage.ErrInvalidMentalModelSubtype):
		return httpx.NewHTTPError(httpx.KindBadRequest, "unknown mental model subtype")
	case errors.Is(err, storage.ErrEmbeddingDimensions):
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}

// isHTTPError reports whether the error already carries a status and detail.
func isHTTPError(err error) bool {
	var target *httpx.HTTPError
	return errors.As(err, &target)
}

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

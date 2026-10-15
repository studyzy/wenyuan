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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// defaultPageLimit is the page size used when the caller does not ask for one.
const defaultPageLimit = 50

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
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	stats, err := h.Store.BankStats(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BankStatsResponse{
		BankID: stats.BankID, TotalNodes: stats.TotalNodes, TotalLinks: stats.TotalLinks,
		TotalDocuments: stats.TotalDocuments, NodesByFactType: stats.NodesByFactType,
		LinksByLinkType: stats.LinksByLinkType, LinksByFactType: stats.LinksByFactType,
		LinksBreakdown: stats.LinksBreakdown, PendingOperations: stats.PendingOperations,
		FailedOperations: stats.FailedOperations, OperationsByStatus: stats.OperationsByStatus,
		LastConsolidatedAt:   formatTimePtr(stats.LastConsolidatedAt),
		LastMemoryWriteAt:    formatTimePtr(stats.LastMemoryWriteAt),
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
	httpx.WriteJSONFor(w, r, http.StatusOK, MemoriesTimeseriesResponse{
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
		out = append(out, TimeseriesBucket{Time: formatTime(slot)})
		byKey[out[len(out)-1].Time] = &out[len(out)-1]
	}
	for _, row := range rows {
		key := formatTime(truncateToWidth(row.Time, cfg.trunc))
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
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if !h.BankLLMHealthEnabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank LLM health is not enabled"))
		return
	}
	status, ok, latencyMS := h.probeLLM(r.Context())
	operations := make([]LLMHealthOperation, 0, len(llmHealthOperations))
	for _, name := range llmHealthOperations {
		operations = append(operations, LLMHealthOperation{
			Operation: name, OK: ok, Status: status, LatencyMS: latencyMS,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BankLLMHealthResponse{BankID: bankID, Operations: operations})
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
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	startDate, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	endDate, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}
	page, err := h.Store.ListAuditLog(r.Context(), storage.AuditQuery{
		BankID:    bankID,
		Action:    strings.TrimSpace(httpx.Query(r, "action")),
		Transport: strings.TrimSpace(httpx.Query(r, "transport")),
		StartDate: startDate,
		EndDate:   endDate,
		Limit:     pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]AuditLogEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		startedAt := formatTime(entry.StartedAt)
		metadata := entry.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		item := AuditLogEntry{
			ID: entry.ID, Action: entry.Action, Transport: entry.Transport,
			BankID: nullableString(entry.BankID), StartedAt: &startedAt,
			EndedAt: formatTimePtr(entry.EndedAt),
			Request: entry.Request, Response: entry.Response, Metadata: metadata,
		}
		if entry.EndedAt != nil {
			ms := int(entry.EndedAt.Sub(entry.StartedAt).Milliseconds())
			item.DurationMS = &ms
		}
		items = append(items, item)
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, AuditLogListResponse{
		BankID: bankID, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r), Items: items,
	})
}

// AuditLogStats buckets the audit trail.
func (h *Handlers) AuditLogStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	stats, err := h.Store.AuditLogStats(r.Context(), bankID,
		strings.TrimSpace(httpx.Query(r, "period")), strings.TrimSpace(httpx.Query(r, "action")))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := make([]AuditLogStatsBucket, 0, len(stats.Bucket))
	for _, bucket := range stats.Bucket {
		buckets = append(buckets, AuditLogStatsBucket{
			Time: formatTime(bucket.Time), Actions: bucket.Actions, Total: bucket.Total,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, AuditLogStatsResponse{
		BankID: stats.BankID, Period: stats.Period, Trunc: stats.Trunc,
		Start: formatTime(stats.Start), Buckets: buckets,
	})
}

// LLMRequests returns one page of the model-request trace.
func (h *Handlers) LLMRequests(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	startDate, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	endDate, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}
	page, err := h.Store.ListLLMRequests(r.Context(), storage.LLMRequestQuery{
		BankID:     bankID,
		Status:     strings.TrimSpace(httpx.Query(r, "status")),
		Operation:  strings.TrimSpace(httpx.Query(r, "operation")),
		Scope:      strings.TrimSpace(httpx.Query(r, "scope")),
		Provider:   strings.TrimSpace(httpx.Query(r, "provider")),
		TraceID:    strings.TrimSpace(httpx.Query(r, "trace_id")),
		DocumentID: strings.TrimSpace(httpx.Query(r, "document_id")),
		MemoryID:   strings.TrimSpace(httpx.Query(r, "memory_id")),
		Group:      boolQueryParam(httpx.Query(r, "group")),
		StartDate:  startDate,
		EndDate:    endDate,
		Limit:      pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]LLMRequestEntry, 0, len(page.Requests))
	for _, request := range page.Requests {
		startedAt := formatTime(request.StartedAt)
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
			Status: request.Status, StartedAt: &startedAt, EndedAt: formatTimePtr(request.EndedAt),
			DurationMS: request.DurationMS, InputTokens: request.InputTokens,
			OutputTokens: request.OutputTokens, CachedTokens: request.CachedTokens,
			ThoughtsTokens: request.ThoughtsTokens, TotalTokens: request.TotalTokens,
			Input: request.Input, Output: request.Output, Error: nullableString(request.Error),
			LLMInfo: llmInfo, Metadata: metadata,
		})
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, LLMRequestListResponse{
		BankID: bankID, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r), Items: items,
	})
}

// LLMRequestStats buckets the model-request trace.
func (h *Handlers) LLMRequestStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	stats, err := h.Store.LLMRequestStats(r.Context(), bankID,
		strings.TrimSpace(httpx.Query(r, "period")), strings.TrimSpace(httpx.Query(r, "operation")))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := make([]LLMRequestStatsBucket, 0, len(stats.Bucket))
	for _, bucket := range stats.Bucket {
		buckets = append(buckets, LLMRequestStatsBucket{
			Time: formatTime(bucket.Time), Statuses: bucket.Statuses, Total: bucket.Total,
			Tokens: LLMTokenSums{
				Input: bucket.Tokens.InputTokens, Output: bucket.Tokens.OutputTokens,
				Cached: bucket.Tokens.CachedTokens, Thoughts: bucket.Tokens.ThoughtsTokens,
				Total: bucket.Tokens.TotalTokens,
			},
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, LLMRequestStatsResponse{
		BankID: stats.BankID, Period: stats.Period, Trunc: stats.Trunc,
		Start: formatTime(stats.Start), Buckets: buckets,
	})
}

// DeleteObservations removes a bank's observations.
func (h *Handlers) DeleteObservations(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	removed, err := h.Store.DeleteObservations(r.Context(), bankID, "")
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DeleteResponse{
		Success: true, Message: "observations deleted", DeletedCount: removed,
	})
}

// ObservationScopes lists a bank's observation scopes.
func (h *Handlers) ObservationScopes(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListObservationScopes(r.Context(), bankID, pageLimit(r), pageOffset(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	scopes := make([]ObservationScopeItem, 0, len(page.Scopes))
	for _, scope := range page.Scopes {
		scopes = append(scopes, ObservationScopeItem{Tags: scopeTags(scope.Scope), Count: scope.Count})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ObservationScopesResponse{
		Scopes: scopes, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r),
	})
}

// scopeTags splits a stored scope string into the tag set it represents, sorted
// so [a,b] and [b,a] render identically. An empty scope is the global/untagged
// scope and yields an empty list.
func scopeTags(scope string) []string {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return []string{}
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// ClearMemoryObservations removes the observations derived from one memory.
func (h *Handlers) ClearMemoryObservations(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	removed, err := h.Store.DeleteObservations(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ClearMemoryObservationsResponse{DeletedCount: removed})
}

// ConsolidationPreview reports which of the bank's existing observation scopes
// each draft consolidation strategy would apply to.
func (h *Handlers) ConsolidationPreview(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body consolidationScopesPreviewRequest
	if err := decodeBody(r, &body); err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	sampleLimit := 5
	if body.SampleLimit != nil {
		sampleLimit = *body.SampleLimit
	}
	if sampleLimit < 0 {
		sampleLimit = 0
	}
	if sampleLimit > 50 {
		sampleLimit = 50
	}
	scopePage, err := h.Store.ListObservationScopes(r.Context(), bankID, 10000, 0)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	scopes := make([]scopePreview, 0, len(scopePage.Scopes))
	for _, scope := range scopePage.Scopes {
		scopes = append(scopes, scopePreview{Tags: scopeTags(scope.Scope), Count: scope.Count})
	}
	preview := buildConsolidationPreview(body.Strategies, scopes, sampleLimit, scopePage.Total <= len(scopes))
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, preview)
}

// ConsolidationRecover retries a bank's failed consolidations.
func (h *Handlers) ConsolidationRecover(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	retried, err := h.Store.RecoverConsolidations(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, RecoverConsolidationResponse{RetriedCount: retried})
}

// Consolidate queues a consolidation pass for one bank.
func (h *Handlers) Consolidate(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	op, deduplicated, err := h.Store.EnqueueConsolidation(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ConsolidationResponse{
		OperationID: op.OperationID, Deduplicated: deduplicated,
	})
}

// parseDateParam reads an optional RFC 3339 query parameter, writing a
// bad-request error and reporting false for a malformed value.
func parseDateParam(w http.ResponseWriter, r *http.Request, key string) (*time.Time, bool) {
	raw := strings.TrimSpace(httpx.Query(r, key))
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, key+" must be an RFC 3339 timestamp"))
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

// boolQueryParam reads a boolean query parameter, false for an absent or
// unrecognised value. The accepted spellings are the ones the reference's
// boolean coercion takes.
func boolQueryParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on", "t", "y":
		return true
	default:
		return false
	}
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

// memoryMember resolves the bank and the memory identifier.
func (h *Handlers) memoryMember(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return "", "", false
	}
	memoryID := strings.TrimSpace(r.PathValue("memory_id"))
	if memoryID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "memory not found"))
		return "", "", false
	}
	return bankID, memoryID, true
}

// pageLimit reads the page size, defaulting and clamping it.
func pageLimit(r *http.Request) int {
	raw := strings.TrimSpace(httpx.Query(r, "limit"))
	if raw == "" {
		return defaultPageLimit
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultPageLimit
	}
	if value > 500 {
		return 500
	}
	return value
}

// pageOffset reads the page offset.
func pageOffset(r *http.Request) int {
	raw := strings.TrimSpace(httpx.Query(r, "offset"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// decodeBody decodes a JSON body with a size bound.
func decodeBody(r *http.Request, target any) error {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	httpx.RecordUnknownBodyFields(r, body, target)
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
}

// writeRequiredBodyFailure emits the framework's 422 body for a missing or
// malformed required JSON body.
func writeRequiredBodyFailure(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteValidationFor(w, r, httpx.BodyDecodeFailure(err, nil))
}

// writeQueryEnumFailure emits the framework's 422 body for a query parameter
// whose value is outside its declared enum.
func writeQueryEnumFailure(w http.ResponseWriter, r *http.Request, name, input string, allowed []string) {
	httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Enum([]string{"query", name}, input, allowed),
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
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
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

package reporting

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
type Handlers struct{ Store storage.Store }

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers { return &Handlers{Store: store} }

// BindRoutes registers every route in the family.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	// Observability.
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixStats), h.Stats)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixStatsTimeseries), h.Timeseries)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixHealthLLM), h.HealthLLM)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAuditLogs), h.AuditLogs)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAuditLogsStats), h.AuditLogStats)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixLLMRequests), h.LLMRequests)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixLLMRequestsStats), h.LLMRequestStats)

	// Observations and consolidation.
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixObservations), h.DeleteObservations)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixObservationScopes), h.ObservationScopes)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixMemoryObservations), h.ClearMemoryObservations)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixConsolidatePreview), h.ConsolidationPreview)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixConsolidateRecover), h.ConsolidationRecover)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixConsolidate), h.Consolidate)

	// Memory sub-routes.
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoriesList), h.ListMemories)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoryMember), h.GetMemory)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixMemoryMember), h.UpdateMemory)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoryHistory), h.MemoryHistory)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixPromptsPreview), h.PromptPreview)

	// Knowledge base.
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgeTree), h.KnowledgeTree)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixKnowledgeFolders), h.CreateKnowledgeFolder)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixKnowledgePages), h.CreateKnowledgePage)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgeExport), h.KnowledgeExport)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixKnowledgeSearch), h.KnowledgeSearch)
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
		LinksByLinkType: stats.LinksByLinkType, PendingOperations: stats.PendingOperations,
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
	series, err := h.Store.MemoriesTimeseries(r.Context(), bankID,
		r.URL.Query().Get("period"), r.URL.Query().Get("time_field"))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := make([]TimeseriesBucket, 0, len(series.Buckets))
	for _, bucket := range series.Buckets {
		buckets = append(buckets, TimeseriesBucket{Time: formatTime(bucket.Time), Count: bucket.Count})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, MemoriesTimeseriesResponse{
		BankID: series.BankID, Period: series.Period, Trunc: series.Trunc,
		TimeField: series.TimeField, Buckets: buckets,
	})
}

// HealthLLM reports whether the bank's model path is usable.
//
// The check reports what it can observe: whether a provider is configured and
// whether the bank's recent model calls succeeded. It does not call a provider,
// because a health probe must not spend a model request.
func (h *Handlers) HealthLLM(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListLLMRequests(r.Context(), storage.LLMRequestQuery{BankID: bankID, Limit: 50})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	operations := []LLMHealthOperation{{
		Name:   "provider",
		Status: "ok",
		Detail: "no provider call is made by a health probe",
	}}
	if page.Total == 0 {
		operations = append(operations, LLMHealthOperation{
			Name: "recent_calls", Status: "unknown",
			Detail: "no model calls have been recorded for this bank",
		})
	} else {
		status := "ok"
		for _, request := range page.Requests {
			if request.Status == "failed" || request.Status == "error" {
				status = "degraded"
				break
			}
		}
		operations = append(operations, LLMHealthOperation{Name: "recent_calls", Status: status})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BankLLMHealthResponse{BankID: bankID, Operations: operations})
}

// AuditLogs returns one page of the audit trail.
func (h *Handlers) AuditLogs(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListAuditLog(r.Context(), storage.AuditQuery{
		BankID:    bankID,
		Action:    strings.TrimSpace(r.URL.Query().Get("action")),
		Transport: strings.TrimSpace(r.URL.Query().Get("transport")),
		Limit:     pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]AuditLogEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		item := AuditLogEntry{
			ID: entry.ID, Action: entry.Action, Transport: entry.Transport,
			BankID: entry.BankID, StartedAt: formatTime(entry.StartedAt),
			EndedAt: formatTimePtr(entry.EndedAt),
			Request: entry.Request, Response: entry.Response, Metadata: entry.Metadata,
		}
		if entry.EndedAt != nil {
			ms := int(entry.EndedAt.Sub(entry.StartedAt).Milliseconds())
			item.DurationMS = &ms
		}
		items = append(items, item)
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, AuditLogListResponse{
		BankID: bankID, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r), Items: items,
	})
}

// AuditLogStats buckets the audit trail.
func (h *Handlers) AuditLogStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	stats, err := h.Store.AuditLogStats(r.Context(), bankID, r.URL.Query().Get("period"))
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
	page, err := h.Store.ListLLMRequests(r.Context(), storage.LLMRequestQuery{
		BankID: bankID, Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Limit: pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]LLMRequestEntry, 0, len(page.Requests))
	for _, request := range page.Requests {
		items = append(items, LLMRequestEntry{
			ID: request.ID, BankID: request.BankID, Operation: request.Operation,
			Scope: request.Scope, TraceID: request.TraceID, SpanID: request.SpanID,
			ParentSpanID: request.ParentSpanID, Provider: request.Provider,
			Model: request.Model, Status: request.Status,
			StartedAt: formatTime(request.StartedAt), EndedAt: formatTimePtr(request.EndedAt),
			DurationMS: request.DurationMS, InputTokens: request.InputTokens,
			OutputTokens: request.OutputTokens, CachedTokens: request.CachedTokens,
			ThoughtsTokens: request.ThoughtsTokens, TotalTokens: request.TotalTokens,
			Input: request.Input, Output: request.Output, Error: request.Error,
			LLMInfo: request.LLMInfo, Metadata: request.Metadata,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, LLMRequestListResponse{
		BankID: bankID, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r), Items: items,
	})
}

// LLMRequestStats buckets the model-request trace.
func (h *Handlers) LLMRequestStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	stats, err := h.Store.LLMRequestStats(r.Context(), bankID, r.URL.Query().Get("period"))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	buckets := make([]LLMRequestStatsBucket, 0, len(stats.Bucket))
	for _, bucket := range stats.Bucket {
		buckets = append(buckets, LLMRequestStatsBucket{
			Time: formatTime(bucket.Time), Statuses: bucket.Statuses, Total: bucket.Total,
			Tokens: LLMTokenSums{
				InputTokens: bucket.Tokens.InputTokens, OutputTokens: bucket.Tokens.OutputTokens,
				CachedTokens: bucket.Tokens.CachedTokens, ThoughtsTokens: bucket.Tokens.ThoughtsTokens,
				TotalTokens: bucket.Tokens.TotalTokens,
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
		scopes = append(scopes, ObservationScopeItem{Scope: scope.Scope, Count: scope.Count})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ObservationScopesResponse{
		Scopes: scopes, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r),
	})
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

// ConsolidationPreview lists the strategies a consolidation could apply.
func (h *Handlers) ConsolidationPreview(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ConsolidationStrategiesPreview{
		BankID: bankID,
		Strategies: []ConsolidationStrategy{
			{Name: "observations", Description: "fold related memories into observations", Enabled: true},
			{Name: "deduplicate", Description: "merge duplicate memories", Enabled: true},
		},
	})
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
	httpx.WriteJSONFor(w, r, http.StatusAccepted, ConsolidationResponse{
		OperationID: op.ID, Deduplicated: deduplicated,
	})
}

// ListMemories returns one page of a bank's memory units.
func (h *Handlers) ListMemories(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListMemories(r.Context(), storage.MemoryQuery{
		BankID: bankID, Type: strings.TrimSpace(r.URL.Query().Get("type")),
		Limit: pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]MemoryItem, 0, len(page.Memories))
	for _, unit := range page.Memories {
		items = append(items, toMemoryItem(unit))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, MemoryListResponse{
		BankID: bankID, Items: items, Total: page.Total,
		Limit: pageLimit(r), Offset: pageOffset(r),
	})
}

// GetMemory returns one memory unit.
func (h *Handlers) GetMemory(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	item, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toMemoryItem(item))
}

// UpdateMemory applies a partial update to one memory unit.
func (h *Handlers) UpdateMemory(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	var body MemoryUpdateRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	item, err := h.Store.UpdateMemory(r.Context(), bankID, memoryID, storage.MemoryPatch{
		Text: body.Text, Tags: body.Tags, Metadata: body.Metadata,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toMemoryItem(item))
}

// MemoryHistory returns the recorded changes of one memory unit.
func (h *Handlers) MemoryHistory(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	entries, err := h.Store.MemoryHistory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	history := make([]MemoryHistoryEntry, 0, len(entries))
	for _, entry := range entries {
		history = append(history, MemoryHistoryEntry{
			ChangedAt: formatTime(entry.ChangedAt), Reason: entry.Reason,
			Text: entry.Text, Tags: entry.Tags,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, MemoryHistoryResponse{
		BankID: bankID, MemoryID: memoryID, History: history,
	})
}

// PromptPreview reports the prompt a retain or reflect run would use, without
// running it.
func (h *Handlers) PromptPreview(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body PromptPreviewRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	operation := strings.TrimSpace(body.Operation)
	if operation == "" {
		operation = "retain"
	}
	// The preview renders the operation's shape from its own inputs, so an
	// operator can inspect it without writing anything.
	httpx.WriteJSONFor(w, r, http.StatusOK, PromptPreviewResponse{
		BankID:    bankID,
		Operation: operation,
		Preview:   "operation=" + operation + " bank_id=" + bankID,
	})
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
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
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
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
	raw := strings.TrimSpace(r.URL.Query().Get("offset"))
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
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "bank not found")
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

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

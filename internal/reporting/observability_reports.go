package reporting

import (
	"errors"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// This file is the observability half of the reporting family: the audit trail
// and the model-request trace, both read-only reports over records the
// middleware and the engines already wrote.

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

	bankID, ok := h.requireBank(w, r, storage.PreferTeam)
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
	items := make([]AuditLogSummary, 0, len(page.Entries))
	for _, entry := range page.Entries {
		items = append(items, auditLogSummary(entry))
	}
	httpx.WriteJSON(w, http.StatusOK, AuditLogListResponse{
		BankID: bankID, Total: page.Total, Limit: limit, Offset: offset, Items: items,
	})
}

// auditLogSummary is the body-less part of an audit entry, the shape both the
// list and the single-entry fetch serve. duration_ms is computed from the two
// stamps and is null until the entry completed, as the reference computes it.
func auditLogSummary(entry storage.AuditEntry) AuditLogSummary {
	startedAt := httpx.FormatTimestamp(entry.StartedAt)
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	item := AuditLogSummary{
		ID: entry.ID, Action: entry.Action, Transport: entry.Transport,
		BankID: nullableString(entry.BankID), StartedAt: &startedAt,
		EndedAt: httpx.FormatTimestampPtr(entry.EndedAt), Metadata: metadata,
	}
	if entry.EndedAt != nil {
		ms := int(entry.EndedAt.Sub(entry.StartedAt).Milliseconds())
		item.DurationMS = &ms
	}
	return item
}

// AuditLog returns one audit entry with its full request and response bodies.
func (h *Handlers) AuditLog(w http.ResponseWriter, r *http.Request) {
	logID := pathID(r, "log_id")
	notFound := httpx.NewHTTPError(httpx.KindNotFound, "Audit log entry '"+logID+"' not found")
	// The reference resolves the entry identifier first and answers "not found"
	// for one that is not an identifier at all (memory_engine.py get_audit_log:
	// `uuid.UUID(log_id)`), then the bank profile, then the bank-wide refusal,
	// and only then the entry's own SELECT — so a tag-scoped caller is refused
	// whether or not the entry exists.
	if !uuidAnyForm(logID) {
		httpx.WriteErrorFor(w, r, notFound)
		return
	}
	// Past the identifier the reference looks the bank up; every None the engine
	// returns — the unknown bank's included — is that same entry 404.
	bankID := PathBankID(r)
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, notFound)
		return
	}
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's audit log") {
		return
	}
	entry, err := h.Store.GetAuditLog(r.Context(), bankID, logID)
	if err != nil {
		if errors.Is(err, storage.ErrAuditEntryNotFound) {
			httpx.WriteErrorFor(w, r, notFound)
			return
		}
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	request, response := entry.Request, entry.Response
	httpx.WriteJSON(w, http.StatusOK, AuditLogEntry{
		AuditLogSummary: auditLogSummary(entry),
		Request:         request, Response: response,
	})
}

// AuditLogStats buckets the audit trail.
func (h *Handlers) AuditLogStats(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r, storage.PreferTeam)
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
	// `call_type` is a `Literal` and `group` a `bool` in the reference's
	// signature (api/http.py:10952-10966), which the framework resolves before
	// the body runs — so a bad one is the 422 its type produces even when a
	// date parameter is garbage too.
	callType, ok := enumParam(w, r, "call_type", storage.LLMCallTypes())
	if !ok {
		return
	}
	group, ok := strictBoolParam(w, r, "group", false)
	if !ok {
		return
	}
	// The date parameters are the body's first act (`_parse_iso_datetime`,
	// api/http.py:10986-10987), after the framework's typed members but before
	// the store looks the bank up — so a malformed date is answered 400 before
	// the bank is looked up, and an unknown bank with a bad date is a 400
	// rather than a 404.
	startDate, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	endDate, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}

	bankID, ok := h.requireBank(w, r, storage.PreferTeam)
	if !ok {
		return
	}
	// The reference reports a missing bank before refusing a scoped caller.
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's LLM requests") {
		return
	}
	params := httpx.QueryParamsOf(r)
	_, documentIDSet := params["document_id"]
	_, memoryIDSet := params["memory_id"]
	page, err := h.Store.ListLLMRequests(r.Context(), storage.LLMRequestQuery{
		BankID: bankID,
		// The reference treats these as truthy strings (`if value:`), so a
		// whitespace-only value filters on that literal value rather than being
		// dropped. Only document_id/memory_id are presence-gated, matching the
		// reference's `is not None` test, which makes an empty value a filter.
		Status:        httpx.Query(r, "status"),
		Operation:     httpx.Query(r, "operation"),
		Scope:         httpx.Query(r, "scope"),
		CallType:      callType,
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
	callType, ok := enumParam(w, r, "call_type", storage.LLMCallTypes())
	if !ok {
		return
	}
	bankID, ok := h.requireBank(w, r, storage.PreferTeam)
	if !ok {
		return
	}
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's LLM requests") {
		return
	}
	period := chartPeriod(r)
	stats, err := h.Store.LLMRequestStats(r.Context(), bankID, period, httpx.Query(r, "operation"), callType)
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

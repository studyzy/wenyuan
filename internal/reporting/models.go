// Package reporting serves the observability, memory sub-route, knowledge-base,
// observation and consolidation route families. Field names on these types are
// public surface.
package reporting

// BankStatsResponse is the per-bank operational summary.
type BankStatsResponse struct {
	BankID               string                    `json:"bank_id"`
	TotalNodes           int                       `json:"total_nodes"`
	TotalLinks           int                       `json:"total_links"`
	TotalDocuments       int                       `json:"total_documents"`
	NodesByFactType      map[string]int            `json:"nodes_by_fact_type"`
	LinksByLinkType      map[string]int            `json:"links_by_link_type"`
	LinksByFactType      map[string]int            `json:"links_by_fact_type"`
	LinksBreakdown       map[string]map[string]int `json:"links_breakdown"`
	PendingOperations    int                       `json:"pending_operations"`
	FailedOperations     int                       `json:"failed_operations"`
	OperationsByStatus   map[string]int            `json:"operations_by_status"`
	LastConsolidatedAt   *string                   `json:"last_consolidated_at"`
	LastMemoryWriteAt    *string                   `json:"last_memory_write_at"`
	PendingConsolidation int                       `json:"pending_consolidation"`
	FailedConsolidation  int                       `json:"failed_consolidation"`
	TotalObservations    int                       `json:"total_observations"`
}

// LLMHealthOperation is one provider operation's health.
type LLMHealthOperation struct {
	Operation string   `json:"operation"`
	OK        bool     `json:"ok"`
	Status    string   `json:"status"`
	LatencyMS *float64 `json:"latency_ms"`
}

// BankLLMHealthResponse reports whether the bank's model path is usable.
type BankLLMHealthResponse struct {
	BankID     string               `json:"bank_id"`
	Operations []LLMHealthOperation `json:"operations"`
}

// TimeseriesBucket is one bucket of the memory-count histogram.
type TimeseriesBucket struct {
	Time        string `json:"time"`
	World       int    `json:"world"`
	Experience  int    `json:"experience"`
	Observation int    `json:"observation"`
}

// MemoriesTimeseriesResponse is the memory-count histogram.
type MemoriesTimeseriesResponse struct {
	BankID    string             `json:"bank_id"`
	Period    string             `json:"period"`
	Trunc     string             `json:"trunc"`
	TimeField string             `json:"time_field"`
	Buckets   []TimeseriesBucket `json:"buckets"`
}

// AuditLogEntry is one recorded API interaction.
type AuditLogEntry struct {
	ID         string         `json:"id"`
	Action     string         `json:"action"`
	Transport  string         `json:"transport"`
	BankID     *string        `json:"bank_id"`
	StartedAt  *string        `json:"started_at"`
	EndedAt    *string        `json:"ended_at"`
	DurationMS *int           `json:"duration_ms"`
	Request    map[string]any `json:"request"`
	Response   map[string]any `json:"response"`
	Metadata   map[string]any `json:"metadata"`
}

// AuditLogListResponse is the audit listing envelope.
type AuditLogListResponse struct {
	BankID string          `json:"bank_id"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
	Items  []AuditLogEntry `json:"items"`
}

// AuditLogStatsBucket is one bucket of the audit histogram.
type AuditLogStatsBucket struct {
	Time    string         `json:"time"`
	Actions map[string]int `json:"actions"`
	Total   int            `json:"total"`
}

// AuditLogStatsResponse is the audit histogram.
type AuditLogStatsResponse struct {
	BankID  string                `json:"bank_id"`
	Period  string                `json:"period"`
	Trunc   string                `json:"trunc"`
	Start   string                `json:"start"`
	Buckets []AuditLogStatsBucket `json:"buckets"`
}

// LLMRequestEntry is one recorded model call.
type LLMRequestEntry struct {
	ID             string         `json:"id"`
	BankID         *string        `json:"bank_id"`
	Operation      *string        `json:"operation"`
	Scope          *string        `json:"scope"`
	TraceID        *string        `json:"trace_id"`
	SpanID         *string        `json:"span_id"`
	ParentSpanID   *string        `json:"parent_span_id"`
	Provider       *string        `json:"provider"`
	Model          *string        `json:"model"`
	Status         string         `json:"status"`
	StartedAt      *string        `json:"started_at"`
	EndedAt        *string        `json:"ended_at"`
	DurationMS     *int           `json:"duration_ms"`
	InputTokens    *int           `json:"input_tokens"`
	OutputTokens   *int           `json:"output_tokens"`
	CachedTokens   *int           `json:"cached_tokens"`
	ThoughtsTokens *int           `json:"thoughts_tokens"`
	TotalTokens    *int           `json:"total_tokens"`
	Input          any            `json:"input"`
	Output         any            `json:"output"`
	Error          *string        `json:"error"`
	LLMInfo        map[string]any `json:"llm_info"`
	Metadata       map[string]any `json:"metadata"`
}

// LLMRequestListResponse is the model-request listing envelope.
type LLMRequestListResponse struct {
	BankID string            `json:"bank_id"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
	Items  []LLMRequestEntry `json:"items"`
}

// LLMTokenSums is the token accounting within one bucket.
type LLMTokenSums struct {
	Input    int `json:"input"`
	Output   int `json:"output"`
	Cached   int `json:"cached"`
	Thoughts int `json:"thoughts"`
	Total    int `json:"total"`
}

// LLMRequestStatsBucket is one bucket of the model-request histogram.
type LLMRequestStatsBucket struct {
	Time     string         `json:"time"`
	Statuses map[string]int `json:"statuses"`
	Total    int            `json:"total"`
	Tokens   LLMTokenSums   `json:"tokens"`
}

// LLMRequestStatsResponse is the model-request histogram.
type LLMRequestStatsResponse struct {
	BankID  string                  `json:"bank_id"`
	Period  string                  `json:"period"`
	Trunc   string                  `json:"trunc"`
	Start   string                  `json:"start"`
	Buckets []LLMRequestStatsBucket `json:"buckets"`
}

// DeleteResponse reports how many records a bulk delete removed.
type DeleteResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	DeletedCount int    `json:"deleted_count"`
}

// ObservationScopeItem is one observation scope.
type ObservationScopeItem struct {
	Tags  []string `json:"tags"`
	Count int      `json:"count"`
}

// ObservationScopesResponse is the observation-scope listing.
type ObservationScopesResponse struct {
	Scopes []ObservationScopeItem `json:"scopes"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

// ConsolidationResponse reports an accepted consolidation.
type ConsolidationResponse struct {
	OperationID  string `json:"operation_id"`
	Deduplicated bool   `json:"deduplicated"`
}

// RecoverConsolidationResponse reports how many consolidations were retried.
type RecoverConsolidationResponse struct {
	RetriedCount int `json:"retried_count"`
}

// StrategyScopePreview is one existing observation scope in a strategy preview.
type StrategyScopePreview struct {
	Tags []string `json:"tags"`
	// Count is the observations in this scope.
	Count int `json:"count"`
	// HandledBy is the index of the strategy that actually applies to this scope
	// (the first that claims it), or null when none does and Default applies.
	HandledBy *int `json:"handled_by"`
}

// StrategyRulePreview is what one rule of a strategy matches among existing scopes.
type StrategyRulePreview struct {
	MatchCount       int                    `json:"match_count"`
	TakenCount       int                    `json:"taken_count"`
	ObservationCount int                    `json:"observation_count"`
	Samples          []StrategyScopePreview `json:"samples"`
}

// StrategyPreview is the preview of one strategy, aligned by position with the
// request.
type StrategyPreview struct {
	Active       bool                  `json:"active"`
	ClaimedCount int                   `json:"claimed_count"`
	Rules        []StrategyRulePreview `json:"rules"`
}

// DefaultScopesPreview is the scopes no strategy claims.
type DefaultScopesPreview struct {
	MatchCount       int                    `json:"match_count"`
	ObservationCount int                    `json:"observation_count"`
	Samples          []StrategyScopePreview `json:"samples"`
}

// ConsolidationStrategiesPreview reports which existing observation scopes each
// consolidation strategy would apply to.
type ConsolidationStrategiesPreview struct {
	Strategies    []StrategyPreview    `json:"strategies"`
	Default       DefaultScopesPreview `json:"default"`
	ScopesScanned int                  `json:"scopes_scanned"`
	Complete      bool                 `json:"complete"`
}

// ClearMemoryObservationsResponse reports how many observations were removed.
type ClearMemoryObservationsResponse struct {
	DeletedCount int `json:"deleted_count"`
}

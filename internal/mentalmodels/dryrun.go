package mentalmodels

// DryRunResult is the preview a dry-run refresh returns: what a real refresh
// would do, having persisted nothing. Its field set matches the reference's
// MentalModelDryRunRefreshResult exactly.
type DryRunResult struct {
	MentalModelID      string            `json:"mental_model_id"`
	Name               string            `json:"name"`
	RequestedMode      string            `json:"requested_mode"`
	EffectiveMode      string            `json:"effective_mode"`
	ModeFallbackReason string            `json:"mode_fallback_reason,omitempty"`
	Outcome            string            `json:"outcome"`
	WouldPersist       bool              `json:"would_persist"`
	Scope              DryRunScope       `json:"scope"`
	Window             DryRunWindow      `json:"window"`
	Facts              DryRunFactCounts  `json:"facts"`
	BasedOn            map[string][]any  `json:"based_on"`
	CurrentContent     string            `json:"current_content"`
	CandidateContent   string            `json:"candidate_content"`
	PreviewContent     string            `json:"preview_content"`
	Diff               string            `json:"diff"`
	DeltaOperations    *DryRunDeltaOps   `json:"delta_operations,omitempty"`
	Retraction         *DryRunRetraction `json:"retraction,omitempty"`
	Trace              DryRunTrace       `json:"trace"`
	Usage              DryRunUsage       `json:"usage"`
	DurationMS         int               `json:"duration_ms"`
	Warnings           []string          `json:"warnings"`
}

// DryRunScope is the memory scope a refresh resolved to.
type DryRunScope struct {
	Tags                  []string `json:"tags,omitempty"`
	TagsMatch             string   `json:"tags_match"`
	TagGroups             []any    `json:"tag_groups,omitempty"`
	FactTypes             []string `json:"fact_types,omitempty"`
	ExcludeMentalModels   bool     `json:"exclude_mental_models"`
	ExcludeMentalModelIDs []string `json:"exclude_mental_model_ids"`
}

// DryRunWindow is the time window a refresh read memories from.
type DryRunWindow struct {
	CreatedAfter  *string `json:"created_after,omitempty"`
	CreatedBefore string  `json:"created_before"`
	Watermark     *string `json:"watermark,omitempty"`
}

// DryRunFactCounts counts facts retrieved versus actually used, by fact type.
type DryRunFactCounts struct {
	Retrieved map[string]int `json:"retrieved"`
	Used      map[string]int `json:"used"`
}

// DryRunDeltaOps is the structured operations a delta refresh emitted.
type DryRunDeltaOps struct {
	Applied []any `json:"applied"`
	Skipped []any `json:"skipped"`
}

// DryRunRetraction reports facts the document cited that no longer exist.
type DryRunRetraction struct {
	FactIDs        []string `json:"fact_ids"`
	FactTexts      []string `json:"fact_texts"`
	Applied        bool     `json:"applied"`
	DeferredReason string   `json:"deferred_reason,omitempty"`
}

// DryRunTrace is the execution trace of a refresh, always included on a dry run.
type DryRunTrace struct {
	RecordedAt         *string           `json:"recorded_at,omitempty"`
	EffectiveMode      string            `json:"effective_mode"`
	ModeFallbackReason string            `json:"mode_fallback_reason,omitempty"`
	Outcome            string            `json:"outcome"`
	ToolCalls          []DryRunToolCall  `json:"tool_calls"`
	LLMCalls           []DryRunLLMCall   `json:"llm_calls"`
	DeltaOperations    *DryRunDeltaOps   `json:"delta_operations,omitempty"`
	Retraction         *DryRunRetraction `json:"retraction,omitempty"`
	Usage              *DryRunUsage      `json:"usage,omitempty"`
	DurationMS         int               `json:"duration_ms"`
	Warnings           []string          `json:"warnings"`
}

// DryRunToolCall is one reflect tool call made during a refresh.
type DryRunToolCall struct {
	Tool        string         `json:"tool"`
	Reason      string         `json:"reason,omitempty"`
	Input       map[string]any `json:"input"`
	Output      map[string]any `json:"output,omitempty"`
	UpdatedAt   *string        `json:"updated_at,omitempty"`
	ResultCount *int           `json:"result_count,omitempty"`
	DurationMS  int            `json:"duration_ms"`
	Iteration   int            `json:"iteration"`
}

// DryRunLLMCall is one LLM call made during a refresh.
type DryRunLLMCall struct {
	Scope      string `json:"scope"`
	DurationMS int    `json:"duration_ms"`
}

// DryRunUsage is the token usage across a refresh's LLM calls.
type DryRunUsage struct {
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	TotalTokens    int `json:"total_tokens"`
	CachedTokens   int `json:"cached_tokens"`
	ThoughtsTokens int `json:"thoughts_tokens"`
}

package reporting

import (
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// MemoryItem is one memory unit as the listing renders it.
type MemoryItem struct {
	ID                    string         `json:"id"`
	Text                  string         `json:"text"`
	Context               string         `json:"context"`
	Date                  string         `json:"date"`
	FactType              *string        `json:"fact_type"`
	DocumentID            *string        `json:"document_id"`
	MentionedAt           *string        `json:"mentioned_at"`
	OccurredStart         *string        `json:"occurred_start"`
	OccurredEnd           *string        `json:"occurred_end"`
	Entities              string         `json:"entities"`
	ChunkID               *string        `json:"chunk_id"`
	ProofCount            int            `json:"proof_count"`
	Tags                  []string       `json:"tags"`
	Metadata              map[string]any `json:"metadata"`
	ConsolidatedAt        *string        `json:"consolidated_at"`
	ConsolidationFailedAt *string        `json:"consolidation_failed_at"`
	State                 string         `json:"state"`
	InvalidationReason    *string        `json:"invalidation_reason"`
	InvalidatedAt         *string        `json:"invalidated_at"`
	EditedAt              *string        `json:"edited_at"`
	UpdatedAt             *string        `json:"updated_at"`
	SourceMemoryIDs       []string       `json:"source_memory_ids"`
}

// MemoryListResponse is the memory listing envelope.
type MemoryListResponse struct {
	Items  []MemoryItem `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// MemoryUpdateRequest is the curation body upstream accepts.
type MemoryUpdateRequest struct {
	Text            *string   `json:"text"`
	Context         *string   `json:"context"`
	OccurredStart   *string   `json:"occurred_start"`
	OccurredEnd     *string   `json:"occurred_end"`
	FactType        *string   `json:"fact_type"`
	Entities        *[]string `json:"entities"`
	ResolveEntities *bool     `json:"resolve_entities"`
	State           *string   `json:"state"`
	Reason          *string   `json:"reason"`
}

// MemoryHistoryEntry is one recorded change to an observation.
type MemoryHistoryEntry struct {
	PreviousText          *string           `json:"previous_text"`
	PreviousTags          []string          `json:"previous_tags"`
	PreviousOccurredStart *string           `json:"previous_occurred_start"`
	PreviousOccurredEnd   *string           `json:"previous_occurred_end"`
	PreviousMentionedAt   *string           `json:"previous_mentioned_at"`
	ChangedAt             *string           `json:"changed_at"`
	NewSourceMemoryIDs    []string          `json:"new_source_memory_ids"`
	SourceFacts           []SourceFactModel `json:"source_facts"`
}

// SourceFactModel is one source fact behind a history entry.
type SourceFactModel struct {
	ID            string  `json:"id"`
	Text          *string `json:"text"`
	Type          *string `json:"type"`
	Context       *string `json:"context"`
	IsNew         bool    `json:"is_new"`
	OccurredStart *string `json:"occurred_start,omitempty"`
	MentionedAt   *string `json:"mentioned_at,omitempty"`
}

// PromptPreviewRequest is the prompt-preview body.
type PromptPreviewRequest struct {
	Operation string  `json:"operation"`
	Strategy  *string `json:"strategy"`
}

// PromptBlockModel is one block of a rendered message.
type PromptBlockModel struct {
	Text     string   `json:"text"`
	Source   string   `json:"source"`
	Field    string   `json:"field"`
	Section  string   `json:"section"`
	Heading  string   `json:"heading"`
	Active   bool     `json:"active"`
	Value    *string  `json:"value"`
	Kind     string   `json:"kind"`
	Choices  []string `json:"choices"`
	Editable bool     `json:"editable"`
}

// PromptPreviewMessage is one rendered prompt message.
type PromptPreviewMessage struct {
	Role   string             `json:"role"`
	Blocks []PromptBlockModel `json:"blocks"`
}

// RunSettingModel is one setting that shapes an operation without appearing in
// its prompt.
type RunSettingModel struct {
	Field    string  `json:"field"`
	Value    *string `json:"value"`
	Kind     string  `json:"kind"`
	Editable bool    `json:"editable"`
}

// PromptPreviewResponse reports the prompt an operation would use.
type PromptPreviewResponse struct {
	Messages       []PromptPreviewMessage `json:"messages"`
	Strategy       *string                `json:"strategy"`
	Strategies     []string               `json:"strategies"`
	RunSettings    []RunSettingModel      `json:"run_settings"`
	ResponseSchema any                    `json:"response_schema"`
	SkippedReason  *string                `json:"skipped_reason"`
}

// ExtractedFactAttachmentModel is one inline attachment a candidate fact was
// read from, in the shape the dry-run preview reports it.
type ExtractedFactAttachmentModel struct {
	BlockIndex int    `json:"block_index"`
	Type       string `json:"type"`
	MediaType  string `json:"media_type"`
}

// ExtractedFactModel is one candidate fact a dry-run extraction produced.
type ExtractedFactModel struct {
	FactType      string                         `json:"fact_type"`
	Text          string                         `json:"text"`
	OccurredStart *string                        `json:"occurred_start"`
	OccurredEnd   *string                        `json:"occurred_end"`
	Entities      []string                       `json:"entities"`
	ChunkIndex    *int                           `json:"chunk_index"`
	Attachments   []ExtractedFactAttachmentModel `json:"attachments"`
}

// ExtractionChunk is one chunk the extractor was handed and how much it yielded.
type ExtractionChunk struct {
	Text      string `json:"text"`
	FactCount int    `json:"fact_count"`
}

// TokenUsageModel is the token accounting a dry-run reports.
type TokenUsageModel struct {
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	TotalTokens    int `json:"total_tokens"`
	CachedTokens   int `json:"cached_tokens"`
	ThoughtsTokens int `json:"thoughts_tokens"`
}

// DryRunExtractionResult is the dry-run extraction payload.
type DryRunExtractionResult struct {
	Facts  []ExtractedFactModel `json:"facts"`
	Chunks []ExtractionChunk    `json:"chunks"`
	Usage  TokenUsageModel      `json:"usage"`
}

// ContentInput accepts upstream's content union: a plain string or an ordered
// list of content blocks. The blocks are kept whole — not just their text — so a
// dry run can canonicalize the same way retain does and attribute a candidate
// fact to the image it was read from.
type ContentInput struct {
	content memoryops.Content
}

// UnmarshalJSON accepts either shape.
func (c *ContentInput) UnmarshalJSON(raw []byte) error {
	return c.content.UnmarshalJSON(raw)
}

// Text returns the canonical text the input carries, with placeholders where
// attachments stood.
func (c ContentInput) Text() string { return c.content.Flatten() }

// Content returns the parsed content value, so a caller can decode its inline
// attachments alongside the canonical text.
func (c ContentInput) Content() memoryops.Content { return c.content }

// toMemoryItem renders a stored memory unit for the listing.
func toMemoryItem(unit storage.MemoryUnit) MemoryItem {
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
	}
	sources := unit.SourceMemoryIDs
	if sources == nil {
		sources = []string{}
	}
	metadata := unit.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	date := ""
	if unit.EventDate != nil {
		date = formatTime(*unit.EventDate)
	}
	var factType *string
	if unit.FactType != "" {
		value := string(unit.FactType)
		factType = &value
	}
	return MemoryItem{
		ID: unit.ID, Text: unit.Text, Context: unit.Context, Date: date,
		FactType: factType, DocumentID: nullableString(unit.DocumentID),
		ChunkID: nullableString(unit.ChunkID), Tags: tags, Metadata: metadata,
		OccurredStart:         formatTimePtr(unit.OccurredStart),
		OccurredEnd:           formatTimePtr(unit.OccurredEnd),
		MentionedAt:           formatTimePtr(unit.MentionedAt),
		ProofCount:            unit.ProofCount,
		ConsolidatedAt:        formatTimePtr(unit.ConsolidatedAt),
		ConsolidationFailedAt: formatTimePtr(unit.ConsolidationFailedAt),
		State:                 memoryState(unit),
		InvalidationReason:    unit.InvalidationReason,
		InvalidatedAt:         formatTimePtr(unit.InvalidatedAt),
		UpdatedAt:             formatTimePtr(unit.UpdatedAt),
		EditedAt:              formatTimePtr(unit.EditedAt),
		SourceMemoryIDs:       sources,
	}
}

// memoryState reports the unit's curation state from its invalidation record.
func memoryState(unit storage.MemoryUnit) string {
	if unit.InvalidatedAt != nil || unit.InvalidationReason != nil {
		return "invalidated"
	}
	return "valid"
}

// nullableString returns a pointer to a non-empty string, or nil.
func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

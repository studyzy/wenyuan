package reporting

import (
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// MemoryItem is one memory unit as the listing renders it.
type MemoryItem struct {
	ID            string         `json:"id"`
	BankID        string         `json:"bank_id"`
	Text          string         `json:"text"`
	Type          string         `json:"type"`
	Context       string         `json:"context,omitempty"`
	DocumentID    string         `json:"document_id,omitempty"`
	ChunkID       string         `json:"chunk_id,omitempty"`
	Tags          []string       `json:"tags"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	OccurredStart *string        `json:"occurred_start"`
	OccurredEnd   *string        `json:"occurred_end"`
	MentionedAt   *string        `json:"mentioned_at"`
	EventDate     *string        `json:"event_date"`
}

// MemoryListResponse is the memory listing envelope.
type MemoryListResponse struct {
	BankID string       `json:"bank_id"`
	Items  []MemoryItem `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// MemoryUpdateRequest is the partial-update body.
type MemoryUpdateRequest struct {
	Text     *string         `json:"text"`
	Tags     *[]string       `json:"tags"`
	Metadata *map[string]any `json:"metadata"`
}

// MemoryHistoryEntry is one recorded change to a memory unit.
type MemoryHistoryEntry struct {
	ChangedAt string   `json:"changed_at"`
	Reason    string   `json:"reason,omitempty"`
	Text      string   `json:"text,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// MemoryHistoryResponse is the memory history envelope.
type MemoryHistoryResponse struct {
	BankID   string               `json:"bank_id"`
	MemoryID string               `json:"memory_id"`
	History  []MemoryHistoryEntry `json:"history"`
}

// PromptPreviewRequest is the prompt-preview body.
type PromptPreviewRequest struct {
	Operation string         `json:"operation"`
	Inputs    map[string]any `json:"inputs"`
}

// PromptPreviewResponse reports the prompt an operation would use.
type PromptPreviewResponse struct {
	BankID    string `json:"bank_id"`
	Operation string `json:"operation"`
	Preview   string `json:"preview"`
}

// toMemoryItem renders a stored memory unit for the wire.
func toMemoryItem(unit storage.MemoryUnit) MemoryItem {
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
	}
	return MemoryItem{
		ID: unit.ID, BankID: unit.BankID, Text: unit.Text, Type: string(unit.FactType),
		Context: unit.Context, DocumentID: unit.DocumentID, ChunkID: unit.ChunkID,
		Tags: tags, Metadata: unit.Metadata,
		OccurredStart: formatTimePtr(unit.OccurredStart),
		OccurredEnd:   formatTimePtr(unit.OccurredEnd),
		MentionedAt:   formatTimePtr(unit.MentionedAt),
		EventDate:     formatTimePtr(unit.EventDate),
	}
}

// ensure the time import is used by the rendering helpers above.
var _ = time.Time{}

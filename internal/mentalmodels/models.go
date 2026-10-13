// Package mentalmodels serves the mental-model route family: named, reusable
// notes a reflection can be built from, including their refresh and history
// operations.
package mentalmodels

import (
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// Model is the wire shape of a mental model. Field names are public surface.
type Model struct {
	ID          string   `json:"id"`
	BankID      string   `json:"bank_id"`
	Subtype     string   `json:"subtype"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SourceQuery string   `json:"source_query,omitempty"`
	Content     string   `json:"content,omitempty"`
	Tags        []string `json:"tags"`
	MaxTokens   int      `json:"max_tokens"`
	// Observations carries the recorded observations; it is always present so a
	// client can rely on the key rather than on its absence.
	Observations  map[string]any `json:"observations"`
	LastUpdated   *time.Time     `json:"last_updated,omitempty"`
	LastRefreshed *time.Time     `json:"last_refreshed_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// CreateRequest is the create body.
type CreateRequest struct {
	Subtype     string   `json:"subtype"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SourceQuery string   `json:"source_query"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	MaxTokens   int      `json:"max_tokens"`
}

// UpdateRequest is the partial-update body. A nil field is left unchanged.
type UpdateRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	SourceQuery *string   `json:"source_query"`
	Content     *string   `json:"content"`
	Tags        *[]string `json:"tags"`
	MaxTokens   *int      `json:"max_tokens"`
}

// RefreshResult is the observable result of a refresh. A dry run reports the
// same shape without applying the change.
type RefreshResult struct {
	Model     Model  `json:"mental_model"`
	Refreshed bool   `json:"refreshed"`
	DryRun    bool   `json:"dry_run"`
	Reason    string `json:"reason,omitempty"`
}

// HistoryEntry is one recorded refresh.
type HistoryEntry struct {
	RefreshedAt time.Time `json:"refreshed_at"`
	SourceQuery string    `json:"source_query,omitempty"`
	Content     string    `json:"content,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// HistoryResponse is the history envelope.
type HistoryResponse struct {
	BankID      string         `json:"bank_id"`
	MentalModel string         `json:"mental_model_id"`
	Entries     []HistoryEntry `json:"history"`
}

// ListResponse is the collection envelope.
type ListResponse struct {
	BankID string  `json:"bank_id"`
	Models []Model `json:"mental_models"`
}

// toModel renders a stored record for the wire.
func toModel(record storage.MentalModel) Model {
	observations := record.Observations
	if observations == nil {
		observations = map[string]any{"observations": []any{}}
	}
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	model := Model{
		ID:           record.ID,
		BankID:       record.BankID,
		Subtype:      string(record.Subtype),
		Name:         record.Name,
		Description:  record.Description,
		SourceQuery:  record.SourceQuery,
		Content:      record.Content,
		Tags:         tags,
		MaxTokens:    record.MaxTokens,
		Observations: observations,
		CreatedAt:    record.CreatedAt,
	}
	if !record.LastUpdated.IsZero() {
		updated := record.LastUpdated
		model.LastUpdated = &updated
	}
	if !record.LastRefreshedAt.IsZero() {
		refreshed := record.LastRefreshedAt
		model.LastRefreshed = &refreshed
	}
	return model
}

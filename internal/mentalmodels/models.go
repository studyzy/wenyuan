// Package mentalmodels serves the mental-model route family: named, reusable
// notes a reflection can be built from, including their refresh and history
// operations.
package mentalmodels

import (
	"encoding/json"
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
	// Trigger is the model's refresh trigger, exposed so a client can read the
	// schedule it was created with.
	Trigger map[string]any `json:"trigger,omitempty"`
	// Observations carries the recorded observations; it is always present so a
	// client can rely on the key rather than on its absence.
	Observations map[string]any `json:"observations"`
	// ReflectResponse is the stored reflect payload, exposed at detail=full.
	ReflectResponse map[string]any `json:"reflect_response,omitempty"`
	// LastMemorySeenAt is the bank write watermark the last refresh observed.
	LastMemorySeenAt *time.Time `json:"last_memory_seen_at,omitempty"`
	// LastRefreshFailedAt parks an automatic refresh after a failure.
	LastRefreshFailedAt *time.Time `json:"last_refresh_failed_at,omitempty"`
	// IsStale reports whether a write inside the model's scope is newer than
	// the last refresh observed.
	IsStale       bool       `json:"is_stale"`
	LastUpdated   *time.Time `json:"last_updated,omitempty"`
	LastRefreshed *time.Time `json:"last_refreshed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// CreateRequest is the create body.
type CreateRequest struct {
	// ID is an optional client-supplied identifier, validated for shape and
	// rejected with a conflict when it already names a model in the bank.
	ID          string   `json:"id"`
	Subtype     string   `json:"subtype"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SourceQuery string   `json:"source_query"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	MaxTokens   int      `json:"max_tokens"`
	// Trigger carries the refresh settings. It is decoded raw so the handler can
	// distinguish an absent trigger from an explicit one and validate its members.
	Trigger json.RawMessage `json:"trigger"`
}

// UpdateRequest is the partial-update body. A nil field is left unchanged.
type UpdateRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	SourceQuery *string   `json:"source_query"`
	Content     *string   `json:"content"`
	Tags        *[]string `json:"tags"`
	MaxTokens   *int      `json:"max_tokens"`
	// Trigger carries refresh settings to patch over the model's current ones.
	// Only the members the client actually sent are applied.
	Trigger json.RawMessage `json:"trigger"`
}

// SubmitResponse is the accepted-operation acknowledgement a refresh returns.
// It carries exactly the fields the reference's AsyncOperationSubmitResponse
// declares, so a caller can poll the named operation.
type SubmitResponse struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
}

// HistoryEntry is one recorded refresh. The reference stores either a version
// snapshot, carrying the content and reflect response that were replaced, or a
// failure record for a refresh that refused to write.
type HistoryEntry struct {
	// PreviousContent is the content the refresh replaced; null for a failure
	// record, which produced no version.
	PreviousContent *string `json:"previous_content"`
	// PreviousReflectResponse is the reflect response the refresh replaced.
	PreviousReflectResponse map[string]any `json:"previous_reflect_response"`
	// ChangedAt is when the entry was recorded.
	ChangedAt time.Time `json:"changed_at"`
	// Kind marks a failure record; absent on a version snapshot.
	Kind string `json:"kind,omitempty"`
	// Outcome, FailureReason and ErrorMessage describe a refused write.
	Outcome       string `json:"outcome,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// ListResponse is the collection envelope.
type ListResponse struct {
	Items  []Model `json:"items"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// CreateResponse is the create acknowledgement. Generation runs in the
// background, so the caller polls the operation.
type CreateResponse struct {
	MentalModelID string `json:"mental_model_id"`
	OperationID   string `json:"operation_id,omitempty"`
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
		ID:                  record.ID,
		BankID:              record.BankID,
		Subtype:             string(record.Subtype),
		Name:                record.Name,
		Description:         record.Description,
		SourceQuery:         record.SourceQuery,
		Content:             record.Content,
		Tags:                tags,
		MaxTokens:           record.MaxTokens,
		Trigger:             record.Trigger,
		Observations:        observations,
		ReflectResponse:     record.ReflectResult,
		LastMemorySeenAt:    record.LastMemorySeenAt,
		LastRefreshFailedAt: record.LastRefreshFailedAt,
		CreatedAt:           record.CreatedAt,
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

// applyDetail trims a model to the requested detail level. "metadata" keeps
// names, tags, staleness and timestamps; "content" adds the stored content and
// configuration; "full" keeps everything, including the reflect response.
func applyDetail(model *Model, detail string) {
	switch detail {
	case "full":
		return
	case "content":
		model.ReflectResponse = nil
	default: // metadata
		model.Content = ""
		model.Observations = nil
		model.ReflectResponse = nil
	}
}

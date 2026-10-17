// Package mentalmodels serves the mental-model route family: named, reusable
// notes a reflection can be built from, including their refresh and history
// operations.
package mentalmodels

import (
	"encoding/json"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Model is the wire shape of a mental model. It declares exactly the fourteen
// fields the reference's MentalModelResponse carries, always present: a field
// the reference leaves unset serializes as JSON null rather than being dropped.
// The detail level decides which of the nullable members are populated.
type Model struct {
	ID          string  `json:"id"`
	BankID      string  `json:"bank_id"`
	Name        string  `json:"name"`
	SourceQuery *string `json:"source_query"`
	// Content is null at metadata detail and the stored (possibly empty) markdown
	// at content/full, matching the reference's opt-in content projection.
	Content   *string  `json:"content"`
	Tags      []string `json:"tags"`
	MaxTokens *int     `json:"max_tokens"`
	// Trigger is the model's refresh trigger, null at metadata detail and the
	// normalized object otherwise.
	Trigger map[string]any `json:"trigger"`
	// LastRefreshedAt is when a refresh last finished.
	LastRefreshedAt *string `json:"last_refreshed_at"`
	// LastMemorySeenAt is the bank write watermark the last refresh observed.
	LastMemorySeenAt *string `json:"last_memory_seen_at"`
	// LastRefreshFailedAt parks an automatic refresh after a failure.
	LastRefreshFailedAt *string `json:"last_refresh_failed_at"`
	CreatedAt           string  `json:"created_at"`
	// ReflectResponse is the stored reflect payload, populated only at full.
	ReflectResponse map[string]any `json:"reflect_response"`
	// IsStale reports whether a write inside the model's scope is newer than
	// the last refresh observed. Null when the route did not compute it (a
	// single read below full detail).
	IsStale *bool `json:"is_stale"`
}

// CreateRequest is the create body. It declares exactly the reference's
// CreateMentalModelRequest fields: id, name, source_query, tags, max_tokens and
// trigger. Any other key a client sends is reported through the ignored-params
// channel, never honoured.
type CreateRequest struct {
	ID          *string         `json:"id"`
	Name        *string         `json:"name"`
	SourceQuery *string         `json:"source_query"`
	Tags        *[]string       `json:"tags"`
	MaxTokens   *int            `json:"max_tokens"`
	Trigger     json.RawMessage `json:"trigger"`
}

// UpdateRequest is the partial-update body. It declares exactly the reference's
// UpdateMentalModelRequest fields: name, source_query, max_tokens, tags and
// trigger. A nil field is left unchanged.
type UpdateRequest struct {
	Name        *string         `json:"name"`
	SourceQuery *string         `json:"source_query"`
	MaxTokens   *int            `json:"max_tokens"`
	Tags        *[]string       `json:"tags"`
	Trigger     json.RawMessage `json:"trigger"`
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
	ChangedAt string `json:"changed_at"`
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
// background, so the caller polls the operation, which is always queued.
type CreateResponse struct {
	MentalModelID string `json:"mental_model_id"`
	OperationID   string `json:"operation_id"`
}

// toModel renders a stored record for the wire at full detail; the caller
// trims it with applyDetail.
func toModel(record storage.MentalModel) Model {
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	sourceQuery := record.SourceQuery
	content := record.Content
	var maxTokens *int
	if record.MaxTokens > 0 {
		value := record.MaxTokens
		maxTokens = &value
	}
	return Model{
		ID:                  record.ID,
		BankID:              record.BankID,
		Name:                record.Name,
		SourceQuery:         &sourceQuery,
		Content:             &content,
		Tags:                tags,
		MaxTokens:           maxTokens,
		Trigger:             normalizeTrigger(record.Trigger),
		LastRefreshedAt:     timestampPtr(record.LastRefreshedAt),
		LastMemorySeenAt:    httpx.FormatTimestampPtr(record.LastMemorySeenAt),
		LastRefreshFailedAt: httpx.FormatTimestampPtr(record.LastRefreshFailedAt),
		CreatedAt:           httpx.FormatTimestamp(record.CreatedAt),
		ReflectResponse:     record.ReflectResult,
	}
}

// timestampPtr renders a time as the reference's isoformat, or nil when it is
// the zero value so the field serializes as JSON null.
func timestampPtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	rendered := httpx.FormatTimestamp(t)
	return &rendered
}

// applyDetail trims a model to the requested detail level, mirroring the
// reference's _row_to_mental_model projection:
//
//   - metadata: names, tags, staleness and timestamps only; content, source
//     query, max_tokens, trigger and reflect_response are null.
//   - content: adds content and configuration (source_query, max_tokens,
//     trigger); reflect_response stays null.
//   - full: keeps everything, including reflect_response.
func applyDetail(model *Model, detail string) {
	switch detail {
	case "full":
		return
	case "content":
		model.ReflectResponse = nil
	default: // metadata
		model.SourceQuery = nil
		model.Content = nil
		model.MaxTokens = nil
		model.Trigger = nil
		model.ReflectResponse = nil
	}
}

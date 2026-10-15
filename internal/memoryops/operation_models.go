package memoryops

import (
	"time"
)

// RetainResponse is the write acceptance response. Every field name is public
// surface.
type RetainResponse struct {
	Success    bool   `json:"success"`
	BankID     string `json:"bank_id"`
	ItemsCount int    `json:"items_count"`
	Async      bool   `json:"async"`
	// Usage is null for an asynchronous write, whose accounting is not known
	// until the queued operation completes.
	Usage        *Usage   `json:"usage"`
	OperationID  string   `json:"operation_id,omitempty"`
	OperationIDs []string `json:"operation_ids,omitempty"`
	// ProcessedContentTokens is the number of content tokens the engine
	// accounted for. It is omitted when the path does not report one, so a
	// client never reads a fabricated zero as a real measurement.
	ProcessedContentTokens *int `json:"processed_content_tokens,omitempty"`
}

// Operation is one list entry, matching the reference's OperationResponse.
// Every field name is public surface. The model keeps null fields (it is in the
// keep-nulls policy of the response contract), so the nullable members are
// pointers without omitempty and serialize as null rather than disappearing.
type Operation struct {
	ID            string  `json:"id"`
	TaskType      string  `json:"task_type"`
	OperationID   *string `json:"operation_id"`
	OperationType *string `json:"operation_type"`
	ItemsCount    int     `json:"items_count"`
	// DocumentID, Filename and MentalModelID name the subject the operation
	// acted on, when the operation type has one.
	DocumentID    *string `json:"document_id"`
	Filename      *string `json:"filename"`
	MentalModelID *string `json:"mental_model_id"`
	// Details is the typed per-operation-type outcome detail; null for the
	// operation types that report none.
	Details      map[string]any `json:"details"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    *time.Time     `json:"updated_at"`
	Status       string         `json:"status"`
	ErrorMessage *string        `json:"error_message"`
	RetryCount   int            `json:"retry_count"`
	NextRetryAt  *time.Time     `json:"next_retry_at"`
	Progress     map[string]any `json:"progress"`
}

// ChildOperationStatus is one child entry of a parent batch operation,
// matching the reference's ChildOperationStatus.
type ChildOperationStatus struct {
	OperationID   string `json:"operation_id"`
	Status        string `json:"status"`
	SubBatchIndex int    `json:"sub_batch_index"`
	ItemsCount    int    `json:"items_count"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// OperationStatus is the single-operation read, matching the reference's
// OperationStatusResponse. Unlike the list entry it carries result_metadata, the
// child roll-up and the raw task payload. The model drops null fields (it is in
// the drop-nulls policy), so the nullable members are omitempty.
type OperationStatus struct {
	OperationID     string                 `json:"operation_id"`
	Status          string                 `json:"status"`
	OperationType   *string                `json:"operation_type,omitempty"`
	ID              *string                `json:"id,omitempty"`
	TaskType        *string                `json:"task_type,omitempty"`
	MentalModelID   *string                `json:"mental_model_id,omitempty"`
	CreatedAt       *time.Time             `json:"created_at,omitempty"`
	UpdatedAt       *time.Time             `json:"updated_at,omitempty"`
	CompletedAt     *time.Time             `json:"completed_at,omitempty"`
	ErrorMessage    *string                `json:"error_message,omitempty"`
	RetryCount      *int                   `json:"retry_count,omitempty"`
	NextRetryAt     *time.Time             `json:"next_retry_at,omitempty"`
	Progress        map[string]any         `json:"progress,omitempty"`
	ResultMetadata  map[string]any         `json:"result_metadata,omitempty"`
	Details         map[string]any         `json:"details,omitempty"`
	ChildOperations []ChildOperationStatus `json:"child_operations,omitempty"`
	TaskPayload     map[string]any         `json:"task_payload,omitempty"`
}

// OperationListResponse is the operation listing envelope. Every field name is
// public surface.
type OperationListResponse struct {
	BankID     string      `json:"bank_id"`
	Total      int         `json:"total"`
	Limit      int         `json:"limit"`
	Offset     int         `json:"offset"`
	Operations []Operation `json:"operations"`
}

// OperationActionResponse is the cancel/retry response. Every field name is
// public surface.
type OperationActionResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	OperationID string `json:"operation_id"`
}

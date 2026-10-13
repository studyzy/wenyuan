package memoryops

import (
	"time"
)

// RetainResponse is the write acceptance response. Every field name is public
// surface.
type RetainResponse struct {
	Success      bool     `json:"success"`
	BankID       string   `json:"bank_id"`
	ItemsCount   int      `json:"items_count"`
	Async        bool     `json:"async"`
	Usage        Usage    `json:"usage"`
	OperationID  string   `json:"operation_id,omitempty"`
	OperationIDs []string `json:"operation_ids,omitempty"`
	// ProcessedContentTokens is the number of content tokens the engine
	// accounted for. It is omitted when the path does not report one, so a
	// client never reads a fabricated zero as a real measurement.
	ProcessedContentTokens *int `json:"processed_content_tokens,omitempty"`
}

// Operation is the operation object exposed by the list, fetch, cancel, and
// retry routes. Every field name is public surface.
type Operation struct {
	ID            string         `json:"id"`
	BankID        string         `json:"bank_id"`
	OperationType string         `json:"operation_type"`
	TaskType      string         `json:"task_type"`
	Status        string         `json:"status"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
	RetryCount    int            `json:"retry_count"`
	NextRetryAt   *time.Time     `json:"next_retry_at,omitempty"`
	Progress      map[string]any `json:"progress,omitempty"`

	// Payload carries the raw submission payload when requested.
	Payload map[string]any `json:"payload,omitempty"`
	// ResultMetadata carries processing result metadata when available.
	ResultMetadata map[string]any `json:"result_metadata,omitempty"`
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

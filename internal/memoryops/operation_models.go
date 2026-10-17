package memoryops

// RetainResponse is the write acceptance response. Every field name is public
// surface. The reference keeps nulls for this route, so operation_id and
// operation_ids are always on the wire and report null when the write was
// synchronous or resolved to a single strategy group — the reference only
// populates operation_ids when a request fanned into more than one group.
type RetainResponse struct {
	Success    bool   `json:"success"`
	BankID     string `json:"bank_id"`
	ItemsCount int    `json:"items_count"`
	Async      bool   `json:"async"`
	// OperationID is null for a synchronous write.
	OperationID *string `json:"operation_id"`
	// OperationIDs is null unless the write resolved to more than one strategy
	// group.
	OperationIDs []string `json:"operation_ids"`
	// Usage is null for an asynchronous write, whose accounting is not known
	// until the queued operation completes.
	Usage *Usage `json:"usage"`
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
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    *string        `json:"updated_at"`
	Status       string         `json:"status"`
	ErrorMessage *string        `json:"error_message"`
	RetryCount   int            `json:"retry_count"`
	NextRetryAt  *string        `json:"next_retry_at"`
	Progress     map[string]any `json:"progress"`
}

// ChildOperationStatus is one child entry of a parent batch operation,
// matching the reference's ChildOperationStatus. The response keeps nulls, so a
// member the child's metadata does not carry is emitted as an explicit null
// rather than dropped or defaulted to zero.
type ChildOperationStatus struct {
	OperationID   string  `json:"operation_id"`
	Status        string  `json:"status"`
	SubBatchIndex *int    `json:"sub_batch_index"`
	ItemsCount    *int    `json:"items_count"`
	ErrorMessage  *string `json:"error_message"`
}

// OperationStatus is the single-operation read, matching the reference's
// OperationStatusResponse. Unlike the list entry it carries result_metadata, the
// child roll-up and the raw task payload. The model keeps nulls (the response
// contract always emits all sixteen keys), so no member carries omitempty: an
// absent value serializes as an explicit null.
type OperationStatus struct {
	OperationID     string                 `json:"operation_id"`
	Status          string                 `json:"status"`
	OperationType   *string                `json:"operation_type"`
	ID              *string                `json:"id"`
	TaskType        *string                `json:"task_type"`
	MentalModelID   *string                `json:"mental_model_id"`
	CreatedAt       *string                `json:"created_at"`
	UpdatedAt       *string                `json:"updated_at"`
	CompletedAt     *string                `json:"completed_at"`
	ErrorMessage    *string                `json:"error_message"`
	RetryCount      *int                   `json:"retry_count"`
	NextRetryAt     *string                `json:"next_retry_at"`
	Progress        map[string]any         `json:"progress"`
	ResultMetadata  map[string]any         `json:"result_metadata"`
	Details         map[string]any         `json:"details"`
	ChildOperations []ChildOperationStatus `json:"child_operations"`
	TaskPayload     map[string]any         `json:"task_payload"`
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

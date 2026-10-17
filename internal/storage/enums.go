package storage

import "time"

// OperationStatus is the async-operation status enumeration. The stored string
// values are the compatibility surface.
type OperationStatus string

// Async-operation status values.
const (
	StatusPending    OperationStatus = "pending"
	StatusProcessing OperationStatus = "processing"
	StatusCompleted  OperationStatus = "completed"
	StatusFailed     OperationStatus = "failed"
	StatusCancelled  OperationStatus = "cancelled"
	// StatusNotFound is the operation-status value reported for an unknown
	// operation identifier; it is distinct from failed.
	StatusNotFound OperationStatus = "not_found"
)

// OperationTypeRetain is the operation type of an asynchronous retain. Reflect
// is not an async-operation type: it runs inside the request that asked for it
// and is reported through the LLM trace, not the operations queue.
const (
	OperationTypeRetain = "retain"
)

// EntityKind is the entity-kind enumeration. The stored string values are the
// compatibility surface.
type EntityKind string

// Entity-kind values.
const (
	EntityKindRegular EntityKind = "regular"
	EntityKindLabel   EntityKind = "label"
)

// EntityKinds returns every entity kind in stable order.
func EntityKinds() []EntityKind {
	return []EntityKind{EntityKindRegular, EntityKindLabel}
}

// Operation is an asynchronous unit of work persisted through the store.
type Operation struct {
	// OperationID is the operation's primary key, named for the reference's
	// column so a query and a struct read alike.
	OperationID    string
	BankID         string
	OperationType  string
	Status         OperationStatus
	Error          string
	Run            int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
	RetryCount     int
	NextRetryAt    *time.Time
	Progress       map[string]any
	Payload        map[string]any
	ResultMetadata map[string]any
	// WorkerID is the consumer that currently holds the claim, empty when the
	// operation is unclaimed.
	WorkerID string
	// ClaimedAt is when the current claim was taken. A claim older than the
	// lease is treated as abandoned and becomes reclaimable.
	ClaimedAt *time.Time
	// SerializationKey narrows concurrency: at most one operation carrying a
	// given key runs at a time, so two refreshes of the same record cannot
	// apply their writes beside each other and leave whichever finished last
	// winning regardless of which read more. Empty means no constraint, which
	// is what every operation other than a per-record refresh wants.
	SerializationKey string
	// IsParent reports whether this is a parent batch operation, derived from
	// result_metadata["is_parent"].
	IsParent bool
}

// ChildOperation is one child of a parent batch operation. The per-child
// subject fields are optional: the reference projects them out of the child's
// result_metadata and emits an explicit null for a key the child never wrote, so
// absence is carried as a nil pointer rather than collapsed to a zero value.
type ChildOperation struct {
	OperationID   string
	Status        OperationStatus
	SubBatchIndex *int
	ItemsCount    *int
	ErrorMessage  *string
}

// operationIsParent reports whether a stored result metadata marks the
// operation as a parent batch operation.
func operationIsParent(metadata map[string]any) bool {
	value, ok := metadata["is_parent"].(bool)
	return ok && value
}

// Terminal reports whether the operation has reached a terminal status.
func (s OperationStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// validTransition reports whether a status move is allowed by the lifecycle
// pending -> processing -> completed|failed, with cancellation reachable from a
// non-terminal status.
func validTransition(from, to OperationStatus) bool {
	switch from {
	case StatusPending:
		return to == StatusProcessing || to == StatusCompleted || to == StatusFailed || to == StatusCancelled
	case StatusProcessing:
		return to == StatusCompleted || to == StatusFailed || to == StatusProcessing || to == StatusCancelled
	case StatusFailed:
		return to == StatusProcessing || to == StatusPending
	case StatusCompleted, StatusCancelled:
		return false
	default:
		return false
	}
}

// MemoryUnitType is the memory-unit type enumeration. The final narrowed set
// is authoritative; the initial wider set is superseded by migration 0003.
type MemoryUnitType string

// Final memory-unit type values.
const (
	TypeWorld       MemoryUnitType = "world"
	TypeExperience  MemoryUnitType = "experience"
	TypeObservation MemoryUnitType = "observation"
)

// MemoryLinkType is the memory-link type enumeration.
type MemoryLinkType string

// Memory-link type values.
const (
	LinkTemporal MemoryLinkType = "temporal"
	LinkSemantic MemoryLinkType = "semantic"
	LinkEntity   MemoryLinkType = "entity"
	LinkCauses   MemoryLinkType = "causes"
	LinkCausedBy MemoryLinkType = "caused_by"
	LinkEnables  MemoryLinkType = "enables"
	LinkPrevents MemoryLinkType = "prevents"
)

// FinalMemoryUnitTypes returns the final narrowed type set in stable order.
func FinalMemoryUnitTypes() []MemoryUnitType {
	return []MemoryUnitType{TypeWorld, TypeExperience, TypeObservation}
}

// MemoryLinkTypes returns every memory-link type in stable order.
func MemoryLinkTypes() []MemoryLinkType {
	return []MemoryLinkType{
		LinkTemporal, LinkSemantic, LinkEntity,
		LinkCauses, LinkCausedBy, LinkEnables, LinkPrevents,
	}
}

// IsValidMemoryUnitType reports whether a type belongs to the final set.
func IsValidMemoryUnitType(t MemoryUnitType) bool {
	for _, candidate := range FinalMemoryUnitTypes() {
		if candidate == t {
			return true
		}
	}
	return false
}

// IsValidMemoryLinkType reports whether a link type belongs to the set.
func IsValidMemoryLinkType(t MemoryLinkType) bool {
	for _, candidate := range MemoryLinkTypes() {
		if candidate == t {
			return true
		}
	}
	return false
}

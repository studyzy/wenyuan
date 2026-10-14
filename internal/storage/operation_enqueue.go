package storage

import "context"

// EnqueueRequest is an enqueue that must not duplicate work already queued.
//
// A background loop runs in every process of a deployment with no leader
// election, so several processes reach the same decision at the same instant.
// The submit that follows has to be idempotent by itself rather than trusting
// a read that every process performed and every process found empty.
type EnqueueRequest struct {
	// Operation is the record to insert. Its ID is generated when empty.
	Operation Operation
	// DedupeByPayloadKey names a key inside the operation's payload that
	// identifies the subject of the work — the record being refreshed, say.
	// When set, an existing operation of the same type carrying the same value
	// under that key suppresses the insert, and the existing operation is
	// returned with Deduplicated true. Empty disables the check.
	DedupeByPayloadKey string
	// DedupeIncludesProcessing widens the suppression to an operation already
	// being *processing*, not just one pending.
	//
	// A pending operation always suppresses, whatever this is: every queued
	// copy of a refresh does exactly the same work, so a second one only pays
	// for the same retrieval and model call twice. Whether a *running* one
	// suppresses is the caller's call. An automatic trigger wants it — the
	// caller did not ask for this specific refresh and a running one covers
	// its intent. An explicit request does not — a refresh already in flight
	// may have read its inputs before the caller's edit.
	DedupeIncludesProcessing bool
}

// EnqueueResult reports what an enqueue did.
type EnqueueResult struct {
	// Operation is the operation that will run: the one inserted, or the
	// existing one that suppressed the insert.
	Operation Operation
	// Deduplicated reports that an existing operation suppressed the insert,
	// so the caller can tell "queued" from "already queued".
	Deduplicated bool
}

// EnqueueOperation inserts a background operation, refusing to duplicate work
// that is already queued or running for the same subject.
//
// It is separate from PutOperation because the two answer different questions:
// PutOperation records an operation an HTTP caller is polling by id and is
// idempotent on that id, while this one is idempotent on the *subject* and must
// decide under concurrency inside the inserting statement.
type OperationEnqueuer interface {
	EnqueueOperation(ctx context.Context, req EnqueueRequest) (EnqueueResult, error)
}

// MentalModelIDPayloadKey names the payload field holding the mental model an
// operation works on. It is both the dedup subject key and what the discovery
// routine's in-flight probe compares, so the two cannot drift.
const MentalModelIDPayloadKey = "mental_model_id"

// MentalModelSerializationKey is the key that keeps two refreshes of one model
// from running at the same time.
func MentalModelSerializationKey(modelID string) string {
	return "mental_model:" + modelID
}

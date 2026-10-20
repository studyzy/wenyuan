package storage

import (
	"context"
	"time"
)

// OperationResultRecorder is the optional capability a backend implements when
// it can carry structured result metadata onto an operation as that operation
// is settled.
//
// AdvanceOperation deliberately stays metadata-free: an operation type that
// reports nothing — which is every type but a mental model refresh today — must
// take the plain path unchanged, and a backend that cannot record the metadata
// must not be forced to pretend it can. The worker falls back to AdvanceOperation
// whenever a store does not implement this, so the capability only ever adds a
// richer record; it never becomes a requirement.
type OperationResultRecorder interface {
	// AdvanceOperationWithResult advances the operation to next, records detail
	// in its error column, and merges result into its result metadata. A key in
	// result replaces any existing value rather than the whole metadata being
	// overwritten, so fields written when the operation was enqueued survive.
	AdvanceOperationWithResult(ctx context.Context, opID string, next OperationStatus, detail string, result map[string]any) (Operation, error)
}

// OperationRetryRecorder is the optional capability a backend implements when it
// can record a retry — a deferral or an ordinary backoff — together with the
// structured result metadata the processor reported about the attempt that led
// to it.
//
// ScheduleOperationRetry deliberately stays metadata-free for the same reason
// AdvanceOperation does: a processor that reports nothing must take the plain
// path unchanged, and a backend that cannot record metadata must not be forced
// to pretend it can. A processor that reports before asking for a deferral — a
// webhook delivery naming the receiver's status, say — needs the report to
// survive the retry, so the worker merges it here rather than dropping it.
type OperationRetryRecorder interface {
	// ScheduleOperationRetryWithResult records a failed attempt and the moment
	// the next one becomes claimable, returning the operation to the claimable
	// pool, and merges result into its result metadata. A key in result replaces
	// any existing value rather than the whole metadata being overwritten, so
	// fields written when the operation was enqueued survive.
	ScheduleOperationRetryWithResult(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, detail string, result map[string]any) error
}

// Both backends carry the capability; a store that did not would fail this at
// compile time rather than silently dropping a processor's report.
var (
	_ OperationResultRecorder = (*MemoryStore)(nil)
	_ OperationResultRecorder = (*PostgresStore)(nil)
	_ OperationRetryRecorder  = (*MemoryStore)(nil)
	_ OperationRetryRecorder  = (*PostgresStore)(nil)
)

// OperationDeferrer is the optional capability a backend implements when it can
// reschedule an operation without recording it as a failure.
//
// A deferral is intentional backpressure, not a verdict on the work: a provider
// rate limit, a saturated store, or an append that lost its race and must be
// read again. The reference keeps those two things apart in the record, because
// a caller that waits for a bank to settle treats a pending operation carrying
// an error as already lost — which a deferral is not. So a deferral moves the
// next attempt and leaves the error column alone, and the reason goes to the
// log and to whatever the processor reported about the attempt.
type OperationDeferrer interface {
	// DeferOperation returns the operation to the claimable pool for next,
	// without spending its retry budget and without recording a failure, and
	// merges result into its result metadata when the processor reported
	// anything about the attempt.
	DeferOperation(ctx context.Context, opID string, next time.Time, result map[string]any) error
}

// Both backends carry the capability; a store that did not would fall back to
// the retry path in the consumer, which does record a failure message.
var (
	_ OperationDeferrer = (*MemoryStore)(nil)
	_ OperationDeferrer = (*PostgresStore)(nil)
)

// OperationLiveness is the capability a backend implements so a consumer can
// ask whether the operation it is about to run — or is running — still exists
// and has not been cancelled.
//
// It is the reference's operation liveness read: the API's cancel path only
// flips a status, and a bank delete removes the operation row (PostgreSQL does
// so by foreign-key cascade; the memory backend purges the queue itself), so the
// running task is the one that notices. A consumer checks it at execution start
// so it does not run work whose bank is gone — which would resurrect the bank
// through the write path's lazy create — and a long-running write checks it at
// its item boundaries so a cancelled or deleted operation stops early.
type OperationLiveness interface {
	// OperationAlive reports whether the identified operation exists, scoped to
	// the given bank, and is not cancelled. A missing row reads as not alive; it
	// does not fault.
	OperationAlive(ctx context.Context, bankID, opID string) (bool, error)
}

// Both backends carry the capability; one that did not would be treated as
// always alive by the consumer, which is the pre-check behaviour.
var (
	_ OperationLiveness = (*MemoryStore)(nil)
	_ OperationLiveness = (*PostgresStore)(nil)
)

// OperationProgressWriter is the optional capability a backend implements when
// it can record a progress snapshot for a running operation.
//
// The reference writes these at coarse phase and batch boundaries —
// `_write_operation_progress` merges a `progress` object into
// `async_operations.result_metadata` and bumps `updated_at` — so an operator
// polling the status route can tell a healthy long-running job (the counters
// advancing across polls) from a frozen one (the same numbers, a stale `at`).
// The write is best-effort by construction: a heartbeat that fails must never
// fail the job it is reporting on, so a caller logs the failure and carries on.
type OperationProgressWriter interface {
	// RecordOperationProgress merges snapshot into the operation's
	// `result_metadata.progress`, leaving sibling metadata keys alone.
	RecordOperationProgress(ctx context.Context, opID string, snapshot map[string]any) error
}

// Both backends carry the capability, so a worker never has to ask whether the
// store it holds can be reported to.
var (
	_ OperationProgressWriter = (*MemoryStore)(nil)
	_ OperationProgressWriter = (*PostgresStore)(nil)
)

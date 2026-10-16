package worker

import (
	"context"
	"errors"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// outcomeKey is the context key under which the consumer stashes the sink that
// collects the metadata a processor reports about the operation it ran. It is
// unexported so only this package installs or reads the sink, and a processor
// can only reach it through ReportOutcome.
type outcomeKey struct{}

// outcomeSink accumulates one operation's reported metadata. The consumer
// creates a sink per operation, so two operations running concurrently never
// share one.
type outcomeSink struct {
	metadata map[string]any
}

// ReportOutcome records structured metadata about the operation the current
// processor is running, to be persisted on the operation record when the
// consumer settles it.
//
// It is optional by design: a processor that never calls it records nothing and
// behaves exactly as before, and a processor run outside a consumer (a direct
// call in a test, say) reports into no sink, which is a no-op. A later report
// replaces an earlier one key by key, so a processor that revises its account
// as it learns more ends up recorded once.
//
// It works on the failure path too: a processor reports before it returns an
// error, so a refused operation can carry the same machine-readable record a
// successful one does rather than only a prose error string.
func ReportOutcome(ctx context.Context, metadata map[string]any) {
	if len(metadata) == 0 {
		return
	}
	sink, _ := ctx.Value(outcomeKey{}).(*outcomeSink)
	if sink == nil {
		return
	}
	if sink.metadata == nil {
		sink.metadata = make(map[string]any, len(metadata))
	}
	for key, value := range metadata {
		sink.metadata[key] = value
	}
}

// complete marks an operation succeeded, recording any metadata the processor
// reported.
func (c *Consumer) complete(ctx context.Context, op storage.Operation, metadata map[string]any) {
	if _, err := c.settle(ctx, op.OperationID, storage.StatusCompleted, "", metadata); err != nil {
		c.reportSettleLapse(op, "mark-completed", err)
		return
	}
	c.cfg.Logger.Info("operation completed", "operation_id", op.OperationID, "bank_id", op.BankID)
	c.promoteParent(ctx, op)
}

// fail marks an operation terminally failed, recording any metadata the
// processor reported alongside the prose detail.
func (c *Consumer) fail(ctx context.Context, op storage.Operation, detail string, metadata map[string]any) {
	if _, err := c.settle(ctx, op.OperationID, storage.StatusFailed, detail, metadata); err != nil {
		c.reportSettleLapse(op, "mark-failed", err)
		return
	}
	c.cfg.Logger.Warn("operation failed permanently", "operation_id", op.OperationID, "detail", detail)
	c.promoteParent(ctx, op)
}

// settleLapsed reports whether a terminal or retry write did not apply because
// the operation's row is gone — its bank was deleted — or no longer accepts the
// transition.
//
// It is the reference's terminal-write behaviour: each of its mark-completed,
// mark-failed, schedule-retry and defer writes is an UPDATE filtered on the
// running status, so a row deleted with its bank (or advanced under the writer)
// makes the statement affect zero rows without raising. The worker mirrors that
// by treating such a write as a no-op rather than a failure.
func settleLapsed(err error) bool {
	return errors.Is(err, storage.ErrOperationNotFound) || errors.Is(err, storage.ErrOperationConflict)
}

// reportSettleLapse records a settle that did not apply: a no-op is reported at
// info when the row it targeted is gone or already settled, and as a failure
// otherwise. A lapsed settle is never an error, and because the caller returns
// on it the parent-batch rollup the settle would have driven does not run.
func (c *Consumer) reportSettleLapse(op storage.Operation, action string, err error) {
	if settleLapsed(err) {
		c.cfg.Logger.Info("operation row is gone or already settled; "+action+" is a no-op",
			"operation_id", op.OperationID, "bank_id", op.BankID)
		return
	}
	c.cfg.Logger.Error(action+" failed", "operation_id", op.OperationID, "error", err)
}

// promoteParent rolls a batch up when the operation that just reached a
// terminal state is one of its children.
//
// A parent carries no work of its own, so nothing executes it and no claim ever
// touches it: the last child to finish is what promotes it. The write is
// guarded inside the store, so several children finishing at once promote the
// parent exactly once. A failure here is logged rather than returned: the
// child's own outcome is already committed and re-settling it would be wrong.
func (c *Consumer) promoteParent(ctx context.Context, op storage.Operation) {
	parentID, _ := op.ResultMetadata["parent_operation_id"].(string)
	if parentID == "" {
		return
	}
	promoted, updated, err := c.store.FinalizeBatchParent(ctx, op.BankID, parentID)
	if err != nil {
		c.cfg.Logger.Error("parent roll-up failed",
			"operation_id", op.OperationID, "parent_operation_id", parentID, "error", err)
		return
	}
	if updated {
		c.cfg.Logger.Info("batch parent promoted",
			"parent_operation_id", promoted.OperationID, "status", promoted.Status)
	}
}

// settle advances an operation to a terminal status, attaching reported
// metadata when there is any and the store can carry it. A store without the
// capability records only the status and detail, exactly as before, so the
// richer record is never a requirement.
func (c *Consumer) settle(ctx context.Context, opID string, status storage.OperationStatus, detail string, metadata map[string]any) (storage.Operation, error) {
	if len(metadata) > 0 {
		if recorder, ok := c.store.(storage.OperationResultRecorder); ok {
			return recorder.AdvanceOperationWithResult(ctx, opID, status, detail, metadata)
		}
	}
	return c.store.AdvanceOperation(ctx, opID, status, detail)
}

// scheduleRetry records a retry and, when the processor reported metadata and
// the store can carry it, merges that metadata onto the operation in the same
// move. The retry's schedule is the caller's argument unchanged, and a store
// without the capability — or a processor that reported nothing — takes the
// plain ScheduleOperationRetry path exactly as before.
func (c *Consumer) scheduleRetry(ctx context.Context, op storage.Operation, retryCount int, next time.Time, detail string, metadata map[string]any) error {
	if len(metadata) > 0 {
		if recorder, ok := c.store.(storage.OperationRetryRecorder); ok {
			return recorder.ScheduleOperationRetryWithResult(ctx, op.OperationID, retryCount, next, detail, metadata)
		}
	}
	return c.store.ScheduleOperationRetry(ctx, op.OperationID, retryCount, next, detail)
}

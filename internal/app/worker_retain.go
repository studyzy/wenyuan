package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/studyzy/wenyuan/internal/history"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/worker"
)

// applyRetain runs one queued retain: the write a submission stashed, replayed
// through the same pipeline the synchronous path uses. The payload was rendered
// when the request was accepted and carries the bank's resolved extraction and
// storage settings, so a queued write stores exactly what the caller's request
// described rather than what the bank's configuration happens to be when a
// worker gets to it.
//
// The operation record belongs to this worker: the write is told to leave the
// queue alone, so it cannot create a second operation for the same work.
func (p archiveProcessor) applyRetain(ctx context.Context, op storage.Operation) error {
	req, err := storage.DecodeAsyncRetainPayload(op.Payload)
	if err != nil {
		// The payload is stored with the work, so an unreadable one would be
		// rejected identically on every attempt.
		return worker.Permanent(fmt.Errorf("queued retain carries no readable payload: %w", err))
	}
	req.BankID = op.BankID
	// The queued path loses the request context, so the trace attribution the
	// synchronous path sets has to be re-established here: without it every
	// extraction call this write makes lands in the model-request trace with no
	// bank and no operation, and the /llm-requests surface cannot list it.
	ctx = provider.WithTraceContext(ctx, provider.TraceContext{BankID: op.BankID, Operation: "retain"})
	// The provider-facing boundaries are supplied here rather than carried in
	// the payload, so a model change reaches queued work without a schema
	// change — and so a queued write extracts the same way the synchronous one
	// would today.
	req.Extraction = newFactExtractor(p.provider, p.extraction)
	req.Embeddings = p.embeddings
	outcome, err := p.store.RetainWrite(ctx, req)
	if err != nil {
		return classifyRetainFailure(err)
	}
	// The document change history files this retain on the operation that ran
	// it — a synchronous retain has none, and leaves none (orchestrator.py:590-619).
	p.recordRetainEvent(ctx, op, req, outcome)
	// The write landed, so the completion events are raised here exactly as the
	// synchronous path raises them: one delivery per submitted item, sharing the
	// operation that ran the write. A failed write raises none — the reference's
	// outbox callback runs inside the retain's transaction, so a rolled-back write
	// leaves no event behind.
	p.notifyRetainCompleted(ctx, op, req.Items)
	// Count the document by its extraction outcome, as the synchronous path
	// does, so both routes report the same facts/no_facts split.
	monitoring.Workload.CountRetainDocument(countUnitIDs(outcome))
	// A queued retain folds its facts into observations exactly as a
	// synchronous one does. The enqueue happens here, after the write landed,
	// rather than at submission: a consolidation queued first could run before
	// the facts it is meant to fold exist.
	p.enqueuePostRetainConsolidation(ctx, op.BankID)
	worker.ReportOutcome(ctx, map[string]any{
		"unit_ids_count":          countUnitIDs(outcome),
		"extraction_errors_count": 0,
		// The reference leaves the last durable progress snapshot on the
		// operation when its batch commits: `stage="storing"`,
		// `processed=chunks_committed`, `total=total_chunks`,
		// `detail={"facts_committed": len(all_unit_ids)}`, stamped with an
		// `at` the heartbeat records
		// (engine/retain/orchestrator.py:2866-2879,
		// engine/memory_engine.py:5163). A row that never recorded one reads as
		// `progress: null`, which is what this service answered for every
		// finished retain before.
		"progress": retainProgressSnapshot(countUnitIDs(outcome), time.Now().UTC()),
	})
	return nil
}

// recordRetainEvent adds this retain to each document's change history.
//
// The event carries what the write did: the facts it added, the facts it
// retired and the observations the stale-observation sweep dropped. The kind
// comes from the write's mode — a forced re-extract is a reprocess, then an
// append, then a retain — and the event lands on the operation that ran it, so
// a change without an operation leaves none.
func (p archiveProcessor) recordRetainEvent(ctx context.Context, op storage.Operation, req storage.RetainRequest, outcome storage.RetainOutcome) {
	for index, item := range req.Items {
		if item.DocumentID == "" || index >= len(outcome.ItemUnitIDs) {
			continue
		}
		event := history.RetainEvent(item.DocumentID, item.UpdateMode, item.ForceReextract,
			len(outcome.ItemUnitIDs[index]), nil)
		history.AppendOperationEvents(ctx, p.store, op.OperationID, event)
	}
}

// notifyRetainCompleted fires one retain.completed event per item the queued
// child operation retained, in the shape the synchronous path raises (see
// memoryops.RetainCompletedData): the operation is the retain operation a worker
// ran, and the data carries the document the item named, the tags it held and how
// many memory units that document owns. The reference raises these from the same
// outbox for both routes, so the two differ only in the operation id — a
// synchronous retain mints one, a queued one carries the id of the operation the
// worker executed.
func (p archiveProcessor) notifyRetainCompleted(ctx context.Context, op storage.Operation, items []storage.RetainItem) {
	for _, item := range items {
		p.enqueueWebhookEvent(ctx, op.BankID, "retain.completed",
			memoryops.RetainCompletedData(op.OperationID, item.DocumentID, item.Tags,
				memoryops.DocumentUnitCount(ctx, p.store, op.BankID, item.DocumentID)))
	}
}

// enqueuePostRetainConsolidation queues the pass that folds a queued retain's
// facts into observations. The bank's own enable_observations and
// enable_auto_consolidation overrides win over the deployment defaults, the
// same resolution the synchronous route applies; a failure is logged and
// swallowed because the write is already durable and the reconcile sweep will
// pick the bank up.
func (p archiveProcessor) enqueuePostRetainConsolidation(ctx context.Context, bankID string) {
	observations := p.observationsEnabled
	auto := p.autoConsolidationEnabled
	if bank, err := p.store.GetBank(ctx, bankID); err == nil {
		if value, ok := bank.Config["enable_observations"].(bool); ok {
			observations = value
		}
		if value, ok := bank.Config["enable_auto_consolidation"].(bool); ok {
			auto = value
		}
	}
	if !observations || !auto {
		return
	}
	if _, _, err := p.store.EnqueueConsolidation(ctx, bankID); err != nil {
		slog.Default().Warn("could not enqueue post-retain consolidation", "bank_id", bankID, "error", err)
	}
}

// classifyRetainFailure separates a write that can succeed later from one that
// cannot.
func classifyRetainFailure(err error) error {
	switch {
	case provider.IsAuthRejected(err):
		// The provider rejected the configured credential (401/403). Every
		// replay sends the same key and earns the same rejection, so the
		// operation fails once instead of spending the retry budget — and the
		// wall-clock a full schedule costs — on an answer that cannot change
		// (#5644).
		return worker.Permanent(err)
	case errors.Is(err, storage.ErrDocumentConflict):
		// An append that lost its race wrote nothing, so "the document moved,
		// read it again" is a retry rather than a verdict on the content. It is
		// deferred without consuming the attempt budget, because a redone
		// append is cheap — the document's existing chunks still match by hash
		// and only this submission's new ones are extracted.
		return worker.Defer(err)
	case errors.Is(err, storage.ErrWritesUnavailable), errors.Is(err, storage.ErrReadShed):
		// The store shedding load is backpressure, which the consumer already
		// treats as a deferral; returning it unchanged keeps that classification
		// in one place.
		return err
	default:
		// Everything else — a model that returned something unreadable, a store
		// error — is retried with a backoff and fails once the budget is spent,
		// so the operation carries the cause either way.
		return err
	}
}

// countUnitIDs reports how many memory units one write produced, which is the
// outcome counter a caller reads to tell work done from work skipped.
func countUnitIDs(outcome storage.RetainOutcome) int {
	total := 0
	for _, ids := range outcome.ItemUnitIDs {
		total += len(ids)
	}
	return total
}

// retainProgressSnapshot renders the `progress` object a finished retain leaves
// on its operation, in the reference's own shape. `processed` and `total` are
// the chunks the submission stored: a queued retain is published after its write
// commits, so the snapshot it leaves behind is the final one, and
// `facts_committed` is the units that write produced.
func retainProgressSnapshot(units int, at time.Time) map[string]any {
	return map[string]any{
		"stage":     "storing",
		"at":        at.Format("2006-01-02T15:04:05.999999+00:00"),
		"processed": units,
		"total":     units,
		"detail":    map[string]any{"facts_committed": units},
	}
}

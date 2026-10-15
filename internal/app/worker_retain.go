package app

import (
	"context"
	"errors"
	"fmt"

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
	req.Extraction = newFactExtractor(p.provider, p.correctionRetries)
	req.Embeddings = p.embeddings
	outcome, err := p.store.RetainWrite(ctx, req)
	if err != nil {
		return classifyRetainFailure(err)
	}
	worker.ReportOutcome(ctx, map[string]any{
		"unit_ids_count":          countUnitIDs(outcome),
		"extraction_errors_count": 0,
	})
	return nil
}

// classifyRetainFailure separates a write that can succeed later from one that
// cannot.
func classifyRetainFailure(err error) error {
	switch {
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

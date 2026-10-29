package storage

import (
	"context"
	"log/slog"

	"github.com/studyzy/wenyuan/internal/history"
)

// Document change history for the curation write paths.
//
// A fact edit, invalidation or restore is recorded on the consolidation
// operation it queues (the one that rebuilds what it removed), so a change with
// automatic consolidation off — or a write that ran without an operation —
// leaves no event, exactly as the reference documents (#5638). The sweep that
// removed the observations notes them on the track bound by history.TrackChanges,
// and this file turns that note into the event.

// curationActionForHistory maps a curation action to the change-history action
// name the event vocabulary uses. An action with no event of its own — a revert
// of nothing, or no curation at all — reports "" and records nothing.
func curationActionForHistory(action curationAction) string {
	switch action {
	case curationEdit:
		return "edit"
	case curationInvalidate:
		return "invalidate"
	case curationRevert:
		return "revert"
	default:
		return ""
	}
}

// curationHistory records what one curation write did, so the event can be filed
// on the consolidation it queues. It is filled in while the write runs and
// consumed after the follow-up pass is submitted.
type curationHistory struct {
	bankID     string
	memoryID   string
	documentID string
	action     string
	changes    *history.ObservationsChanges
	// untracked is set when the caller bound a history trace at this write, so
	// the tracked writes are filed by that outermost scope instead of here.
	untracked bool
}

// beginCurationHistory binds the tracked-writes record for one curation write
// and reads the fact's document, which the event is filed under.
func beginCurationHistory(ctx context.Context, store Store, bankID, memoryID string, action curationAction) (*curationHistory, context.Context) {
	track := &curationHistory{bankID: bankID, memoryID: memoryID, action: curationActionForHistory(action)}
	if track.action == "" {
		return track, ctx
	}
	// A caller already tracking this write owns the notes; this write must not
	// bind a second record, or the inner defer would file events the outer sweep
	// has not finished collecting.
	if history.ChangesTracked(ctx) {
		track.untracked = true
		return track, ctx
	}
	ctx, track.changes = history.TrackChanges(ctx)
	return track, ctx
}

// recordDocument reads the fact's document. It is called before the curation
// moves the row, so a fact that is about to leave the live view still resolves.
func (t *curationHistory) recordDocument(ctx context.Context, store Store) {
	if t.untracked || t.documentID != "" {
		return
	}
	documents, err := resolveFactDocuments(ctx, store, t.bankID, []string{t.memoryID})
	if err != nil || len(documents) == 0 {
		return
	}
	t.documentID = documents[t.memoryID]
}

// file appends the curation event to the consolidation operation the write
// queued. A write that recorded no change (no curated action) files nothing.
func (t *curationHistory) file(ctx context.Context, store Store, operationID string) {
	if t == nil || t.untracked || t.documentID == "" || operationID == "" {
		return
	}
	history.AppendOperationEvents(ctx, store, operationID,
		history.CurationEvents(t.action, t.documentID, t.memoryID, t.changes)...)
}

// clearHistory records one memory's observation clear, so the event is filed on
// the consolidation it queues.
type clearHistory struct {
	bankID     string
	memoryID   string
	documentID string
	changes    *history.ObservationsChanges
	untracked  bool
}

// beginClearHistory binds the tracked-writes record for one memory's clear and
// reads the memory's document before its observations go.
func beginClearHistory(ctx context.Context, store Store, bankID, memoryID string) (*clearHistory, context.Context) {
	track := &clearHistory{bankID: bankID, memoryID: memoryID}
	if history.ChangesTracked(ctx) {
		track.untracked = true
		return track, ctx
	}
	track.recordDocument(ctx, store)
	ctx, track.changes = history.TrackChanges(ctx)
	return track, ctx
}

// recordDocument reads the memory's document, which is where the clear's event
// is filed. Only a clear that removed something is recorded, so the read happens
// on the caller's word that it did.
func (t *clearHistory) recordDocument(ctx context.Context, store Store) {
	if t.untracked || t.documentID != "" {
		return
	}
	documents, err := resolveFactDocuments(ctx, store, t.bankID, []string{t.memoryID})
	if err != nil || len(documents) == 0 {
		return
	}
	t.documentID = documents[t.memoryID]
}

// file appends the clear event to the consolidation operation the clear queued.
func (t *clearHistory) file(ctx context.Context, store Store, operationID string) {
	if t == nil || t.untracked || t.documentID == "" || operationID == "" {
		return
	}
	history.AppendOperationEvents(ctx, store, operationID,
		history.ObservationsClearedEvents(t.documentID, t.memoryID, t.changes)...)
}

// resolveFactDocuments names the document each fact belongs to, through the
// backend's own read. A backend that cannot name documents returns an empty map,
// which leaves every event unfiled rather than failing the write.
func resolveFactDocuments(ctx context.Context, store Store, bankID string, factIDs []string) (map[string]string, error) {
	reader, ok := store.(DocumentFactSourceReader)
	if !ok {
		return map[string]string{}, nil
	}
	documents, err := reader.DocumentFactSources(ctx, bankID, factIDs)
	if err != nil {
		// History is never worth failing a write for.
		slog.Default().Warn("could not resolve a fact's document for the change history",
			"bank_id", bankID, "error", err)
		return map[string]string{}, nil
	}
	return documents, nil
}

// noteSweptObservations records what a stale-observation sweep removed on the
// history trace the calling write bound. The sweep itself is the one write site
// every fact-changing path shares — an edit, an invalidation, a clear, a retag,
// a delete — so noting here is what lets each of them carry
// observations_removed without repeating the note, exactly as the reference
// notes it inside the store's own sweep (memories/pg/writes.py:280).
func noteSweptObservations(ctx context.Context, removed []RemovedObservation, sweptFacts []string) {
	if len(removed) == 0 && len(sweptFacts) == 0 {
		return
	}
	byObservation := make(map[string][]string, len(removed))
	for _, observation := range removed {
		byObservation[observation.ObservationID] = observation.SourceFactIDs
	}
	history.NoteObservationsRemoved(ctx, byObservation, sweptFacts)
}

package consolidation

import (
	"context"

	"github.com/studyzy/wenyuan/internal/history"
)

// The note sites are the write sites that touch observations during a pass. They
// record what the pass did on the record bound by history.TrackChanges, which
// the pass's runner bind for the whole job — a no-op when nothing is bound, so a
// pass started outside a worker (a test, a maintenance job) notes nothing and
// the read costs nothing.
//
// The reference's consolidator.py notes the same three things: a create and an
// update through note_observation_written (the dedup fold counts as an update),
// and a delete through note_observations_removed, read before the row goes
// (:418, :3194-3200, :3236-3243, :3314-3320).

// noteObservationWritten records an observation the pass created or updated.
func (c *Consolidator) noteObservationWritten(ctx context.Context, observationID string, sourceFactIDs []string, created bool) {
	history.NoteObservationWritten(ctx, observationID, sourceFactIDs, created)
}

// noteObservationRemoved records an observation the pass deleted, with the facts
// it stood on. The sources come from the pool the batch loaded, which is the
// pass's own view of the row it is about to delete — the same ids a re-read
// would return, without a second query per delete.
func (c *Consolidator) noteObservationRemoved(ctx context.Context, observationID string, sources []string) {
	history.NoteObservationsRemoved(ctx, map[string][]string{observationID: sources}, nil)
}

// recordConsolidationEvents files the pass's tracked observation writes under
// the documents their source facts came from, on the operation that ran it. It
// runs on the way out of Run, including when the pass failed: every batch that
// got that far committed its writes, and the reference records them for the same
// reason (:1596-1622). A pass with no operation — a test, a maintenance job —
// records nothing, which is the "no operation, no history" rule.
//
// The store must also be able to name each fact's document; a store that cannot
// (anything other than the two memory backends) records no consolidation events
// rather than failing the pass.
func (c *Consolidator) recordConsolidationEvents(ctx context.Context, bankID string, changes *history.ObservationsChanges) {
	if c.operationID == "" || changes == nil {
		return
	}
	writer, ok := c.store.(history.EventWriter)
	if !ok {
		return
	}
	history.RecordConsolidationEvents(ctx, writer, bankID, changes, c.operationID)
}

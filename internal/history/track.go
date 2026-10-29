package history

import (
	"context"
	"log/slog"
)

// trackedChanges is the context key the tracked observations record is bound
// under. A plain key type so no other package can collide with it.
type trackedChanges struct{}

// TrackChanges collects the observation writes made inside the block, returning
// the record they are noted on. Every write site notes through the value the
// context carries, so a site that runs without a tracked operation is a no-op
// rather than a caller that has to be told whether tracking is on.
func TrackChanges(ctx context.Context) (context.Context, *ObservationsChanges) {
	changes := newObservationsChanges()
	return context.WithValue(ctx, trackedChanges{}, changes), changes
}

// ChangesOf returns the record an operation is tracking, or nil when nothing is
// bound — which is what a write site outside a history-aware operation sees.
func ChangesOf(ctx context.Context) *ObservationsChanges {
	changes, _ := ctx.Value(trackedChanges{}).(*ObservationsChanges)
	return changes
}

// ChangesTracked reports whether a write site's notes would be kept — for the
// one note that costs a read.
func ChangesTracked(ctx context.Context) bool {
	return ChangesOf(ctx) != nil
}

// NoteObservationsRemoved records observations deleted (id -> source facts), and
// the facts whose change caused it. A no-op when no operation is tracking.
func NoteObservationsRemoved(ctx context.Context, removed map[string][]string, sweptFacts []string) {
	changes := ChangesOf(ctx)
	if changes == nil {
		return
	}
	for observationID, sources := range removed {
		changes.Removed[observationID] = append([]string(nil), sources...)
	}
	for _, factID := range sweptFacts {
		if factID != "" {
			changes.SweptFacts[factID] = struct{}{}
		}
	}
}

// NoteObservationWritten records an observation consolidation created or
// updated, and the facts it stands on.
func NoteObservationWritten(ctx context.Context, observationID string, sourceFactIDs []string, created bool) {
	changes := ChangesOf(ctx)
	if changes == nil || observationID == "" {
		return
	}
	target := changes.Updated
	if created {
		target = changes.Created
	}
	target[observationID] = append([]string(nil), sourceFactIDs...)
}

// logHistoryFailure reports a history write that could not be recorded. It is a
// warning rather than an error the caller returns: the change it describes
// already committed, and failing it now would undo nothing.
func logHistoryFailure(operationID string, err error) {
	slog.Default().Warn("failed to record document history on operation",
		"operation_id", operationID, "error", err)
}

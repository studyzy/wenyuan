// Decision preparation: what a model's answer must go through before the pass
// writes it. Everything here runs before storage is touched: a target the model
// meant but miscopied is repaired, one it cannot have meant is dropped with a
// warning, and a delete that names a merge carries the survivor its evidence
// moves to.

package consolidation

import (
	"context"
	"log/slog"
	"strings"
)

// MergeStore is the optional capability a store provides for the bookkeeping a
// delete-merge needs beyond the plain writes. The reference moves a merged-away
// observation's evidence onto its survivor, or — when the model named no usable
// target — requeues that evidence so the next pass attaches it
// (_fold_deleted_evidence, consolidator.py:3335-3380); a store without the
// capability turns every merge into a plain delete, whose old sources stay
// stamped consolidated and are never reconsidered.
type MergeStore interface {
	// MergeObservationEvidence folds the evidence of the observation `mergedID`
	// into the observation `survivorID` — empty when the model named no usable
	// target — widening the survivor's dates and keeping its text. A survivor
	// that is gone, or that is not an observation, requeues the merged
	// observation's live sources instead: their consolidated stamps are cleared
	// (the reference's mark_consolidated(when=None)), so the next pass folds
	// them wherever the knowledge now belongs. The caller deletes the merged
	// observation afterwards; the merged row must still exist when this runs.
	MergeObservationEvidence(ctx context.Context, bankID, survivorID, mergedID string) error
}

// SuccessorEnqueuer is the optional capability a store provides when it can
// requeue the next round of a round-capped consolidation chain. Without it a
// pass that folded its round budget stops silently, and the facts it never
// reached wait for a retain to trigger consolidation again.
type SuccessorEnqueuer interface {
	// EnqueueConsolidationSuccessor records a consolidation operation for the
	// bank that carries callerRequested in its task payload, the way the
	// reference's submit_async_consolidation does (memory_engine.py:23716-23717).
	EnqueueConsolidationSuccessor(ctx context.Context, bankID string, callerRequested bool) error
}

// ScopeObservationCounter is the optional capability a store provides when it
// can count a scope's observations without a limit. The scope cap is measured
// against that true count; a store without it keeps the cap measured against
// the bounded candidate pool.
type ScopeObservationCounter interface {
	CountObservationsInScope(ctx context.Context, bankID string, scope []string) (int, error)
}

// reaimUpdatesAtFacts splits a response's updates into the ones that name an
// existing observation and the ones that name a batch FACT id. The latter have
// no observation to update, and dropping them loses the fact: they are applied
// as creates, after the response's own creates (consolidator.py:3753-3760).
func reaimUpdatesAtFacts(decision Decision, factIDs map[string]bool) ([]Create, []Update) {
	creates := decision.Creates
	updates := make([]Update, 0, len(decision.Updates))
	for _, update := range decision.Updates {
		if factIDs[update.ObservationID] {
			creates = append(creates, Create{Text: update.Text, SourceIDs: update.SourceIDs, Reason: update.Reason})
			continue
		}
		updates = append(updates, update)
	}
	return creates, updates
}

// resolvePoolID returns raw as a pool id, tolerating a miscopied tail.
//
// Small models copy the head of a UUID and invent the rest. An exact match
// wins; otherwise the one pool id sharing the first two groups — 48 bits, so
// two candidates never collide by chance in a pool of tens — is meant.
// Anything else is returned unchanged and rejected downstream
// (consolidator.py:3285-3306).
func resolvePoolID(raw string, poolIDs map[string]bool) string {
	if poolIDs[raw] || len(raw) < 13 {
		return raw
	}
	match := ""
	for id := range poolIDs {
		if strings.HasPrefix(id, raw[:13]) {
			if match != "" {
				return raw
			}
			match = id
		}
	}
	if match == "" {
		return raw
	}
	return match
}

// preparedDelete is one delete the pass will apply: the observation it removes,
// the survivor its evidence moves to (empty when the model named none or an
// unusable one), and whether the model meant it as a merge at all — a merge
// whose target cannot be resolved still requeues its evidence rather than
// retiring it.
type preparedDelete struct {
	observationID string
	mergedInto    string
	isMerge       bool
}

// prepareDeletes resolves one response's deletes against the pool. A delete
// naming an observation the same response updates is dropped — the update
// carries the newer knowledge, so it wins (consolidator.py:2648-2657). One
// naming an observation the model was never shown is rejected
// (consolidator.py:2696-2712). A merge target must also be one the model was
// shown, and never the deleted observation itself (consolidator.py:2688-2692).
func prepareDeletes(deletes []Delete, updatedIDs, poolIDs map[string]bool) []preparedDelete {
	prepared := make([]preparedDelete, 0, len(deletes))
	for _, del := range deletes {
		if del.ObservationID == "" {
			// A delete entry without a target names nothing and would remove an
			// arbitrary observation, so it is skipped rather than guessed at.
			continue
		}
		if updatedIDs[del.ObservationID] {
			slog.Warn("Batch consolidation: dropped delete of observation " + del.ObservationID + " — the same response updates it")
			continue
		}
		if !poolIDs[del.ObservationID] {
			slog.Debug("Batch consolidation: rejected delete — observation " + del.ObservationID + " not in unioned recall")
			continue
		}
		target := ""
		if del.MergedInto != "" && del.MergedInto != del.ObservationID && poolIDs[del.MergedInto] {
			target = del.MergedInto
		}
		prepared = append(prepared, preparedDelete{observationID: del.ObservationID, mergedInto: target, isMerge: del.MergedInto != ""})
	}
	return prepared
}

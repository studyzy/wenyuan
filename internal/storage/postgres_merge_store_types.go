//go:build pgx

package storage

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// Which fact kinds a merge round reads and writes.
//
// The team bank is written only by a merge round, so mergedFactTypes is at once
// the set a round reads back as its previous content, the set it may write, and
// the set it sweeps when a fact no round wrote is found in the bank. Keeping the
// three uses on one list is what stops a hand-written `experience` or
// `observation` from surviving because the sweep only knew about `world`.

// mergedFactTypes are the fact kinds the team bank may hold for a project.
//
// `observation` is here because a member's conclusions travel with them, even
// though consolidation regenerates the team's own observations from the merged
// facts.
var mergedFactTypes = []string{string(TypeWorld), string(TypeExperience), string(TypeObservation)}

// mergedFactTypesFor is a member's input types: the same set, minus `experience`
// when the deployment switched personal history out of the input.
func mergedFactTypesFor(includeExperience bool) []string {
	if includeExperience {
		return mergedFactTypes
	}
	return []string{string(TypeWorld), string(TypeObservation)}
}

// countUnmanagedTx counts the team bank's facts that no round wrote.
//
// A non-zero count is what makes the round's write replace them. Reading it while
// the claim is held is what keeps the count and the sweep from disagreeing: a
// fact written between the read and the write would otherwise survive until the
// round after next.
func (s *PostgresMergeStore) countUnmanagedTx(ctx context.Context, project *MergeProject) (int, error) {
	teamCtx, err := WithSchema(ctx, project.TeamSchema)
	if err != nil {
		return 0, err
	}
	qualified := quoteIdent(project.TeamSchema)
	var count int
	err = s.db.QueryRowContext(teamCtx, `
		SELECT count(*) FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2) AND metadata->>'merge_key' IS NULL`,
		project.BankID, mergedFactTypes).Scan(&count)
	return count, err
}

// breakerOpen reports whether the project must skip this round.
//
// The breaker is per project, not per member: a round either writes the project's
// content or does not, so the state that matters is "every member is failing".
// A member with no ledger row has never merged and is not failing, so a project
// gains its first failure records only once rounds actually run.
//
// An open breaker rests for RecoveryInterval, measured from the last counted
// failure, and then lets exactly one round through. That round either succeeds —
// which resets every member's count, so the next failure starts from one — or
// fails and restarts the rest. Retrying on this cadence rather than on every tick
// is the whole point: a project whose answer cannot be parsed otherwise spends a
// model call per tick, forever.
func breakerOpen(states map[string]tenant.MergeState, memberIDs []string, opts MergeRoundOptions, now time.Time) bool {
	if opts.FailThreshold <= 0 || len(memberIDs) == 0 {
		// A threshold of zero is the switch that turns the breaker off. It is
		// checked here as well as at the call site so no future caller can open
		// the breaker by passing an interval without a threshold.
		return false
	}
	lastAttempt := time.Time{}
	for _, id := range memberIDs {
		state, ok := states[id]
		if !ok || state.FailCount < opts.FailThreshold {
			// One member still healthy means the project can make progress.
			return false
		}
		if state.LastAttemptAt == nil {
			// Counting a failure without recording when is a state only an
			// older deployment can be in; the safe reading is "rest is over",
			// so the project gets its probe round instead of stalling until an
			// operator notices.
			return false
		}
		if state.LastAttemptAt.After(lastAttempt) {
			lastAttempt = *state.LastAttemptAt
		}
	}
	if opts.RecoveryInterval <= 0 {
		// No recovery configured: an open breaker stays open until an operator
		// resets it.
		return true
	}
	return now.Sub(lastAttempt) < opts.RecoveryInterval
}

//go:build pgx

package storage

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// Which fact kinds a merge round reads, and which the team bank may hold.
//
// The two are not the same set. The team bank is written only by a merge round
// and by consolidation: the round copies `world` and `experience` verbatim, and
// consolidation then synthesises the team's own `observation` rows from them.
// Both kinds end up in the bank, so the sweep, the reconciliation and the status
// counters must know about `observation` — but a round must never read one back
// from a member.

// mergedInputTypes are the fact kinds a round reads from a member's bank.
//
// `observation` is deliberately absent. An observation is a derived row that
// consolidation rewrites in place — same id, new text — and that rewrite does
// not move `updated_at`, so the round's cursor can never see it again; and were
// it seen, its new text is a new merge key, which would leave two versions of
// one conclusion standing. A member's conclusions still reach the team bank,
// because the team bank re-derives them from the facts that were copied.
func mergedInputTypes(includeExperience bool) []string {
	if includeExperience {
		return []string{string(TypeWorld), string(TypeExperience)}
	}
	return []string{string(TypeWorld)}
}

// teamBankTypes are the fact kinds the team bank may hold for a project.
//
// The team's own consolidation writes `observation` rows here, which is why this
// set stays wider than the input set: a sweep that only knew about `world` and
// `experience` would delete the team's conclusions on every round.
var teamBankTypes = []string{string(TypeWorld), string(TypeExperience), string(TypeObservation)}

// mergeMetadataOrigin marks a fact row a merge round wrote. It is the team
// bank's "managed" test: anything else in the bank — a hand-written fact, or a
// row written by an older round that keyed facts on their text — is not the
// round's and the next round replaces it.
const mergeMetadataOrigin = "merge"

// countUnmanagedTx counts the team bank's facts that no round wrote.
//
// A non-zero count is what makes the round's write replace them. Reading it while
// the claim is held is what keeps the count and the sweep from disagreeing: a
// fact written between the read and the write would otherwise survive until the
// round after next.
func (s *PostgresMergeStore) countUnmanagedTx(ctx context.Context, tx Tx, project *MergeProject) (int, error) {
	teamCtx, err := WithSchema(ctx, project.TeamSchema)
	if err != nil {
		return 0, err
	}
	qualified := quoteIdent(project.TeamSchema)
	var count int
	err = tx.QueryRowContext(teamCtx, `
		SELECT count(*) FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2)
		  AND COALESCE(metadata->>'origin', '') <> $3`,
		project.BankID, teamBankTypes, mergeMetadataOrigin).Scan(&count)
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

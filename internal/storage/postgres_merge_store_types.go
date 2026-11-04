//go:build pgx

package storage

import (
	"context"
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

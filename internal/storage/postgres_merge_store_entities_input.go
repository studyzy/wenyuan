//go:build pgx

package storage

import "context"

// Reading the entity names a batch's facts mention, so the round can rebuild the
// team bank's own entities from them.
//
// The names are read from the member bank rather than extracted from the text: a
// member's retain already resolved them, and re-extraction would be a model call
// in the write path — the dependency the verbatim copy exists to avoid.
//
// Each fact keeps the names exactly as the member's bank stored them, duplicates
// included. Folding them to team identities is a merge rule, and the rules live
// in internal/projectmerge: this package is a dependency of that one, so the
// direction cannot be reversed here.

// attachMemberEntityNames fills EntityNames on each fact of a batch.
//
// A fact with no postings keeps a nil list rather than an empty one, so a caller
// can tell "mentions nothing" from "not read".
func (s *PostgresMergeStore) attachMemberEntityNames(
	ctx context.Context, tx Tx, member MergeProjectMember, qualified string, fresh []MergeFact,
) error {
	if len(fresh) == 0 {
		return nil
	}
	memberCtx, err := WithSchema(ctx, member.Schema)
	if err != nil {
		return err
	}
	unitIDs := make([]string, len(fresh))
	for i := range fresh {
		unitIDs[i] = fresh[i].UnitID
	}
	rows, err := tx.QueryContext(memberCtx, `
		SELECT ue.unit_id::text, e.canonical_name
		FROM `+qualified+`.unit_entities ue
		JOIN `+qualified+`.entities e ON e.id = ue.entity_id
		WHERE ue.unit_id = ANY($1::uuid[]) AND e.bank_id = $2
		ORDER BY ue.unit_id, e.canonical_name`,
		unitIDs, memberBankID(member))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	byUnit := make(map[string][]string, len(fresh))
	for rows.Next() {
		var unitID, name string
		if err := rows.Scan(&unitID, &name); err != nil {
			return err
		}
		byUnit[unitID] = append(byUnit[unitID], name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range fresh {
		fresh[i].EntityNames = byUnit[fresh[i].UnitID]
	}
	return nil
}

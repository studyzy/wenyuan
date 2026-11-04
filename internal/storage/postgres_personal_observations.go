//go:build pgx

package storage

import (
	"context"
)

// retireObservationsForRemovedRowsTx removes the observations resting on any of
// the projected rows a round is about to delete, and requeues the surviving
// co-sources so the next consolidation rebuilds them.
//
// This is the personal store's own copy of delete_stale_observations, because
// the two live behind different objects; the rule is the reference's: an
// observation whose text was synthesised from evidence that has just gone must
// be retired rather than keep answering, and every fact that shared it gets
// another turn. All of this stays inside the aggregate bank — the source banks'
// own consolidated markers are never touched.
func retireObservationsForRemovedRowsTx(ctx context.Context, tx Tx, qualified, bankID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, COALESCE(source_memory_ids, '{}'::uuid[])::text
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'observation' AND source_memory_ids && $2::uuid[]`,
		bankID, factIDs)
	if err != nil {
		return err
	}
	doomed := make([]string, 0)
	removedSet := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		removedSet[id] = true
	}
	requeueSet := map[string]bool{}
	for rows.Next() {
		var observationID, sources string
		if err := rows.Scan(&observationID, &sources); err != nil {
			_ = rows.Close()
			return err
		}
		doomed = append(doomed, observationID)
		for _, source := range decodePostgresTextArray([]byte(sources)) {
			if !removedSet[source] {
				requeueSet[source] = true
			}
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(doomed) > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1 AND observation_id = ANY($2::uuid[])`,
			bankID, doomed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
			bankID, doomed); err != nil {
			return err
		}
	}
	if len(requeueSet) > 0 {
		requeue := make([]string, 0, len(requeueSet))
		for id := range requeueSet {
			requeue = append(requeue, id)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
			 WHERE bank_id = $1 AND id = ANY($2::uuid[]) AND fact_type IN ('world', 'experience')`,
			bankID, requeue); err != nil {
			return err
		}
	}
	return nil
}

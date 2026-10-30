//go:build pgx

package storage

import (
	"context"
)

// deleteObservationsWithoutSourcesTx removes the observations in one bank that
// were made only from the given facts.
//
// It is the personal store's own copy of the merge store's sweep because the two
// live behind different objects; the rule is the same one the reference's
// consolidation relies on: an observation survives while any of its source facts
// does.
func deleteObservationsWithoutSourcesTx(ctx context.Context, tx Tx, qualified, bankID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, COALESCE(source_memory_ids, '{}'::uuid[])::text
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'observation' AND source_memory_ids && $2::uuid[]
		FOR UPDATE`, bankID, factIDs)
	if err != nil {
		return err
	}
	doomed := make([]string, 0)
	doomedSet := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		doomedSet[id] = true
	}
	for rows.Next() {
		var observationID, sources string
		if err := rows.Scan(&observationID, &sources); err != nil {
			_ = rows.Close()
			return err
		}
		remaining := 0
		for _, source := range decodePostgresTextArray([]byte(sources)) {
			if !doomedSet[source] {
				remaining++
			}
		}
		if remaining == 0 {
			doomed = append(doomed, observationID)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(doomed) == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, doomed)
	return err
}

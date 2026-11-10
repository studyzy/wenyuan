//go:build pgx

package storage

import "context"

// Keeping the team bank's entity rows tidy as facts come and go.
//
// A team fact's postings cascade away with the row, so deleting a fact is all it
// takes to stop it naming its entities. What that leaves behind is an entity
// naming nothing — and once the delete has run, which entities those were is no
// longer knowable from the postings, because they are gone. So the candidates are
// read first and handed to the existing maintenance sweep, which deletes an
// entity only when it truly has no posting left and drops the co-occurrences no
// current unit witnesses.
//
// The sweep is the same one the member banks use (GraphMaintainer.PruneEntity).
// Nothing new is scheduled: a team schema is an active tenant, so the
// graph.maintenance job already covers it.

// queueOrphanedEntities records the entities the given facts' postings name, so
// the maintenance sweep re-checks them once the facts are gone.
//
// It must run before the facts are deleted. A fact outside this bank contributes
// nothing — the read is scoped to the bank the round writes.
func queueOrphanedEntities(ctx context.Context, tx Tx, qualified, bankID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+qualified+`.entity_maintenance_queue (bank_id, entity_id)
		SELECT DISTINCT $1::text, ue.entity_id
		FROM `+qualified+`.unit_entities ue
		JOIN `+qualified+`.entities e ON e.id = ue.entity_id
		WHERE ue.unit_id = ANY($2::uuid[]) AND e.bank_id = $1
		ON CONFLICT (bank_id, entity_id) DO NOTHING`,
		bankID, factIDs)
	return err
}

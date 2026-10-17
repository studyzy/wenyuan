package storage

import "context"

// ClearBankMemories removes a bank's memories. An empty factType is the full
// clear: memory units, documents, attachments, observation history, the
// curation archive and the entity registry all go, while the bank row itself
// survives (its configuration and stats are kept). A non-empty factType narrows
// the removal to one memory type, leaving documents, entities and other types
// in place.
//
// The count is taken inside the same transaction as the delete, so a caller's
// before/after diff cannot race a concurrent retain.
func (s *PostgresStore) ClearBankMemories(ctx context.Context, bankID, factType string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	qualified := quoteIdent(s.schema)
	removed, err := deleteBankMemories(ctx, s, tx, qualified, bankID, factType)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

// deleteBankMemories runs the clear statements inside an open transaction and
// returns the number of live memory units removed.
func deleteBankMemories(ctx context.Context, s *PostgresStore, tx Tx, qualified, bankID, factType string) (int, error) {
	// The unit delete decides the reported count, so it is taken first: the
	// links and associations hold foreign keys to the units about to disappear.
	if factType == "" {
		count, err := deleteFullBankContents(ctx, tx, qualified, bankID)
		if err != nil {
			return 0, err
		}
		return count, nil
	}
	return deleteBankMemoriesOfType(ctx, s, tx, qualified, bankID, factType)
}

// deleteFullBankContents removes every domain row a bank holds, in the
// reference's order. The bank row is left alone, so nothing cascades from it
// and every table is cleared explicitly.
func deleteFullBankContents(ctx context.Context, tx Tx, qualified, bankID string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1`, bankID).Scan(&count); err != nil {
		return 0, err
	}
	// Every posting in the bank is about to go, so give back its mention
	// count before the unit_entities rows are deleted.
	if err := releaseEntityPostings(ctx, tx, qualified,
		`ue.unit_id IN (SELECT id FROM `+qualified+`.memory_units WHERE bank_id = $1)`, bankID); err != nil {
		return 0, err
	}
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.documents WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.attachments WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.memory_links WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.unit_entities WHERE unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1)`,
		`DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.observation_history WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.invalidated_memory_units WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.entities WHERE bank_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, statement, bankID); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// deleteBankMemoriesOfType removes one bank's live and archived memories of a
// single fact type. Documents, entities and the other types are untouched,
// because they may still be referenced. Deleting the observation type also
// drops the bank's observation history, whose snapshots no longer cascade.
func deleteBankMemoriesOfType(ctx context.Context, s *PostgresStore, tx Tx, qualified, bankID, factType string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1 AND fact_type = $2`,
		bankID, factType).Scan(&count); err != nil {
		return 0, err
	}
	args := []any{bankID, factType}
	// The postings of the units this type owns are about to go, so give back
	// their mention counts first.
	if err := releaseEntityPostings(ctx, tx, qualified,
		`ue.unit_id IN (SELECT id FROM `+qualified+`.memory_units WHERE bank_id = $1 AND fact_type = $2)`,
		bankID, factType); err != nil {
		return 0, err
	}
	// Units of the other types may point at these; their outgoing edges go with
	// them, so queue the survivors for a relink before the links are deleted.
	ids, err := memoryUnitIDsOfType(ctx, tx, qualified, bankID, factType)
	if err != nil {
		return 0, err
	}
	if err := s.enqueueRelinkVictims(ctx, tx, bankID, ids, false); err != nil {
		return 0, err
	}
	// Every observation resting on one of these units is stale the moment its
	// evidence goes, so it is deleted and the surviving co-sources are queued
	// for re-consolidation. Swept before the delete, in the sweep's lock order,
	// and again after it — the second pass catches an observation a concurrent
	// consolidation committed in between.
	if len(ids) > 0 {
		if _, err := s.deleteStaleObservationsTx(ctx, tx, bankID, ids); err != nil {
			return 0, err
		}
	}
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.memory_links
		 WHERE from_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND fact_type = $2)
		    OR to_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND fact_type = $2)`,
		`DELETE FROM ` + qualified + `.unit_entities
		 WHERE unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND fact_type = $2)`,
		`DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND fact_type = $2`,
		`DELETE FROM ` + qualified + `.invalidated_memory_units WHERE bank_id = $1 AND fact_type = $2`,
	} {
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return 0, err
		}
	}
	if factType == string(TypeObservation) {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1`, bankID); err != nil {
			return 0, err
		}
	}
	// Second sweep: an observation a concurrent consolidation committed between
	// the pre-delete sweep and the delete above would otherwise outlive its
	// evidence.
	if len(ids) > 0 {
		if _, err := s.deleteStaleObservationsTx(ctx, tx, bankID, ids); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// memoryUnitIDsOfType returns one bank's live unit ids of a single fact type,
// the affected set a clear-by-type queues relink victims from.
func memoryUnitIDsOfType(ctx context.Context, tx Tx, qualified, bankID, factType string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id::text FROM `+qualified+`.memory_units WHERE bank_id = $1 AND fact_type = $2`,
		bankID, factType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

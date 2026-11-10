//go:build pgx

package storage

import (
	"context"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// reconcileTeamFacts removes the team facts whose every source has left the
// members' banks.
//
// It runs only when a rebuild completes, and it is what makes a segmented
// rebuild safe: the completing round has not read the earlier segments, so it
// cannot infer "not replayed" from the current batch. The ledger can: a source
// row records the team fact it supports, and the join against the member's own
// memory_units says whether that source still exists. A fact two members both
// carry therefore survives while either copy remains.
//
// The provenance rows of sources that are gone are removed afterwards. Leaving
// them would keep the delete detector firing, turning every later round into a
// rebuild that reconciles the same rows again.
func (s *PostgresMergeStore) reconcileTeamFacts(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	if len(req.Members) == 0 {
		return nil
	}
	live, err := liveSourceUnits(ctx, tx, req)
	if err != nil {
		return err
	}
	teamCtx, err := WithSchema(ctx, req.TeamSchema)
	if err != nil {
		return err
	}
	qualified := quoteIdent(req.TeamSchema)
	// The reconcile a round runs under the cursor rule: a team
	// row whose source has gone is retired.
	if !req.RebuildDone {
		rows, err := tx.QueryContext(teamCtx, `
			SELECT id::text, COALESCE(metadata->>'source_unit_id', '')
			FROM `+qualified+`.memory_units
			WHERE bank_id = $1 AND fact_type = ANY($2)
			  AND metadata->>'origin' = $3`, req.BankID, teamBankTypes, mergeMetadataOrigin)
		if err != nil {
			return err
		}
		doomed := make([]string, 0)
		for rows.Next() {
			var id, sourceUnitID string
			if err := rows.Scan(&id, &sourceUnitID); err != nil {
				_ = rows.Close()
				return err
			}
			if !live[sourceUnitID] {
				doomed = append(doomed, id)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(doomed) > 0 {
			// An observation resting on a doomed fact is stale the moment its
			// evidence goes; the sweep also drops its history rows.
			if err := queueOrphanedEntities(ctx, tx, qualified, req.BankID, doomed); err != nil {
				return err
			}
			if err := deleteStaleObservationsWithoutSources(ctx, tx, qualified, req.BankID, doomed); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
				req.BankID, doomed); err != nil {
				return err
			}
		}
	}
	return deleteDeadLedgerRows(ctx, tx, req)
}

// liveSourceUnits is the set of member facts at least one member still holds.
//
// The set is of unit ids rather than merge keys because a team row is identified
// by the member fact it mirrors: an edited fact keeps its unit id, so its row is
// still supported even though its text — and therefore its merge key — changed.
func liveSourceUnits(ctx context.Context, tx Tx, req FinishMergeRequest) (map[string]bool, error) {
	live := make(map[string]bool)
	for _, member := range req.Members {
		memberCtx, err := WithSchema(ctx, member)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(memberCtx, `
			SELECT DISTINCT l.source_unit_id::text
			FROM `+adminTable("merge_source_unit")+` l
			JOIN `+quoteIdent(member)+`.memory_units m ON m.id = l.source_unit_id
			WHERE l.team_tenant_id = $1 AND l.project_id = $2 AND l.tenant_id = $3
			  AND l.merge_key <> $4`,
			req.TeamID, req.ProjectID, member, tenant.DroppedMergeKey)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var unitID string
			if err := rows.Scan(&unitID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			live[unitID] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// finalizeRebuildTx retires every team row the current semantics did not write,
// after a rebuild's replay has finished and been reconciled.
//
// A rebuild replays the members' banks under the current rules, so when it
// completes the team bank must hold exactly what the replay produced. The rows
// it did not produce are the pre-replay content: facts an older round keyed on
// their text, and observations copied from a member's bank. Both are retired
// here, together with the observations that rested on them, and the ledger is
// emptied so the replay's own rows are the only provenance left.
//
// Without this pass the migration would have no ending: the pre-replay rows are
// not the round's (their metadata has no `origin`), so the next round's unmanaged
// sweep would delete them without retiring the observations that rest on them.
func (s *PostgresMergeStore) finalizeRebuildTx(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	teamCtx, err := WithSchema(ctx, req.TeamSchema)
	if err != nil {
		return err
	}
	qualified := quoteIdent(req.TeamSchema)
	// What the current rules write: the round's own rows, and anything resting
	// on a fact that still lives — the team's consolidated observations
	// included. Those are the team's content; everything else in the bank is
	// pre-replay residue.
	//
	// A rebuild's completing round writes nothing, and it is the round that has
	// to retire: the replay's last write already replaced the members' facts, so
	// an observation a consolidation derived from one of the replaced rows now
	// cites nothing. That check is the smaller one and runs alone.
	if len(req.Replace.Insert) == 0 && len(req.Replace.Remove) == 0 {
		// The replay has reached the end of every member's bank, so every fact
		// the members still hold has been carried. A row whose own source is
		// gone has nothing left to mirror and is retired here.
		live, err := liveSourceUnits(ctx, tx, req)
		if err != nil {
			return err
		}
		stale, err := s.rowsWithDeadSourcesTx(ctx, tx, qualified, req.BankID, live)
		if err != nil {
			return err
		}
		if err := s.retireRows(ctx, tx, qualified, req.BankID, stale, nil); err != nil {
			return err
		}
		orphans, err := s.observationOrphansTx(ctx, tx, qualified, req.BankID)
		if err != nil {
			return err
		}
		return s.retireRows(ctx, tx, qualified, req.BankID, orphans, nil)
	}
	// The retire below runs on the rows the round itself wrote:
	// every row the current semantics did not write is residue from an older
	// one — keyed on its text, or copied from a member's observation — and its
	// own evidence goes with it.
	rows, err := tx.QueryContext(teamCtx, `
		SELECT id::text, COALESCE(source_memory_ids, '{}'::uuid[])::text
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2)
		  AND COALESCE(metadata->>'origin', '') <> $3
		FOR NO KEY UPDATE`, req.BankID, teamBankTypes, mergeMetadataOrigin)
	if err != nil {
		return err
	}
	stale := make([]string, 0)
	evidence := make(map[string][]string)
	for rows.Next() {
		var id, sources string
		if err := rows.Scan(&id, &sources); err != nil {
			_ = rows.Close()
			return err
		}
		stale = append(stale, id)
		evidence[id] = decodePostgresTextArray([]byte(sources))
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := s.retireRows(ctx, tx, qualified, req.BankID, stale, evidence); err != nil {
		return err
	}
	// The replay rewrote every fact's provenance, so the ledger's previous rows
	// would only keep the delete detector firing on sources that are gone.
	return s.finalizeLedger(ctx, tx, req)
}

// rowsWithDeadSourcesTx lists the merge rows whose source fact is gone.
func (s *PostgresMergeStore) rowsWithDeadSourcesTx(
	ctx context.Context, tx Tx, qualified, bankID string, live map[string]bool,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, COALESCE(metadata->>'source_unit_id', '')
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND COALESCE(metadata->>'origin', '') = $2`, bankID, mergeMetadataOrigin)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	stale := make([]string, 0)
	for rows.Next() {
		var id, sourceUnitID string
		if err := rows.Scan(&id, &sourceUnitID); err != nil {
			return nil, err
		}
		if !live[sourceUnitID] {
			stale = append(stale, id)
		}
	}
	return stale, rows.Err()
}

// observationOrphansTx finds the observations a consolidation derived whose
// evidence all went away with the rows the replay replaced.
func (s *PostgresMergeStore) observationOrphansTx(ctx context.Context, tx Tx, qualified, bankID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, COALESCE(source_memory_ids, '{}'::uuid[])::text
		FROM `+qualified+`.memory_units o
		WHERE o.bank_id = $1 AND o.fact_type = 'observation'
		  AND COALESCE(o.metadata->>'origin', '') <> $2
		  AND NOT EXISTS (SELECT 1 FROM `+qualified+`.memory_units s
		                  WHERE s.id = ANY(o.source_memory_ids))`,
		bankID, mergeMetadataOrigin)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	orphans := make([]string, 0)
	for rows.Next() {
		var id, sources string
		if err := rows.Scan(&id, &sources); err != nil {
			return nil, err
		}
		// An observation that never cited anything was not derived from the
		// bank and is not the replay's to retire.
		if len(decodePostgresTextArray([]byte(sources))) > 0 {
			orphans = append(orphans, id)
		}
	}
	return orphans, rows.Err()
}

// retireRows deletes the rows and the observations that rested on them.
//
// The sweep runs first — the reference's own rule — so an observation whose
// evidence is going is retired deliberately rather than orphaned. Links and
// entity postings need no statement of their own: both cascade from
// memory_units. The entities those postings leave bare are queued for the
// maintenance sweep before the delete, because after it they cannot be found.
func (s *PostgresMergeStore) retireRows(
	ctx context.Context, tx Tx, qualified, bankID string, rows []string, evidence map[string][]string,
) error {
	if len(rows) == 0 {
		return nil
	}
	if evidence != nil {
		if err := dropRetiredEvidence(ctx, tx, qualified, bankID, rows, evidence); err != nil {
			return err
		}
	}
	if err := queueOrphanedEntities(ctx, tx, qualified, bankID, rows); err != nil {
		return err
	}
	if err := deleteStaleObservationsWithoutSources(ctx, tx, qualified, bankID, rows); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, rows)
	return err
}

// finalizeLedger drops the provenance rows of sources the members no longer hold.
//
// The replay rewrote every fact's provenance, so an old row for a source that is
// gone would only keep the delete detector firing on it. A row the member still
// holds stays: a replay may not have reached it yet (a fact a teammate's earlier
// round already wrote under the same source), and dropping it would make the
// next round read that fact as new and begin another rebuild.
func (s *PostgresMergeStore) finalizeLedger(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	live, err := liveSourceUnits(ctx, tx, req)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT source_unit_id::text FROM `+adminTable("merge_source_unit")+`
		WHERE team_tenant_id = $1 AND project_id = $2`, req.TeamID, req.ProjectID)
	if err != nil {
		return err
	}
	dead := make([]string, 0)
	for rows.Next() {
		var unitID string
		if err := rows.Scan(&unitID); err != nil {
			_ = rows.Close()
			return err
		}
		if !live[unitID] {
			dead = append(dead, unitID)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(dead) == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		DELETE FROM `+adminTable("merge_source_unit")+`
		WHERE team_tenant_id = $1 AND project_id = $2 AND source_unit_id = ANY($3::uuid[])`,
		req.TeamID, req.ProjectID, dead)
	return err
}

// dropRetiredEvidence rewrites the evidence list of any fact that cites a
// retiring row, keeping the citations that survive.
//
// Without this the sweep that follows would retire every observation citing the
// retired set, the team's own conclusions among them. Only the citations of rows
// about to disappear are dropped: a fact whose evidence still exists keeps it,
// and so keeps its consolidated_at.
func dropRetiredEvidence(
	ctx context.Context, tx Tx, qualified, bankID string, retired []string, evidence map[string][]string,
) error {
	gone := make(map[string]bool, len(retired))
	for _, id := range retired {
		gone[id] = true
	}
	kept := make(map[string][]string)
	for _, id := range retired {
		survivors := make([]string, 0, len(evidence[id]))
		for _, source := range evidence[id] {
			if !gone[source] {
				survivors = append(survivors, source)
			}
		}
		if len(survivors) != len(evidence[id]) {
			kept[id] = survivors
		}
	}
	if len(kept) == 0 {
		return nil
	}
	for id, survivors := range kept {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET source_memory_ids = $3::uuid[]
			 WHERE bank_id = $1 AND id = $2::uuid`,
			bankID, id, survivors); err != nil {
			return err
		}
	}
	return nil
}

// deleteDeadLedgerRows drops provenance rows whose source unit is gone.
func deleteDeadLedgerRows(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	for _, member := range req.Members {
		memberCtx, err := WithSchema(ctx, member)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(memberCtx, `
			DELETE FROM `+adminTable("merge_source_unit")+` l
			WHERE l.team_tenant_id = $1 AND l.project_id = $2 AND l.tenant_id = $3
			  AND NOT EXISTS (
			      SELECT 1 FROM `+quoteIdent(member)+`.memory_units m
			      WHERE m.id = l.source_unit_id
			  )`,
			req.TeamID, req.ProjectID, member); err != nil {
			return err
		}
	}
	return nil
}

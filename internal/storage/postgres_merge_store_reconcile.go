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
	live, err := liveMergeKeys(ctx, tx, req)
	if err != nil {
		return err
	}
	teamCtx, err := WithSchema(ctx, req.TeamSchema)
	if err != nil {
		return err
	}
	qualified := quoteIdent(req.TeamSchema)
	rows, err := tx.QueryContext(teamCtx, `
		SELECT id::text, COALESCE(metadata->>'merge_key', '')
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2)
		  AND metadata->>'merge_key' IS NOT NULL`, req.BankID, mergedFactTypes)
	if err != nil {
		return err
	}
	doomed := make([]string, 0)
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			_ = rows.Close()
			return err
		}
		if !live[key] {
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
		if err := deleteStaleObservationsWithoutSources(ctx, tx, qualified, req.BankID, doomed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
			req.BankID, doomed); err != nil {
			return err
		}
	}
	return deleteDeadLedgerRows(ctx, tx, req)
}

// liveMergeKeys is the set of team merge keys at least one member still holds.
func liveMergeKeys(ctx context.Context, tx Tx, req FinishMergeRequest) (map[string]bool, error) {
	live := make(map[string]bool)
	for _, member := range req.Members {
		memberCtx, err := WithSchema(ctx, member)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(memberCtx, `
			SELECT DISTINCT l.merge_key
			FROM `+adminTable("merge_source_unit")+` l
			JOIN `+quoteIdent(member)+`.memory_units m ON m.id = l.source_unit_id
			WHERE l.team_tenant_id = $1 AND l.project_id = $2 AND l.tenant_id = $3
			  AND l.merge_key <> $4`,
			req.TeamID, req.ProjectID, member, tenant.DroppedMergeKey)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				_ = rows.Close()
				return nil, err
			}
			live[key] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return live, nil
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

//go:build pgx

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// FinishMergeStanza writes one round's output.
//
// Everything a round changes lands in one transaction: the team bank's facts and
// the observations that lose their sources, the ledger, and the member states.
// The write is fenced on the claim stamp, so a round whose model call overlapped
// another process's is refused rather than applied out of order.
func (s *PostgresMergeStore) FinishMergeStanza(ctx context.Context, req FinishMergeRequest) error {
	teamCtx, err := WithSchema(ctx, req.TeamSchema)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The fence: the claim must still be this round's. A round that another
	// process took over finds its stamp replaced and writes nothing.
	fenced, err := s.claimHeld(ctx, tx, req.TeamID, req.ProjectID, req.Stamp)
	if err != nil {
		return err
	}
	if !fenced {
		return ErrMergeClaimLost
	}
	if err := s.applyFacts(teamCtx, tx, req); err != nil {
		return err
	}
	if err := s.replaceLedger(ctx, tx, req); err != nil {
		return err
	}
	if err := s.saveStates(ctx, tx, req); err != nil {
		return err
	}
	return tx.Commit()
}

// claimHeld reports whether this round still owns the project's claim.
func (s *PostgresMergeStore) claimHeld(ctx context.Context, tx Tx, teamID, projectID string, stamp time.Time) (bool, error) {
	var held int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM `+adminTable("merge_state")+`
		WHERE team_tenant_id = $1 AND project_id = $2
		  AND merging_since IS NOT NULL AND merging_since = $3`,
		teamID, projectID, stamp).Scan(&held)
	return held > 0, err
}

// applyFacts rewrites the team bank's managed facts.
//
// The order matters and is not DeleteBank's: that one deletes a whole bank, while
// a round deletes only the facts the answer dropped. Running a bank-wide delete
// here would empty the team's content on every round.
func (s *PostgresMergeStore) applyFacts(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	schema, err := schemaIn(ctx)
	if err != nil {
		return err
	}
	qualified := quoteIdent(schema)
	if len(req.Replace.Remove) > 0 {
		doomed := make([]string, 0, len(req.Replace.Remove))
		for _, fact := range req.Replace.Remove {
			doomed = append(doomed, fact.ID)
		}
		// An observation is removed only when every fact it was made from is
		// gone; one built partly on surviving facts keeps its row and loses the
		// dead ids. This is the reference's own sweep, reused rather than
		// re-implemented: it locks the touched rows in id order, which is what
		// keeps two concurrent rounds from deadlocking.
		if err := s.deleteObservationsWithoutSources(ctx, tx, qualified, req.BankID, doomed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
			req.BankID, doomed); err != nil {
			return err
		}
	}
	for _, fact := range req.Replace.Insert {
		metadata := fmt.Sprintf(`{"merge_key": %q, "merged_at": %q}`, fact.MergeKey, time.Now().UTC().Format(time.RFC3339))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+qualified+`.memory_units (bank_id, document_id, text, fact_type, metadata)
			VALUES ($1, NULL, $2, 'world', $3::jsonb)`,
			req.BankID, fact.Text, metadata); err != nil {
			return err
		}
	}
	return nil
}

// deleteObservationsWithoutSources removes observations built only on the given
// facts.
func (s *PostgresMergeStore) deleteObservationsWithoutSources(
	ctx context.Context, tx Tx, qualified string, bankID string, factIDs []string,
) error {
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

// replaceLedger records the round's provenance.
//
// A rebuild replaces each contributing member's rows wholesale; an incremental
// round only adds the newly consumed sources, so the ledger never loses a row the
// team's content still rests on.
func (s *PostgresMergeStore) replaceLedger(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	for _, tenantID := range req.Replace.RebuildMembers {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM `+adminTable("merge_source_unit")+`
			WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = $3`,
			req.TeamID, req.ProjectID, tenantID); err != nil {
			return err
		}
	}
	seen := make(map[string]bool)
	for tenantID, sources := range req.Replace.Sources {
		for _, source := range sources {
			key := tenantID + "\x00" + source.UnitID
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO `+adminTable("merge_source_unit")+`
				    (team_tenant_id, project_id, tenant_id, source_unit_id, merge_key)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (team_tenant_id, project_id, tenant_id, source_unit_id)
				DO UPDATE SET merge_key = EXCLUDED.merge_key, consumed_at = now()`,
				req.TeamID, req.ProjectID, tenantID, source.UnitID, source.MergeKey); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveStates advances the members' cursors and clears the claim.
func (s *PostgresMergeStore) saveStates(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	now := time.Now().UTC()
	for _, member := range req.Members {
		cursor, ok := req.Cursors[member]
		if !ok {
			if _, err := tx.ExecContext(ctx, `
				UPDATE `+adminTable("merge_state")+`
				SET merging_since = NULL, last_merged_at = $4
				WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = $3`,
				req.TeamID, req.ProjectID, member, now); err != nil {
				return err
			}
			continue
		}
		unitID := cursor.UnitID
		if unitID == "" {
			unitID = zeroUUID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE `+adminTable("merge_state")+`
			SET cursor_updated_at = $4, cursor_unit_id = $5::uuid, last_merged_at = $6,
			    last_error = NULL, fail_count = 0, merging_since = NULL,
			    rebuild_requested = CASE WHEN $7 THEN false ELSE rebuild_requested END
			WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = $3`,
			req.TeamID, req.ProjectID, member, cursor.UpdatedAt, unitID, now, req.Rebuild); err != nil {
			return err
		}
	}
	return nil
}

// AbandonMergeStanza records a round that produced no output, releasing the
// claim so the next round can try.
func (s *PostgresMergeStore) AbandonMergeStanza(ctx context.Context, teamID, projectID string, state MergeStateFailure) error {
	if state.Error == "" && !state.Counting {
		// Releasing the claim is the whole job when the round simply had nothing
		// to do.
		_, err := s.db.ExecContext(ctx, `
			UPDATE `+adminTable("merge_state")+`
			SET merging_since = NULL
			WHERE team_tenant_id = $1 AND project_id = $2 AND merging_since IS NOT NULL`,
			teamID, projectID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE `+adminTable("merge_state")+`
		SET merging_since = NULL,
		    last_error = $3,
		    fail_count = CASE WHEN $4 THEN fail_count + 1 ELSE fail_count END
		WHERE team_tenant_id = $1 AND project_id = $2`,
		teamID, projectID, nullIfEmpty(state.Error), state.Counting)
	return err
}

// nullIfEmpty stores an empty message as NULL.
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ClearMemberProject removes one member's ledger rows and state for one project.
func (s *PostgresMergeStore) ClearMemberProject(ctx context.Context, teamID, projectID, tenantID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM `+adminTable("merge_source_unit")+`
		WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = $3`,
		teamID, projectID, tenantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM `+adminTable("merge_state")+`
		WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = $3`,
		teamID, projectID, tenantID); err != nil {
		return err
	}
	return tx.Commit()
}

// RequestRebuild asks the next round to ignore the cursors.
//
// The flag is stored on every member's state row rather than on the project,
// because merge_state is keyed by the (project, member) pair; a project whose
// members list is empty has nothing to merge and needs no flag.
func (s *PostgresMergeStore) RequestRebuild(ctx context.Context, teamID, projectID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE `+adminTable("merge_state")+`
		SET rebuild_requested = true
		WHERE team_tenant_id = $1 AND project_id = $2`, teamID, projectID)
	return err
}

// ResetMergeState clears the circuit breaker so a halted project resumes.
func (s *PostgresMergeStore) ResetMergeState(ctx context.Context, teamID, projectID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE `+adminTable("merge_state")+`
		SET fail_count = 0, last_error = NULL
		WHERE team_tenant_id = $1 AND project_id = $2`, teamID, projectID)
	return err
}

// MergeStatus reports one project's state for the console.
func (s *PostgresMergeStore) MergeStatus(ctx context.Context, teamID, projectID string) (MergeStatus, error) {
	var status MergeStatus
	var bankID string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.bank_id
		FROM `+adminTable("team_project")+` p
		WHERE p.team_tenant_id = $1 AND p.project_id = $2`, teamID, projectID).Scan(&bankID)
	if errors.Is(err, sql.ErrNoRows) {
		return status, tenant.ErrNotFound
	}
	if err != nil {
		return status, err
	}
	status.ProjectID, status.BankID = projectID, bankID
	var lastMergedAt sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE last_merged_at IS NOT NULL),
		       max(last_merged_at),
		       COALESCE(max(last_error), ''),
		       COALESCE(max(fail_count), 0),
		       count(*) FILTER (WHERE last_merged_at IS NULL)
		FROM `+adminTable("merge_state")+`
		WHERE team_tenant_id = $1 AND project_id = $2`,
		teamID, projectID).Scan(&status.PendingMembers, &lastMergedAt, &status.LastError,
		&status.FailCount, &status.PendingMembers); err != nil {
		return status, err
	}
	if lastMergedAt.Valid {
		value := lastMergedAt.Time
		status.LastMergedAt = &value
		status.Merged = true
	}
	status.Halted = status.FailCount > 0 && status.LastError != ""
	if err := s.countTeamFacts(ctx, teamID, status.BankID, &status); err != nil {
		return status, err
	}
	return status, nil
}

// countTeamFacts counts the team bank's managed facts and the world facts no
// round wrote.
//
// The unmanaged count is what tells an operator a team bank is being written
// directly: those facts are replaced by the next round, and a silent
// disappearance is the hardest kind of data loss to trace.
func (s *PostgresMergeStore) countTeamFacts(ctx context.Context, teamSchema, bankID string, status *MergeStatus) error {
	exists, err := SchemaExists(ctx, s.db, teamSchema)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	qualified := quoteIdent(teamSchema)
	return s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE metadata->>'merge_key' IS NOT NULL),
		       count(*) FILTER (WHERE metadata->>'merge_key' IS NULL)
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'world'`, bankID).Scan(&status.TeamFacts, &status.UnmanagedFacts)
}

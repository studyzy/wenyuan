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
	// Reconciliation runs after the ledger: only then does a fact carried by
	// this batch have a source row that proves it still lives. Reconciling
	// before the insert would delete what the round just wrote.
	if req.Reconcile {
		if err := s.reconcileTeamFacts(ctx, tx, req); err != nil {
			return err
		}
	}
	// A completed rebuild retires what the current semantics did not write — the
	// rows an older round keyed on their text, and every row copied from a
	// member's observation — and rewrites the ledger from the replay.
	//
	// It runs after the write, because a rebuild's last round writes nothing and
	// would otherwise never retire anything, but before saveStates so the flag is
	// cleared only when the retirement landed.
	if req.RebuildDone {
		if err := s.finalizeRebuildTx(ctx, tx, req); err != nil {
			return err
		}
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

// applyFacts writes the team bank's managed facts.
//
// A round both adds and replaces: an insert is a member fact the bank does not
// hold yet, a remove is the row an edited member fact used to produce. Nothing
// else is deleted here — a fact whose source has left the members' banks is the
// rebuild's reconciliation, which runs after the ledger.
func (s *PostgresMergeStore) applyFacts(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	schema, err := schemaIn(ctx)
	if err != nil {
		return err
	}
	qualified := quoteIdent(schema)
	// The defence against a direct write: a fact in the team bank that no round
	// wrote is not part of the team's content, and the next round replaces it. It
	// applies to every merged kind, because a `world`-only sweep would leave a
	// hand-written `experience` or `observation` standing forever.
	if req.UnmanagedFacts > 0 {
		if err := s.removeUnmanagedFacts(ctx, tx, qualified, req.BankID); err != nil {
			return err
		}
	}
	if err := s.removeReplacedFacts(ctx, tx, qualified, req.BankID, req.Replace.Remove); err != nil {
		return err
	}
	mergedAt := time.Now().UTC().Format(time.RFC3339)
	// The ids the inserts generate are what the round's links point at, so each
	// insert returns its row. A tag list is never NULL in the column, and an
	// untagged fact arrives as nil; passing that through would fail the insert
	// rather than write an empty array.
	ids := make(map[string]string, len(req.Replace.Insert))
	for _, fact := range req.Replace.Insert {
		id, err := insertTeamFact(ctx, tx, qualified, req.BankID, fact, mergedAt)
		if err != nil {
			return err
		}
		ids[fact.MergeKey] = id
	}
	if len(req.Replace.Links) > 0 {
		if err := s.applyLinks(ctx, tx, qualified, req.BankID, req.Replace.Links, ids); err != nil {
			return err
		}
	}
	return nil
}

// removeReplacedFacts deletes the team rows an edited member fact no longer
// produces, retiring the observations that rested on them first.
func (s *PostgresMergeStore) removeReplacedFacts(ctx context.Context, tx Tx, qualified, bankID string, rows []TeamFactRow) error {
	if len(rows) == 0 {
		return nil
	}
	doomed := make([]string, len(rows))
	for i, row := range rows {
		doomed[i] = row.ID
	}
	if err := deleteStaleObservationsWithoutSources(ctx, tx, qualified, bankID, doomed); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, doomed)
	return err
}

// mergeSearchVectorExpr is searchVectorExpr over the merge write's bind
// placeholders, under the package's default dictionary.
//
// The merge store writes a schema the store's context does not select and has no
// resolved text-search backend of its own, so it tokenizes under the default.
// That is the same dictionary a deployment without an explicit override uses,
// which is the ordinary case; a deployment that overrode it would give its team
// rows a different dictionary from its member rows.
//
// Each referenced placeholder is cast here: an untyped parameter used only inside
// COALESCE is inferred as text by some positions and left undecided by others, and
// one that PostgreSQL decides is a timestamptz rejects the empty string a Go
// string binds as.
//
// It is the merge store's own copy of the expression rather than a call into the
// store, because the store is behind the pgx tag and this file is not: the merge
// code is the only user, and it is always built with the tag.
var mergeSearchVectorExpr = searchVectorExpr(defaultNativeLanguage, "$2", "$3", "$14::text")

// insertTeamFact writes one team fact and returns its id.
//
// The column list is the member fact's own: text, kind, tags, context, the four
// temporal bounds, the observation scopes, the text signals, the three stamps,
// the evidence and the vector. A merge is a copy, so a column left to its default
// is a column the team bank answers worse than the member's bank does — an
// unembedded row is invisible to the semantic arm, a row without dates is
// invisible to the temporal one, and a row without a text search vector is
// invisible to the keyword one.
func insertTeamFact(ctx context.Context, tx Tx, qualified, bankID string, fact TeamFactRow, mergedAt string) (string, error) {
	factType := fact.FactType
	if factType == "" {
		factType = TypeWorld
	}
	tags := fact.Tags
	if tags == nil {
		tags = []string{}
	}
	sourceMemoryIDs := fact.SourceMemoryIDs
	if sourceMemoryIDs == nil {
		sourceMemoryIDs = []string{}
	}
	var embedding any
	if len(fact.Embedding) > 0 {
		if err := ValidateEmbedding(fact.Embedding); err != nil {
			return "", fmt.Errorf("team fact %q: %w", fact.SourceUnitID, err)
		}
		embedding = EncodeEmbedding(fact.Embedding)
	}
	var scopes any
	if len(fact.ObservationScopes) > 0 {
		scopes = string(fact.ObservationScopes)
	}
	createdAtValue := any(fact.CreatedAt)
	if fact.CreatedAt == nil {
		createdAtValue = time.Now().UTC()
	}
	updatedAtValue := any(fact.UpdatedAt)
	if fact.UpdatedAt.IsZero() {
		updatedAtValue = time.Now().UTC()
	}
	var editedAtValue any
	if fact.EditedAt != nil {
		editedAtValue = *fact.EditedAt
	}
	metadata := fmt.Sprintf(
		`{"origin": %q, "source_unit_id": %q, "source_tenant": %q, "merge_key": %q, "merged_at": %q}`,
		mergeMetadataOrigin, fact.SourceUnitID, fact.SourceTenant, fact.MergeKey, mergedAt)
	var id string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO `+qualified+`.memory_units
			(bank_id, text, context, fact_type, metadata, tags,
			 event_date, occurred_start, occurred_end, mentioned_at, embedding,
			 source_memory_ids, observation_scopes, text_signals,
			 created_at, updated_at, edited_at, document_id, search_vector,
			 consolidated_at, consolidation_failed_at)
		VALUES ($1::text, $2::text, $3::text, $4::text, $5::jsonb, $6::text[],
		        $7::timestamptz, $8::timestamptz, $9::timestamptz, $10::timestamptz, $11::vector,
		        $12::uuid[], $13::jsonb, $14::text,
		        $15::timestamptz, $16::timestamptz, $17::timestamptz, NULL, `+mergeSearchVectorExpr+`,
		        NULL, NULL)
		RETURNING id::text`,
		bankID, fact.Text, fact.Context, string(factType), metadata, tags,
		fact.EventDate, fact.OccurredStart, fact.OccurredEnd, fact.MentionedAt, embedding,
		sourceMemoryIDs, scopes, fact.TextSignals,
		createdAtValue, updatedAtValue, editedAtValue).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// removeUnmanagedFacts deletes the team bank's facts no round wrote, of every
// merged kind, sweeping the observations they supported first.
func (s *PostgresMergeStore) removeUnmanagedFacts(ctx context.Context, tx Tx, qualified, bankID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2)
		  AND metadata->>'merge_key' IS NULL
		FOR UPDATE`, bankID, teamBankTypes)
	if err != nil {
		return err
	}
	orphanIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		orphanIDs = append(orphanIDs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(orphanIDs) == 0 {
		return nil
	}
	if err := deleteStaleObservationsWithoutSources(ctx, tx, qualified, bankID, orphanIDs); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, orphanIDs)
	return err
}

// deleteStaleObservationsWithoutSources removes the bank's observations built
// only on the given facts.
//
// It is the reference's sweep — an observation is stale the moment any source it
// was synthesised from disappears, so it is deleted and the surviving co-sources
// are requeued rather than kept as an answer that no longer holds — applied to a
// schema the caller names, because a merge round writes a schema the store's
// context does not select.
func deleteStaleObservationsWithoutSources(
	ctx context.Context, tx Tx, qualified string, bankID string, factIDs []string,
) error {
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
	deleted := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		deleted[id] = true
	}
	seen := make(map[string]bool)
	remaining := make([]string, 0)
	for rows.Next() {
		var observationID, sources string
		if err := rows.Scan(&observationID, &sources); err != nil {
			_ = rows.Close()
			return err
		}
		doomed = append(doomed, observationID)
		for _, source := range decodePostgresTextArray([]byte(sources)) {
			if deleted[source] || seen[source] {
				continue
			}
			seen[source] = true
			remaining = append(remaining, source)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(doomed) == 0 {
		return nil
	}
	// Lock the outgoing facts, the doomed observations and their surviving
	// co-sources in one id order before writing any of them, so two sweeps over a
	// shared observation cannot deadlock.
	lockIDs := make([]string, 0, len(factIDs)+len(doomed)+len(remaining))
	lockIDs = append(lockIDs, factIDs...)
	lockIDs = append(lockIDs, doomed...)
	lockIDs = append(lockIDs, remaining...)
	if _, err := tx.ExecContext(ctx,
		`SELECT id FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND id = ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE`,
		bankID, lockIDs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return err
	}
	// observation_history is keyed by observation_id and does not cascade from
	// memory_units, so the snapshots go explicitly.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1 AND observation_id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return err
	}
	if len(remaining) == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
		 WHERE bank_id = $1 AND id = ANY($2::uuid[]) AND fact_type IN ('experience', 'world')`,
		bankID, remaining)
	return err
}

// replaceLedger records the round's provenance.
//
// A rebuild replaces each contributing member's rows wholesale; an incremental
// round only adds the newly consumed sources, so the ledger never loses a row the
// team's content still rests on.
func (s *PostgresMergeStore) replaceLedger(ctx context.Context, tx Tx, req FinishMergeRequest) error {
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
//
// A completed round clears every participating member's failure count, whether or
// not that member had facts to consume. Leaving the count on a member the round
// reached but did not advance would leave the project permanently one round short
// of a clean state, and the circuit breaker would keep resting on a project that
// is in fact merging fine.
func (s *PostgresMergeStore) saveStates(ctx context.Context, tx Tx, req FinishMergeRequest) error {
	now := time.Now().UTC()
	for _, member := range req.Members {
		cursor, ok := req.Cursors[member]
		if !ok {
			if _, err := tx.ExecContext(ctx, `
				UPDATE `+adminTable("merge_state")+`
				SET merging_since = NULL, last_merged_at = $4,
				    last_error = NULL, fail_count = 0
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
			req.TeamID, req.ProjectID, member, cursor.UpdatedAt, unitID, now, req.RebuildDone); err != nil {
			return err
		}
	}
	return nil
}

// AbandonMergeStanza records a round that produced no output, releasing the
// claim so the next round can try.
//
// A counted failure also stamps last_attempt_at. The circuit breaker needs to
// know when a project was last tried to decide whether its rest is over, and
// last_merged_at cannot answer that: it only moves on success, so a project that
// has never merged would look like it had never been tried either.
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
		    fail_count = CASE WHEN $4 THEN fail_count + 1 ELSE fail_count END,
		    last_attempt_at = CASE WHEN $4 THEN now() ELSE last_attempt_at END
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
	// The cursors go back to the beginning. A rebuild is a replay of the members'
	// banks from the start, and a replay that continues from the old cursor never
	// revisits the facts already behind it — which is exactly what a rebuild
	// exists to reconsider.
	_, err := s.db.ExecContext(ctx, `
		UPDATE `+adminTable("merge_state")+`
		SET rebuild_requested = true,
		    cursor_updated_at = '-infinity',
		    cursor_unit_id = '00000000-0000-0000-0000-000000000000'::uuid
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

// countTeamFacts counts the team bank's managed facts and the facts no round
// wrote.
//
// The unmanaged count is what tells an operator a team bank is being written
// directly: those facts are replaced by the next round, and a silent
// disappearance is the hardest kind of data loss to trace. It covers every
// merged kind, not just `world`, because the sweep that replaces them does too.
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
		WHERE bank_id = $1 AND fact_type = ANY($2)`, bankID, teamBankTypes).Scan(&status.TeamFacts, &status.UnmanagedFacts)
}

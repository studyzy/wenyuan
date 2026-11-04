//go:build pgx

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The PostgreSQL implementation of MergeStore.
//
// It holds one database handle and addresses two schemas through it: the fixed
// admin schema for the ledger, and whichever tenant schema the context names for
// the member and team banks. Both tables of a round's write therefore live in
// one transaction, which PostgreSQL allows across schemas as long as it is one
// connection — the reason this object owns the handle rather than routing through
// PostgresStore, whose methods each open their own transaction.

// PostgresMergeStore is the merge job's cross-schema store.
type PostgresMergeStore struct {
	db DB
}

// NewPostgresMergeStore returns the merge store over a database handle.
//
// The handle must reach the admin schema and every tenant schema. In this
// service both live in one database by construction, which is what makes a
// single transaction able to span them; a deployment that split the registry onto
// another database would break that, so the assumption is checked by
// TestMergeStoreSpansSchemasInOneDatabase rather than left implicit.
func NewPostgresMergeStore(db DB) *PostgresMergeStore {
	return &PostgresMergeStore{db: db}
}

// adminTable names one ledger table in the admin schema.
func adminTable(table string) string { return quoteIdent(AdminSchema) + "." + quoteIdent(table) }

// schemaIn resolves the tenant schema a statement is about.
//
// MergeStore has no default schema: a merge round is inherently cross-tenant, so
// a context that names none is an error rather than a fallback to some process
// default.
func schemaIn(ctx context.Context) (string, error) {
	schema, ok := SchemaFrom(ctx)
	if !ok {
		return "", ErrSchemaRequired
	}
	return schema, nil
}

// ListMergeProjects returns every active team project that has members.
func (s *PostgresMergeStore) ListMergeProjects(ctx context.Context) ([]MergeProject, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.team_tenant_id, COALESCE(t.display_name, p.team_tenant_id), p.project_id, p.name, p.bank_id,
		       pm.tenant_id,
		       COALESCE(ms.cursor_updated_at, '-infinity'::timestamptz),
		       COALESCE(ms.cursor_unit_id, '00000000-0000-0000-0000-000000000000'::uuid)::text,
		       ms.last_merged_at, COALESCE(ms.last_error, ''), COALESCE(ms.fail_count, 0),
		       ms.merging_since, COALESCE(ms.rebuild_requested, false), (ms.tenant_id IS NOT NULL)
		FROM `+adminTable("team_project")+` p
		JOIN `+adminTable("tenant")+` t ON t.tenant_id = p.team_tenant_id
		JOIN `+adminTable("team_project_member")+` pm
		  ON pm.team_tenant_id = p.team_tenant_id AND pm.project_id = p.project_id
		LEFT JOIN `+adminTable("merge_state")+` ms
		  ON ms.team_tenant_id = p.team_tenant_id AND ms.project_id = p.project_id AND ms.tenant_id = pm.tenant_id
		WHERE t.kind = 'team' AND t.status = 'active' AND t.deleted_at IS NULL
		ORDER BY p.team_tenant_id, p.project_id, ms.last_merged_at NULLS FIRST, pm.tenant_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	projects := make([]MergeProject, 0)
	index := map[string]int{}
	for rows.Next() {
		var teamID, teamName, projectID, name, bankID, memberID, cursorUnitID, lastError, cursorText string
		var cursorUpdatedAt time.Time
		var lastMergedAt, mergingSince sql.NullTime
		var failCount int
		var rebuildRequested, hasLedger bool
		if err := rows.Scan(&teamID, &teamName, &projectID, &name, &bankID, &memberID,
			&cursorText, &cursorUnitID, &lastMergedAt, &lastError, &failCount,
			&mergingSince, &rebuildRequested, &hasLedger); err != nil {
			return nil, err
		}
		cursorUpdatedAt, err = parseCursorTimestamp(cursorText)
		if err != nil {
			return nil, err
		}
		key := teamID + "\x00" + projectID
		position, ok := index[key]
		if !ok {
			projects = append(projects, MergeProject{
				TeamID:     teamID,
				TeamName:   teamName,
				ProjectID:  projectID,
				Name:       name,
				BankID:     bankID,
				TeamSchema: teamID,
			})
			position = len(projects) - 1
			index[key] = position
		}
		member := MergeProjectMember{
			TenantID:  memberID,
			ProjectID: projectID,
			Schema:    memberID,
			HasLedger: hasLedger,
		}
		if hasLedger {
			member.State = tenant.MergeState{
				TeamID:           teamID,
				ProjectID:        projectID,
				TenantID:         memberID,
				CursorUpdatedAt:  cursorUpdatedAt,
				CursorUnitID:     cursorUnitID,
				LastError:        lastError,
				FailCount:        failCount,
				RebuildRequested: rebuildRequested,
			}
			if lastMergedAt.Valid {
				value := lastMergedAt.Time
				member.State.LastMergedAt = &value
			}
			if mergingSince.Valid {
				value := mergingSince.Time
				member.State.MergingSince = &value
			}
		}
		projects[position].Members = append(projects[position].Members, member)
	}
	return projects, rows.Err()
}

// BeginMergeStanza acquires the project's round claim and reads what one round
// needs, all in one transaction.
//
// The advisory lock is transaction-scoped, so it cannot span the model call that
// follows. The claim is carried instead by merge_state.merging_since, which this
// transaction stamps under the lock and FinishMergeStanza clears. Two processes
// therefore cannot read the same round's inputs, and a round whose stamp another
// process replaced cannot write its result.
func (s *PostgresMergeStore) BeginMergeStanza(
	ctx context.Context, teamID, projectID string, opts MergeRoundOptions,
	fn func(stanza MergeStanza) error,
) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	lockA, lockB := tenant.MergeLockName(teamID, projectID)
	var acquired bool
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext($1), hashtext($2))`, lockA, lockB).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	project, err := s.loadProjectTx(ctx, tx, teamID, projectID)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			// The project went away between discovery and the lock.
			return false, nil
		}
		return false, err
	}
	// Re-read the members inside the lock: discovery ran outside it, and a member
	// added or removed meanwhile must not be read from a stale list.
	members, err := s.loadMembersTx(ctx, tx, teamID, projectID)
	if err != nil {
		return false, err
	}
	project.Members = members

	input := MergeStanzaInput{Project: *project}
	input.Previous, err = s.loadPreviousTx(ctx, project)
	if err != nil {
		return false, err
	}
	input.UnmanagedFacts, err = s.countUnmanagedTx(ctx, project)
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.TenantID)
	}
	states, err := s.loadStatesTx(ctx, tx, teamID, projectID, ids)
	if err != nil {
		return false, err
	}

	stamp := time.Now().UTC()
	for i := range input.Project.Members {
		member := &input.Project.Members[i]
		state, ok := states[member.TenantID]
		if !ok {
			state = tenant.MergeState{
				TeamID: teamID, ProjectID: projectID, TenantID: member.TenantID,
				CursorUnitID: "00000000-0000-0000-0000-000000000000",
			}
		}
		member.State = state
		member.HasLedger = ok
		// A live stamp means another process is between its read and its write.
		// A stale one is a crashed round and is taken over.
		if state.MergingSince != nil && stamp.Sub(*state.MergingSince) < opts.StaleAfter {
			return false, nil
		}
	}
	// The circuit breaker: a project that has failed opts.FailThreshold rounds in
	// a row rests for opts.RecoveryInterval, then one round is let through to
	// test whether whatever broke it has cleared. A success resets the count, so
	// a project that keeps failing retries on that cadence instead of on every
	// tick — which is what spent 88 model calls on one unrecoverable answer.
	if opts.FailThreshold > 0 && breakerOpen(states, ids, opts, stamp) {
		return false, nil
	}
	if err := s.claimStamp(ctx, tx, &input.Project, stamp); err != nil {
		return false, err
	}
	// The batch is shared across the project's members: an uneven round is what
	// keeps one prolific writer from starving the rest.
	shared := opts.Batch
	for i := range input.Project.Members {
		member := &input.Project.Members[i]
		entry, err := s.loadMemberInput(ctx, *member, shared, opts.IncludeExperience)
		if err != nil {
			return false, err
		}
		if len(entry.Fresh) > 0 {
			shared -= len(entry.Fresh)
			if shared < 0 {
				shared = 0
			}
		}
		input.Members = append(input.Members, entry)
	}
	stanza := &mergeStanza{input: input, stamp: stamp}
	if err := fn(stanza); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// claimStamp records the round claim for every member of the project.
func (s *PostgresMergeStore) claimStamp(ctx context.Context, tx Tx, project *MergeProject, stamp time.Time) error {
	for _, member := range project.Members {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+adminTable("merge_state")+`
			    (team_tenant_id, project_id, tenant_id, merging_since)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (team_tenant_id, project_id, tenant_id)
			DO UPDATE SET merging_since = EXCLUDED.merging_since`,
			project.TeamID, project.ProjectID, member.TenantID, stamp); err != nil {
			return err
		}
	}
	return nil
}

// loadProjectTx reads one project's identity.
func (s *PostgresMergeStore) loadProjectTx(ctx context.Context, tx Tx, teamID, projectID string) (*MergeProject, error) {
	var name, bankID, teamName string
	err := tx.QueryRowContext(ctx, `
		SELECT p.name, p.bank_id, COALESCE(t.display_name, p.team_tenant_id)
		FROM `+adminTable("team_project")+` p
		JOIN `+adminTable("tenant")+` t ON t.tenant_id = p.team_tenant_id
		WHERE p.team_tenant_id = $1 AND p.project_id = $2`, teamID, projectID).Scan(&name, &bankID, &teamName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tenant.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &MergeProject{
		TeamID: teamID, TeamName: teamName, ProjectID: projectID, Name: name,
		BankID: bankID, TeamSchema: teamID,
	}, nil
}

// loadMembersTx reads one project's members, longest-unmerged first.
func (s *PostgresMergeStore) loadMembersTx(ctx context.Context, tx Tx, teamID, projectID string) ([]MergeProjectMember, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT pm.tenant_id
		FROM `+adminTable("team_project_member")+` pm
		WHERE pm.team_tenant_id = $1 AND pm.project_id = $2
		ORDER BY pm.tenant_id`, teamID, projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := make([]MergeProjectMember, 0)
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		members = append(members, MergeProjectMember{TenantID: tenantID, Schema: tenantID, ProjectID: projectID})
	}
	return members, rows.Err()
}

// loadStatesTx reads the ledger states of the given members.
func (s *PostgresMergeStore) loadStatesTx(ctx context.Context, tx Tx, teamID, projectID string, tenantIDs []string) (map[string]tenant.MergeState, error) {
	states := make(map[string]tenant.MergeState, len(tenantIDs))
	if len(tenantIDs) == 0 {
		return states, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT tenant_id, cursor_updated_at::text, cursor_unit_id::text, last_merged_at,
		       COALESCE(last_error, ''), fail_count, merging_since, rebuild_requested, last_attempt_at
		FROM `+adminTable("merge_state")+`
		WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = ANY($3)`,
		teamID, projectID, tenantIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var state tenant.MergeState
		var lastMergedAt, mergingSince, lastAttemptAt sql.NullTime
		var cursorText string
		if err := rows.Scan(&state.TenantID, &cursorText, &state.CursorUnitID,
			&lastMergedAt, &state.LastError, &state.FailCount, &mergingSince,
			&state.RebuildRequested, &lastAttemptAt); err != nil {
			return nil, err
		}
		state.CursorUpdatedAt, err = parseCursorTimestamp(cursorText)
		if err != nil {
			return nil, err
		}
		state.TeamID, state.ProjectID = teamID, projectID
		if lastMergedAt.Valid {
			value := lastMergedAt.Time
			state.LastMergedAt = &value
		}
		if mergingSince.Valid {
			value := mergingSince.Time
			state.MergingSince = &value
		}
		if lastAttemptAt.Valid {
			value := lastAttemptAt.Time
			state.LastAttemptAt = &value
		}
		states[state.TenantID] = state
	}
	return states, rows.Err()
}

// loadPreviousTx reads the team bank's managed facts.
func (s *PostgresMergeStore) loadPreviousTx(ctx context.Context, project *MergeProject) ([]TeamFactRow, error) {
	teamCtx, err := WithSchema(ctx, project.TeamSchema)
	if err != nil {
		return nil, err
	}
	qualified := quoteIdent(project.TeamSchema)
	rows, err := s.db.QueryContext(teamCtx, `
		SELECT id::text, text, COALESCE(metadata->>'merge_key', ''), fact_type
		FROM `+qualified+`.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2) AND metadata->>'merge_key' IS NOT NULL
		ORDER BY created_at, id`, project.BankID, mergedFactTypes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	facts := make([]TeamFactRow, 0)
	for rows.Next() {
		var fact TeamFactRow
		if err := rows.Scan(&fact.ID, &fact.Text, &fact.MergeKey, &fact.FactType); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// memberBankID is the bank a member's contribution lives in.
// It is the project's name under the member's own tenant, which is what the
// two-part bank id means: "<tenant>::<project name>".
func memberBankID(member MergeProjectMember) string {
	return tenant.ProjectBankID(member.TenantID, member.ProjectID)
}

// zeroUUID is the lower bound of the (updated_at, id) cursor.
const zeroUUID = "00000000-0000-0000-0000-000000000000"

// mergeStanza is the read-only view one round gets.
type mergeStanza struct {
	input MergeStanzaInput
	stamp time.Time
}

// Input returns what the round read.
func (m *mergeStanza) Input() MergeStanzaInput { return m.input }

// ClaimStamp is the value written to merging_since, which FinishMergeStanza
// echoes back so a superseded round cannot write.
func (m *mergeStanza) ClaimStamp() time.Time { return m.stamp }

// parseCursorTimestamp reads a cursor's stored value.
//
// The cursor's lower bound is '-infinity', which the driver hands back as a
// string rather than a time; a real timestamp arrives formatted by PostgreSQL.
// Both mean the same thing to the caller: no position yet is the zero time, which
// MergeCursor.Before treats as "read from the beginning".
func parseCursorTimestamp(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "-infinity") {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		// PostgreSQL renders a timestamptz in the session's own format, which is
		// not RFC 3339 unless the session asked for it.
		value, err = time.Parse("2006-01-02 15:04:05.999999-07", trimmed)
		if err != nil {
			return time.Time{}, fmt.Errorf("read merge cursor %q: %w", raw, err)
		}
	}
	return value.UTC(), nil
}

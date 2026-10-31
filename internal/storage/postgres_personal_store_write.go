//go:build pgx

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// FinishPersonalRound writes one round's output.
//
// Everything a round changes lands in one transaction: the aggregate bank's
// facts and the observations that lose their sources, the ledger, the per-bank
// watermarks, the directory projection and the state row. The write is fenced on
// the claim stamp, so a round whose model call overlapped another process's is
// refused rather than applied out of order.
func (s *PostgresPersonalStore) FinishPersonalRound(ctx context.Context, req FinishPersonalRequest) error {
	schema := req.Schema
	if schema == "" {
		resolved, err := schemaForTenant(ctx, req.TenantID)
		if err != nil {
			return err
		}
		schema = resolved
	}
	if err := validSchemaNameOrErr(schema); err != nil {
		return err
	}
	qualified := quoteIdent(schema)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	held, err := s.claimHeldTx(ctx, tx, req.TenantID, req.Stamp)
	if err != nil {
		return err
	}
	if !held {
		return ErrPersonalClaimLost
	}
	if err := s.applyPersonalFacts(ctx, tx, qualified, req); err != nil {
		return err
	}
	if err := s.applyPersonalLedger(ctx, tx, req); err != nil {
		return err
	}
	if err := s.applyPersonalCursors(ctx, tx, req); err != nil {
		return err
	}
	if err := s.applyPersonalDirectory(ctx, tx, req); err != nil {
		return err
	}
	if err := s.completePersonalState(ctx, tx, req); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureAggregateBank makes the aggregate bank a bank like any other.
//
// Writing its facts is not enough: every bank-scoped surface — the bank list the
// console shows, a read addressed to the aggregate, a write to it — resolves the
// bank through the banks table first, so an aggregate holding facts but no bank
// row is invisible to all of them. Creating the row is therefore part of the
// round that first writes content, not a separate setup step somebody has to
// remember.
//
// The row is created, never replaced: a bank the person already owns under this
// id keeps its name, disposition and mission, which is what makes the second and
// every later round a no-op here.
func (s *PostgresPersonalStore) ensureAggregateBank(ctx context.Context, tx Tx, qualified, bankID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+qualified+`.banks (bank_id, name, mission, config)
		VALUES ($1, $2, $3, '{}'::jsonb)
		ON CONFLICT (bank_id) DO NOTHING`,
		bankID, bankID, aggregateMission)
	return err
}

// aggregateMission is what the aggregate bank's mission says, so a reader of the
// bank that never sees this feature still learns what fills it.
const aggregateMission = "结论来自该租户其余各个 bank 的汇总，由后台任务生成，不直接接受人工写入。"

// claimHeldTx reports whether this round still owns the tenant's claim.
func (s *PostgresPersonalStore) claimHeldTx(ctx context.Context, tx Tx, tenantID string, stamp time.Time) (bool, error) {
	var held int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM `+adminTable("personal_state")+`
		WHERE tenant_id = $1 AND merging_since IS NOT NULL AND merging_since = $2`,
		tenantID, stamp).Scan(&held)
	return held > 0, err
}

// applyPersonalFacts rewrites the aggregate bank's derived facts.
//
// The order matters and is not DeleteBank's: that one deletes a whole bank, while
// a round deletes only the facts the answer dropped. Running a bank-wide delete
// here would empty the aggregate on every round.
func (s *PostgresPersonalStore) applyPersonalFacts(ctx context.Context, tx Tx, qualified string, req FinishPersonalRequest) error {
	if len(req.Remove) > 0 {
		doomed := make([]string, 0, len(req.Remove))
		for _, fact := range req.Remove {
			doomed = append(doomed, fact.ID)
		}
		// An observation is removed only when every fact it was made from is
		// gone; one built partly on surviving facts keeps its row.
		if err := deleteObservationsWithoutSourcesTx(ctx, tx, qualified, req.BankID, doomed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
			req.BankID, doomed); err != nil {
			return err
		}
	}
	// The defence against a direct write: a world fact in the aggregate bank that
	// no round wrote is not part of the aggregate, and the next round replaces it.
	// Its observations go with it, by the same rule the explicit removals use.
	if req.UnmanagedFacts > 0 {
		var orphanIDs []string
		rows, err := tx.QueryContext(ctx, `
			SELECT id::text FROM `+qualified+`.memory_units
			WHERE bank_id = $1 AND fact_type = 'world'
			  AND COALESCE(metadata->>'origin', '') <> $2
			FOR UPDATE`, req.BankID, tenant.OriginAggregate)
		if err != nil {
			return err
		}
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
		if len(orphanIDs) > 0 {
			if err := deleteObservationsWithoutSourcesTx(ctx, tx, qualified, req.BankID, orphanIDs); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
				req.BankID, orphanIDs); err != nil {
				return err
			}
		}
	}
	mergedAt := req.Stamp.UTC().Format(time.RFC3339)
	for _, fact := range req.Insert {
		banks := req.FactBanks[fact.MergeKey]
		sources, err := json.Marshal(sourcesOf(req, fact.MergeKey))
		if err != nil {
			return err
		}
		banksJSON, err := json.Marshal(banks)
		if err != nil {
			return err
		}
		topicsJSON, err := json.Marshal(topicsOf(req, fact.MergeKey))
		if err != nil {
			return err
		}
		metadata := fmt.Sprintf(
			`{"origin": %q, "merge_key": %q, "merged_at": %q, "sources": %s, "banks": %s, "topics": %s}`,
			tenant.OriginAggregate, fact.MergeKey, mergedAt, sources, banksJSON, topicsJSON)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+qualified+`.memory_units (bank_id, document_id, text, fact_type, metadata)
			VALUES ($1, NULL, $2, 'world', $3::jsonb)`,
			req.BankID, fact.Text, metadata); err != nil {
			return err
		}
	}
	return nil
}

// personalSourceRef is one provenance entry on an aggregate fact.
type personalSourceRef struct {
	UnitID string `json:"unit_id"`
	BankID string `json:"bank_id"`
}

// sourcesOf lists the provenance entries an aggregate fact carries.
func sourcesOf(req FinishPersonalRequest, mergeKey string) []personalSourceRef {
	out := make([]personalSourceRef, 0)
	for _, row := range req.Sources {
		if row.MergeKey != mergeKey {
			continue
		}
		out = append(out, personalSourceRef{UnitID: row.UnitID, BankID: row.BankID})
	}
	return out
}

// topicsOf lists the topics an aggregate fact belongs to.
func topicsOf(req FinishPersonalRequest, mergeKey string) []string {
	out := make([]string, 0)
	for _, topic := range req.Topics {
		if topic.MergeKey == mergeKey {
			out = append(out, topic.Name)
		}
	}
	return out
}

// applyPersonalLedger records the round's provenance.
//
// A rebuild replaces the tenant's rows wholesale; an incremental round only adds
// the newly consumed sources, so the ledger never loses a row the aggregate
// content still rests on.
func (s *PostgresPersonalStore) applyPersonalLedger(ctx context.Context, tx Tx, req FinishPersonalRequest) error {
	if req.Rebuild {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+adminTable("personal_source")+` WHERE tenant_id = $1`, req.TenantID); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(req.Sources))
	for _, source := range req.Sources {
		if seen[source.UnitID] {
			continue
		}
		seen[source.UnitID] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+adminTable("personal_source")+`
			    (tenant_id, source_unit_id, merge_key, bank_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, source_unit_id)
			DO UPDATE SET merge_key = EXCLUDED.merge_key, bank_id = EXCLUDED.bank_id, consumed_at = now()`,
			req.TenantID, source.UnitID, source.MergeKey, source.BankID); err != nil {
			return err
		}
	}
	return nil
}

// applyPersonalCursors advances the source banks' watermarks.
func (s *PostgresPersonalStore) applyPersonalCursors(ctx context.Context, tx Tx, req FinishPersonalRequest) error {
	now := time.Now().UTC()
	for bankID, cursor := range req.Cursors {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+adminTable("personal_bank_state")+`
			    (tenant_id, source_bank_id, cursor_updated_at, cursor_unit_id, last_merged_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, source_bank_id)
			DO UPDATE SET cursor_updated_at = EXCLUDED.cursor_updated_at,
			              cursor_unit_id = EXCLUDED.cursor_unit_id,
			              last_merged_at = EXCLUDED.last_merged_at`,
			req.TenantID, bankID, cursor.UpdatedAt, cursor.UnitID, now); err != nil {
			return err
		}
	}
	return nil
}

// applyPersonalDirectory replaces the tenant's navigation index.
//
// The index is projected from the round's own output, which is why it is
// rewritten wholesale: a topic that no aggregate fact carries any more must not
// survive in the directory.
func (s *PostgresPersonalStore) applyPersonalDirectory(ctx context.Context, tx Tx, req FinishPersonalRequest) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+adminTable("personal_link")+` WHERE tenant_id = $1`, req.TenantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+adminTable("personal_topic")+` WHERE tenant_id = $1`, req.TenantID); err != nil {
		return err
	}
	weights := make(map[string]int, len(req.FactBanks))
	for _, banks := range req.FactBanks {
		for _, bank := range banks {
			weights[bank]++
		}
	}
	for bank, weight := range weights {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+adminTable("personal_link")+` (tenant_id, to_bank_id, weight)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, to_bank_id) DO UPDATE SET weight = EXCLUDED.weight, updated_at = now()`,
			req.TenantID, bank, weight); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(req.Topics))
	for _, topic := range req.Topics {
		key := topic.Name + "\x00" + topic.BankID
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+adminTable("personal_topic")+` (tenant_id, topic, bank_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, topic, bank_id) DO UPDATE SET weight = personal_topic.weight + 1, updated_at = now()`,
			req.TenantID, topic.Name, topic.BankID); err != nil {
			return err
		}
	}
	return nil
}

// completePersonalState records a successful round and releases the claim.
func (s *PostgresPersonalStore) completePersonalState(ctx context.Context, tx Tx, req FinishPersonalRequest) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE `+adminTable("personal_state")+`
		SET merging_since = NULL, last_round_at = now(), last_error = NULL,
		    fail_count = 0, rebuild_requested = false
		WHERE tenant_id = $1`, req.TenantID)
	return err
}

// AbandonPersonalRound records a round that produced no output.
//
// It clears the claim in every case: a round that found nothing to do has still
// released the tenant, and a round that failed has recorded why.
func (s *PostgresPersonalStore) AbandonPersonalRound(ctx context.Context, tenantID string, failure PersonalFailure) error {
	if failure.Error == "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE `+adminTable("personal_state")+`
			SET merging_since = NULL, last_round_at = now()
			WHERE tenant_id = $1`, tenantID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO `+adminTable("personal_state")+` (tenant_id, merging_since, last_round_at, last_error, fail_count)
		VALUES ($1, NULL, now(), $2, CASE WHEN $3::boolean THEN 1 ELSE 0 END)
		ON CONFLICT (tenant_id) DO UPDATE
		SET merging_since = NULL, last_round_at = now(), last_error = EXCLUDED.last_error,
		    fail_count = CASE WHEN $3::boolean THEN personal_state.fail_count + 1 ELSE personal_state.fail_count END`,
		tenantID, failure.Error, failure.Counting)
	return err
}

// RequestRebuild asks the next round to ignore the watermarks.
func (s *PostgresPersonalStore) RequestRebuild(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO `+adminTable("personal_state")+` (tenant_id, rebuild_requested)
		VALUES ($1, true)
		ON CONFLICT (tenant_id) DO UPDATE SET rebuild_requested = true, fail_count = 0, last_error = NULL`,
		tenantID)
	return err
}

// ResetPersonalState clears the circuit breaker so a halted tenant resumes.
func (s *PostgresPersonalStore) ResetPersonalState(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO `+adminTable("personal_state")+` (tenant_id, fail_count, last_error, merging_since)
		VALUES ($1, 0, NULL, NULL)
		ON CONFLICT (tenant_id) DO UPDATE SET fail_count = 0, last_error = NULL, merging_since = NULL`,
		tenantID)
	return err
}

// PersonalStatus reports one tenant's state for the console.
func (s *PostgresPersonalStore) PersonalStatus(ctx context.Context, tenantID string, sharedSchema bool) (PersonalStatus, error) {
	status := PersonalStatus{BankID: tenant.PersonalBankID(tenantID), SharedSchema: sharedSchema, Kind: "personal"}
	var kind, state string
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, status FROM `+adminTable("tenant")+` WHERE tenant_id = $1`, tenantID).Scan(&kind, &state)
	_ = state
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A single-tenant deployment has no registry row; its one schema takes an
		// aggregate like any person does.
		status.Kind = "personal"
	case err != nil:
		return status, err
	default:
		status.Kind = kind
	}
	if status.Kind == "team" {
		// Refused rather than reported as "no aggregate": the management plane
		// answers a team from its members' aggregates instead, and an empty
		// status would read as a team whose memory stopped working.
		return status, ErrPersonalAggregateIsTeam
	}
	status.Aggregate = true

	var (
		lastRound sql.NullTime
		adopted   sql.NullBool
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT last_round_at, COALESCE(last_error, ''), COALESCE(fail_count, 0),
		       COALESCE(rebuild_requested, false), COALESCE(adopted_preexisting, false)
		FROM `+adminTable("personal_state")+` WHERE tenant_id = $1`, tenantID).
		Scan(&lastRound, &status.LastError, &status.FailCount, &status.RebuildRequested, &adopted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return status, err
	}
	if lastRound.Valid {
		value := lastRound.Time
		status.LastRoundAt = &value
	}
	status.AdoptedPreexisting = adopted.Bool
	status.Halted = status.LastError != "" && status.FailCount >= DefaultPersonalFailThreshold
	if !schemaPresent(ctx, s.db, tenantID) {
		// The schema is gone (a purged tenant): the ledger rows went with it.
		return status, nil
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+quoteIdent(tenantID)+`.banks WHERE bank_id <> $1`,
		status.BankID).Scan(&status.SourceBanks); err != nil {
		return status, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM `+adminTable("personal_bank_state")+` WHERE tenant_id = $1`,
		tenantID).Scan(&status.PendingBanks); err != nil {
		return status, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE COALESCE(metadata->>'origin', '') = $2),
		       count(*) FILTER (WHERE COALESCE(metadata->>'origin', '') <> $2)
		FROM `+quoteIdent(tenantID)+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'world'`,
		status.BankID, tenant.OriginAggregate).Scan(&status.AggregateFacts, &status.UserFacts); err != nil {
		return status, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+adminTable("personal_link")+` WHERE tenant_id = $1`, tenantID).
		Scan(&status.Links); err != nil {
		return status, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+adminTable("personal_topic")+` WHERE tenant_id = $1`, tenantID).
		Scan(&status.Topics); err != nil {
		return status, err
	}
	return status, nil
}

// PersonalDirectory returns the navigation index a completed round projected.
func (s *PostgresPersonalStore) PersonalDirectory(ctx context.Context, tenantID string) (PersonalDirectory, error) {
	directory := PersonalDirectory{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT to_bank_id, weight FROM `+adminTable("personal_link")+`
		WHERE tenant_id = $1 ORDER BY weight DESC, to_bank_id`, tenantID)
	if err != nil {
		return directory, err
	}
	for rows.Next() {
		var link PersonalLink
		if err := rows.Scan(&link.BankID, &link.Weight); err != nil {
			_ = rows.Close()
			return directory, err
		}
		directory.Links = append(directory.Links, link)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return directory, err
	}
	rows, err = s.db.QueryContext(ctx, `
		SELECT topic, bank_id FROM `+adminTable("personal_topic")+`
		WHERE tenant_id = $1 ORDER BY topic, bank_id`, tenantID)
	if err != nil {
		return directory, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var topic PersonalTopic
		if err := rows.Scan(&topic.Name, &topic.BankID); err != nil {
			return directory, err
		}
		directory.Topics = append(directory.Topics, topic)
	}
	return directory, rows.Err()
}

// validSchemaNameOrErr reports whether a schema name may be quoted into a
// statement.
func validSchemaNameOrErr(schema string) error {
	if !validSchemaName(schema) {
		return fmt.Errorf("%w: %q", ErrInvalidSchema, schema)
	}
	return nil
}

// schemaPresent reports whether a tenant schema exists.
func schemaPresent(ctx context.Context, db DB, schema string) bool {
	exists, err := SchemaExists(ctx, db, schema)
	return err == nil && exists
}

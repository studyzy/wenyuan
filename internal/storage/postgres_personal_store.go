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

// The PostgreSQL implementation of PersonalStore.
//
// It holds one database handle and addresses two schemas through it: the fixed
// admin schema for the ledger, and whichever tenant schema the context names —
// the person's own, which holds both the source banks and the aggregate bank.
// A round's write therefore lands in one transaction, which PostgreSQL allows
// across schemas as long as it is one connection.

// PostgresPersonalStore is the personal rollup job's store.
type PostgresPersonalStore struct {
	db DB
}

// NewPostgresPersonalStore returns the personal aggregate store over a database
// handle.
//
// The handle must reach the admin schema and every tenant schema; in this
// service both live in one database by construction, which is what makes a
// single transaction able to span them.
func NewPostgresPersonalStore(db DB) *PostgresPersonalStore {
	return &PostgresPersonalStore{db: db}
}

// personalLockName is the advisory-lock name of one tenant's round.
//
// The two halves are passed as separate hashtext arguments so no single string
// can collide with another tenant's by concatenation.
func personalLockName(tenantID string) (string, string) {
	return "wenyuan:personal-rollup", tenantID
}

// ListRollupTenants returns the active personal tenants.
//
// A single-tenant deployment has no registry rows — its one schema is the
// deployment's own — so the list is that schema, named after the deployment's
// configured one. A team is not a person: its banks are merged into a project's
// team bank by project.merge, and a second aggregate over them would be a
// conflicting view of the same content.
func (s *PostgresPersonalStore) ListRollupTenants(ctx context.Context) ([]tenant.Tenant, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id FROM `+adminTable("tenant")+`
		WHERE status = 'active' AND kind = 'personal' AND deleted_at IS NULL
		ORDER BY tenant_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]tenant.Tenant, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, tenant.Tenant{ID: id, Schema: id})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	// No registry: this is the single-tenant deployment, whose one schema is the
	// one the context names. A context that names none is not this store's to
	// guess, so the round simply finds nothing to do.
	fallback, ok := SchemaFrom(ctx)
	if !ok {
		return nil, nil
	}
	exists, err := SchemaExists(ctx, s.db, fallback)
	if err != nil || !exists {
		return nil, err
	}
	return []tenant.Tenant{{ID: fallback, Schema: fallback}}, nil
}

// BeginPersonalRound acquires the tenant's claim and reads what a round needs,
// all in one transaction.
func (s *PostgresPersonalStore) BeginPersonalRound(
	ctx context.Context, tenantID string, batch int, staleAfter time.Duration,
	fn func(input PersonalRoundInput, claim time.Time) error,
) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	schema, err := schemaForTenant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	// Every read below names its schema explicitly, but the context has to carry
	// it too: a source bank's facts are read in that tenant's own schema.
	ctx, err = WithSchema(ctx, schema)
	if err != nil {
		return false, err
	}
	lockA, lockB := personalLockName(tenantID)
	var acquired bool
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext($1), hashtext($2))`, lockA, lockB).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}

	state, err := s.loadStateTx(ctx, tx, tenantID)
	if err != nil {
		return false, err
	}
	stamp := time.Now().UTC()
	// A live stamp means another process is between its read and its write. A
	// stale one is a crashed round and is taken over.
	if state.MergingSince != nil && stamp.Sub(*state.MergingSince) < staleAfter {
		return false, nil
	}
	// A halted tenant is skipped without a claim: the next round would only fail
	// the same way, and an operator resets it deliberately.
	if state.Halted {
		return false, nil
	}
	if err := s.claimTx(ctx, tx, tenantID, stamp); err != nil {
		return false, err
	}

	self := tenant.PersonalBankID(tenantID)
	input := PersonalRoundInput{
		TenantID: tenantID, Schema: schema, BankID: self,
		RebuildRequested: state.RebuildRequested, Halted: state.Halted,
	}
	input.Previous, err = s.previousTx(ctx, tx, schema, self)
	if err != nil {
		return false, err
	}
	input.Sources, err = s.sourcesTx(ctx, tx, schema, self)
	if err != nil {
		return false, err
	}
	input.SourceDeleted, err = s.sourceDeletedTx(ctx, tx, schema, tenantID)
	if err != nil {
		return false, err
	}
	if state.RebuildRequested || input.SourceDeleted {
		// A rebuild reads every source's facts from the beginning, so the batch
		// bounds it as a whole.
		input.Facts, err = s.rebuildFactsTx(ctx, tx, input.Sources)
	} else {
		input.Facts, err = s.freshFactsTx(ctx, tx, input.Sources, batch)
	}
	if err != nil {
		return false, err
	}
	// A tenant whose aggregate already holds content must have its bank row even
	// when this round has nothing new to write: a caught-up tenant takes the
	// no-write path, and an aggregate whose content predates the row would
	// otherwise stay invisible to the bank list for as long as nothing changes.
	if len(input.Previous) > 0 || len(input.Facts) > 0 {
		if err := s.ensureAggregateBank(ctx, tx, quoteIdent(schema), self); err != nil {
			return false, err
		}
	}
	input.Topics, err = s.topicsTx(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if input.UnmanagedFacts, err = s.unmanagedTx(ctx, schema, self); err != nil {
		return false, err
	}
	if err := fn(input, stamp); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// topicsTx reads the tenant's vocabulary in topic order.
func (s *PostgresPersonalStore) topicsTx(ctx context.Context, tenantID string) ([]PersonalTopic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT topic, bank_id FROM `+adminTable("personal_topic")+`
		WHERE tenant_id = $1 ORDER BY topic, bank_id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PersonalTopic
	for rows.Next() {
		var topic PersonalTopic
		if err := rows.Scan(&topic.Name, &topic.BankID); err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

// unmanagedTx counts the aggregate bank's world facts no round wrote.
func (s *PostgresPersonalStore) unmanagedTx(ctx context.Context, schema, bankID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM `+quoteIdent(schema)+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'world'
		  AND COALESCE(metadata->>'origin', '') <> $2`, bankID, tenant.OriginAggregate).Scan(&count)
	return count, err
}

// personalState is the row BeginPersonalRound works from.
type personalState struct {
	MergingSince     *time.Time
	FailCount        int
	Halted           bool
	RebuildRequested bool
	Adopted          bool
}

// loadStateTx reads one tenant's state row, defaulting one that does not exist.
func (s *PostgresPersonalStore) loadStateTx(ctx context.Context, tx Tx, tenantID string) (personalState, error) {
	var (
		state                    personalState
		halted, rebuild, adopted sql.NullBool
		merging                  sql.NullTime
	)
	err := tx.QueryRowContext(ctx, `
		SELECT merging_since, fail_count, rebuild_requested, adopted_preexisting,
		       (last_error IS NOT NULL AND last_error <> '')
		FROM `+adminTable("personal_state")+` WHERE tenant_id = $1`, tenantID).
		Scan(&merging, &state.FailCount, &rebuild, &adopted, &halted)
	if errors.Is(err, sql.ErrNoRows) {
		return personalState{}, nil
	}
	if err != nil {
		return personalState{}, err
	}
	if merging.Valid {
		state.MergingSince = &merging.Time
	}
	state.Halted = halted.Bool
	state.RebuildRequested = rebuild.Bool
	state.Adopted = adopted.Bool
	return state, nil
}

// claimTx stamps the round's claim.
func (s *PostgresPersonalStore) claimTx(ctx context.Context, tx Tx, tenantID string, stamp time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+adminTable("personal_state")+` (tenant_id, merging_since)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET merging_since = EXCLUDED.merging_since`,
		tenantID, stamp)
	return err
}

// previousTx reads the aggregate bank's current world facts.
func (s *PostgresPersonalStore) previousTx(ctx context.Context, tx Tx, schema, bankID string) ([]PersonalPreviousFact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, text, COALESCE(metadata->>'merge_key', '')
		FROM `+quoteIdent(schema)+`.memory_units
		WHERE bank_id = $1 AND fact_type = 'world'
		ORDER BY created_at, id`, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PersonalPreviousFact
	for rows.Next() {
		var fact PersonalPreviousFact
		if err := rows.Scan(&fact.ID, &fact.Text, &fact.MergeKey); err != nil {
			return nil, err
		}
		if fact.MergeKey == "" {
			fact.MergeKey = tenant.MergeKey(fact.Text)
		}
		out = append(out, fact)
	}
	return out, rows.Err()
}

// sourcesTx lists the tenant's source banks, the one that contributed longest ago
// first.
//
// The aggregate bank itself is never a source: it is written by the round, and
// reading it back as input would fold conclusions into conclusions.
func (s *PostgresPersonalStore) sourcesTx(ctx context.Context, tx Tx, schema, selfBankID string) ([]PersonalSourceBank, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT b.bank_id,
		       COALESCE(bs.cursor_updated_at, '-infinity'::timestamptz)::text,
		       COALESCE(bs.cursor_unit_id, '00000000-0000-0000-0000-000000000000'::uuid)::text,
		       bs.last_merged_at, (bs.tenant_id IS NOT NULL)
		FROM `+quoteIdent(schema)+`.banks b
		LEFT JOIN `+adminTable("personal_bank_state")+` bs
		  ON bs.tenant_id = $1 AND bs.source_bank_id = b.bank_id
		WHERE b.bank_id <> $2
		ORDER BY bs.last_merged_at NULLS FIRST, b.bank_id`, tenantIDOf(ctx), selfBankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PersonalSourceBank
	for rows.Next() {
		var (
			bank      PersonalSourceBank
			cursorAt  string
			cursorID  string
			mergedAt  sql.NullTime
			hasLedger bool
		)
		if err := rows.Scan(&bank.BankID, &cursorAt, &cursorID, &mergedAt, &hasLedger); err != nil {
			return nil, err
		}
		// The cursor's lower bound is '-infinity', which the driver hands back as
		// text rather than a time; the zero time means "read from the beginning".
		updatedAt, err := parseCursorTimestamp(cursorAt)
		if err != nil {
			return nil, err
		}
		bank.Cursor = MergeCursor{UpdatedAt: updatedAt, UnitID: cursorID}
		if mergedAt.Valid {
			value := mergedAt.Time
			bank.LastMergedAt = &value
		}
		bank.HasLedger = hasLedger
		out = append(out, bank)
	}
	return out, rows.Err()
}

// tenantIDOf is the tenant id the context was scoped to.
//
// A tenant's schema is named after its tenant id by construction, so the
// context's schema is the id the ledger is keyed by.
func tenantIDOf(ctx context.Context) string {
	schema, _ := SchemaFrom(ctx)
	return schema
}

// schemaForTenant resolves the schema a tenant's banks live in.
func schemaForTenant(ctx context.Context, tenantID string) (string, error) {
	if schema, ok := SchemaFrom(ctx); ok && schema == tenantID {
		return schema, nil
	}
	if !validSchemaName(tenantID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSchema, tenantID)
	}
	return tenantID, nil
}

// freshFactsTx reads the new facts of each source bank, oldest bank first, until
// the round's batch is spent.
func (s *PostgresPersonalStore) freshFactsTx(ctx context.Context, tx Tx, banks []PersonalSourceBank, batch int) ([]PersonalSourceFact, error) {
	if batch <= 0 {
		return nil, nil
	}
	remaining := batch
	var out []PersonalSourceFact
	for _, bank := range banks {
		if remaining <= 0 {
			break
		}
		facts, err := s.factsAfterTx(ctx, tx, bank, remaining)
		if err != nil {
			return nil, err
		}
		out = append(out, facts...)
		remaining -= len(facts)
	}
	return out, nil
}

// rebuildFactsTx reads every source bank's facts from the beginning.
func (s *PostgresPersonalStore) rebuildFactsTx(ctx context.Context, tx Tx, banks []PersonalSourceBank) ([]PersonalSourceFact, error) {
	var out []PersonalSourceFact
	for _, bank := range banks {
		facts, err := s.factsAfterTx(ctx, tx, PersonalSourceBank{BankID: bank.BankID}, -1)
		if err != nil {
			return nil, err
		}
		out = append(out, facts...)
	}
	return out, nil
}

// factsAfterTx reads one bank's world facts after its watermark, or all of them
// when limit is negative.
func (s *PostgresPersonalStore) factsAfterTx(ctx context.Context, tx Tx, bank PersonalSourceBank, limit int) ([]PersonalSourceFact, error) {
	schema, ok := SchemaFrom(ctx)
	if !ok {
		return nil, ErrSchemaRequired
	}
	cursor := bank.Cursor
	statement := `
		SELECT id::text, text, updated_at
		FROM ` + quoteIdent(schema) + `.memory_units
		WHERE bank_id = $1 AND fact_type = 'world'
		  AND (updated_at, id) > ($2::timestamptz, $3::uuid)
		ORDER BY updated_at, id`
	args := []any{bank.BankID, cursor.UpdatedAt, zeroUUID}
	if !cursor.Before() && cursor.UnitID != "" {
		args[2] = cursor.UnitID
	}
	if limit >= 0 {
		statement += ` LIMIT $4`
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PersonalSourceFact
	for rows.Next() {
		var fact PersonalSourceFact
		if err := rows.Scan(&fact.UnitID, &fact.Text, &fact.UpdatedAt); err != nil {
			return nil, err
		}
		fact.BankID = bank.BankID
		out = append(out, fact)
	}
	return out, rows.Err()
}

// sourceDeletedTx reports whether any live ledger source has gone from its bank.
//
// Only rows that still support aggregate content are checked. A source the model
// folded away supports nothing, so its deletion cannot make the aggregate stale.
func (s *PostgresPersonalStore) sourceDeletedTx(ctx context.Context, tx Tx, schema, tenantID string) (bool, error) {
	var deleted bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1
		    FROM `+adminTable("personal_source")+` l
		    LEFT JOIN `+quoteIdent(schema)+`.memory_units m ON m.id = l.source_unit_id
		    WHERE l.tenant_id = $1 AND l.merge_key <> $2 AND m.id IS NULL
		)`, tenantID, tenant.DroppedMergeKey).Scan(&deleted)
	return deleted, err
}

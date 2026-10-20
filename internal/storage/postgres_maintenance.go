package storage

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

// PurgeExpiredRows deletes expired rows from one table in bounded chunks,
// claimed oldest-first with FOR UPDATE SKIP LOCKED.
//
// SKIP LOCKED is what makes a leaderless fleet safe: a chunk never waits on
// another sweeper, and two processes sweeping the same table take disjoint
// chunks rather than redoing each other's work. Each chunk commits on its own,
// so no transaction holds locks longer than one batch.
func (s *PostgresStore) PurgeExpiredRows(ctx context.Context, schema, table, tsColumn string, cutoff time.Time, batchSize, maxBatches int) (int, error) {
	if batchSize <= 0 {
		batchSize = 1
	}
	if maxBatches <= 0 {
		return 0, nil
	}
	qualified := quoteIdent(schema) + "." + quoteIdent(table)
	column := quoteIdent(tsColumn)
	deleted := 0
	for batch := 0; batch < maxBatches; batch++ {
		if ctx.Err() != nil {
			return deleted, ctx.Err()
		}
		removed, err := s.purgeChunk(ctx, qualified, column, cutoff, batchSize)
		if err != nil {
			return deleted, err
		}
		deleted += removed
		// A short chunk means the expired range is drained, or that another
		// sweeper holds the rest — either way there is nothing left to take.
		if removed < batchSize {
			break
		}
		// A short pause between chunks gives concurrent writers a window to
		// commit, so a long sweep does not monopolise the table's locks.
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(retentionBatchPause):
		}
	}
	return deleted, nil
}

// retentionBatchPause is the pause between retention chunks.
const retentionBatchPause = 250 * time.Millisecond

// purgeChunk removes one bounded batch of expired rows.
func (s *PostgresStore) purgeChunk(ctx context.Context, qualified, column string, cutoff time.Time, batchSize int) (int, error) {
	query := `WITH expired AS (
	            SELECT id FROM ` + qualified + `
	            WHERE ` + column + ` < $1
	            ORDER BY ` + column + `
	            LIMIT $2
	            FOR UPDATE SKIP LOCKED
	          ), removed AS (
	            DELETE FROM ` + qualified + ` t USING expired e WHERE t.id = e.id RETURNING 1
	          )
	          SELECT count(*) FROM removed`
	var removed int
	if err := s.db.QueryRowContext(ctx, query, cutoff, batchSize).Scan(&removed); err != nil {
		return 0, err
	}
	return removed, nil
}

// BanksNeedingConsolidation returns the banks in one schema holding facts that
// are eligible for consolidation with none queued.
//
// A fact is stranded when a consolidation operation failed terminally after the
// fact was written: both consolidation timestamps are null, so nothing will
// pick it up again. The bank-level enable switch is not consulted here — this
// answers "is there work", and the caller filters by configuration before
// queueing.
func (s *PostgresStore) BanksNeedingConsolidation(ctx context.Context, schema string) ([]string, error) {
	// Driven from `banks`, not from `memory_units`: the reference's discovery
	// routine is `FROM banks b WHERE ... AND EXISTS (SELECT 1 FROM memory_units
	// m WHERE m.bank_id = b.bank_id ...)` (migrations/0005_reference_functions.sql),
	// so a unit whose bank row is gone is not a bank with work — it is debris
	// left by an interrupted delete, and naming it here would only queue
	// consolidations for banks that cannot exist.
	query := `SELECT b.bank_id
	          FROM ` + quoteIdent(schema) + `.banks b
	          WHERE NOT EXISTS (
	                SELECT 1 FROM ` + quoteIdent(schema) + `.async_operations o
	                WHERE o.bank_id = b.bank_id
	                  AND o.operation_type = $1
	                  AND o.status IN ('pending', 'processing')
	            )
	            AND EXISTS (
	                SELECT 1 FROM ` + quoteIdent(schema) + `.memory_units m
	                WHERE m.bank_id = b.bank_id
	                  AND m.consolidated_at IS NULL
	                  AND m.consolidation_failed_at IS NULL
	                  AND m.fact_type IN ('experience', 'world')
	            )`
	rows, err := s.db.QueryContext(ctx, query, OperationTypeConsolidate)
	if err != nil {
		// A schema whose tables are missing or mid-migration has no work to
		// reconcile, which is not a failure of the job.
		if isUndefinedTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanStrings(rows)
}

// MentalModelsWithSchedule returns the scheduled models of one schema.
//
// A model with a refresh already queued or running is excluded, so a fleet
// whose every process runs this job does not queue one wave per process. The
// exclusion is a read and so advisory only; the enqueue itself is what makes it
// idempotent.
func (s *PostgresStore) MentalModelsWithSchedule(ctx context.Context, schema string) ([]ScheduledMentalModel, error) {
	query := `SELECT mm.bank_id, mm.id, COALESCE(mm.trigger->>'refresh_cron',''), mm.last_refreshed_at
	          FROM ` + quoteIdent(schema) + `.mental_models mm
	          WHERE COALESCE(mm.trigger->>'refresh_cron', '') <> ''
	            AND NOT EXISTS (
	                SELECT 1 FROM ` + quoteIdent(schema) + `.async_operations o
	                WHERE o.bank_id = mm.bank_id
	                  AND o.operation_type = $1
	                  AND o.status IN ('pending', 'processing')
	                  AND o.task_payload->>'mental_model_id' = mm.id
	            )`
	rows, err := s.db.QueryContext(ctx, query, OperationTypeRefreshMentalModel)
	if err != nil {
		if isUndefinedTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]ScheduledMentalModel, 0)
	for rows.Next() {
		var entry ScheduledMentalModel
		var refreshed sql.NullTime
		if err := rows.Scan(&entry.BankID, &entry.MentalModelID, &entry.RefreshCron, &refreshed); err != nil {
			return nil, err
		}
		if refreshed.Valid {
			entry.LastRefreshedAt = refreshed.Time
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// PruneTerminalOperations removes settled operations past their retention.
//
// Only terminal rows are eligible, so a schema holding nothing but outstanding
// work removes nothing. The batch is claimed oldest-first under SKIP LOCKED for
// the same reason the retention sweep is: every process runs this and they must
// take disjoint work rather than collide.
//
// Before the rows go, the stored archives they own are swept the same way, so
// an export operation's blob does not outlive the row that was its only handle.
func (s *PostgresStore) PruneTerminalOperations(ctx context.Context, schema string, cutoff time.Time, batchSize int) (int, error) {
	if batchSize <= 0 {
		return 0, nil
	}
	s.purgeExpiredExportArchives(ctx, schema, cutoff, batchSize)
	query := `WITH expired AS (
	            SELECT operation_id FROM ` + quoteIdent(schema) + `.async_operations
	            WHERE status = ANY($1::text[])
	              AND updated_at < $2
	            ORDER BY updated_at, operation_id
	            LIMIT $3
	            FOR UPDATE SKIP LOCKED
	          ), removed AS (
	            DELETE FROM ` + quoteIdent(schema) + `.async_operations o
	            USING expired e WHERE o.operation_id = e.operation_id RETURNING 1
	          )
	          SELECT count(*) FROM removed`
	var removed int
	if err := s.db.QueryRowContext(ctx, query, TerminalOperationStatuses, cutoff, batchSize).Scan(&removed); err != nil {
		if isUndefinedTable(err) {
			return 0, nil
		}
		return 0, err
	}
	return removed, nil
}

// purgeExpiredExportArchives deletes the stored archives of the export
// operations a prune is about to remove.
//
// It mirrors PruneTerminalOperations' predicate (terminal status + updated_at <
// cutoff), ordering and batch bound, so an export's archive is removed in step
// with its row instead of being orphaned in file storage. The bound matters: an
// unbounded purge would re-select every expired export on every run and reissue
// a file-storage delete for each, because storage_key stays in result_metadata
// until the row itself is gone. Sharing the prune's ordered window makes the
// two advance together. Best-effort throughout — a blob that cannot be deleted
// is logged, never returned, so archive cleanup cannot block the row prune.
func (s *PostgresStore) purgeExpiredExportArchives(ctx context.Context, schema string, cutoff time.Time, batchSize int) {
	query := `SELECT result_metadata FROM ` + quoteIdent(schema) + `.async_operations
	          WHERE operation_type = ANY($1::text[])
	            AND status = ANY($2::text[])
	            AND updated_at < $3
	          ORDER BY updated_at, operation_id
	          LIMIT $4`
	rows, err := s.db.QueryContext(ctx, query, ExportOperationTypes, TerminalOperationStatuses, cutoff, batchSize)
	if err != nil {
		if !isUndefinedTable(err) {
			slog.Default().Warn("failed to select expired export archives", "schema", schema, "error", err)
		}
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			slog.Default().Warn("failed to read expired export archive metadata", "schema", schema, "error", err)
			return
		}
		metadata, err := unmarshalResultMetadata(raw)
		if err != nil {
			continue
		}
		s.deleteOperationExportArchive(ctx, metadata)
	}
	if err := rows.Err(); err != nil {
		slog.Default().Warn("failed to scan expired export archives", "schema", schema, "error", err)
	}
}

// scanStrings reads a single text column from every row.
func scanStrings(rows *sql.Rows) ([]string, error) {
	out := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// isUndefinedTable reports whether an error is a missing table, which on a
// schema mid-migration means nothing to do rather than a failure.
//
// It is matched on the SQL state rather than the driver's error type, because
// the storage area compiles without the driver on the default build.
func isUndefinedTable(err error) bool {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		// 42P01 undefined_table, 3F000 invalid_schema_name.
		switch state.SQLState() {
		case "42P01", "3F000":
			return true
		}
	}
	return false
}

// DropSchemaForTest removes a schema and everything in it.
//
// It lives in a non-test file because a test in another package reaches the
// store through the interface and cannot see a method that only exists under a
// test build. Every harness that migrates a uniquely named schema calls this
// when it finishes: leaving schemas behind accumulates across runs, and the
// cross-schema discovery routines then run out of lock table opening a cursor
// per schema.
//
// It is destructive and scoped to a schema whose name the caller chose, which
// is what makes it safe for a harness and unsuitable for anything else.
func (s *PostgresStore) DropSchemaForTest(schema string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
	return err
}

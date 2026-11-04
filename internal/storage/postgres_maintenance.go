package storage

import (
	"context"
	"errors"
	"fmt"
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

// BanksNeedingConsolidation answers the consolidation reconcile: one
// cross-schema round-trip through the reference's own routine.
//
// The routine lives in a schema installed by the migration series
// (migrations/0005_reference_functions.sql carries its body verbatim), so the
// caller supplies the schema holding it. While it is absent the answer is "no
// work" rather than an error, which is what the reference's swallowed discovery
// failure amounts to.
func (s *PostgresStore) BanksNeedingConsolidation(ctx context.Context, routineSchema string) ([]ReconcileTarget, error) {
	found, err := s.queryDiscovery(ctx,
		`SELECT schema_name, bank_id FROM `+quoteIdent(routineSchema)+`.banks_needing_consolidation()`)
	if err != nil {
		return nil, s.classifyDiscoveryError(err)
	}
	targets := make([]ReconcileTarget, 0, len(found))
	for _, row := range found {
		targets = append(targets, ReconcileTarget{Schema: textOf(row[0]), BankID: textOf(row[1])})
	}
	return targets, nil
}

// MentalModelsWithSchedule answers the scheduled refresh: one cross-schema
// round-trip through mental_models_with_cron(). The routine already excludes
// any model with a refresh queued or running, and already reads both schedules.
func (s *PostgresStore) MentalModelsWithSchedule(ctx context.Context, routineSchema string) ([]ScheduledMentalModel, error) {
	found, err := s.queryDiscovery(ctx,
		`SELECT schema_name, bank_id, mental_model_id, refresh_cron, last_refreshed_at, full_refresh_cron, last_full_refreshed_at
		   FROM `+quoteIdent(routineSchema)+`.mental_models_with_cron()`)
	if err != nil {
		return nil, s.classifyDiscoveryError(err)
	}
	out := make([]ScheduledMentalModel, 0, len(found))
	for _, row := range found {
		// The function declares schema_name, bank_id, mental_model_id,
		// refresh_cron, last_refreshed_at, full_refresh_cron,
		// last_full_refreshed_at — the crons and their stamps alternate, so the
		// cron columns are 3 and 5 and the stamps 4 and 6.
		entry := ScheduledMentalModel{Schema: textOf(row[0]), BankID: textOf(row[1]), MentalModelID: textOf(row[2])}
		if row[3] != nil {
			entry.RefreshCron = *row[3]
		}
		if row[5] != nil {
			entry.FullRefreshCron = *row[5]
		}
		if entry.LastRefreshedAt, err = parseDiscoveryTime(row[4]); err != nil {
			return nil, err
		}
		if entry.LastFullRefreshedAt, err = parseDiscoveryTime(row[6]); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// SchemasWithExpiredRows lists the schemas holding rows past their retention
// window, one cross-schema round-trip through schemas_with_expired_rows().
//
// The reference probes before it deletes: a deployment with thousands of tenant
// schemas must not open one delete statement per schema per sweep when a handful
// hold expired rows.
func (s *PostgresStore) SchemasWithExpiredRows(ctx context.Context, routineSchema, table, tsColumn string, days int) ([]string, error) {
	if days <= 0 {
		return nil, nil
	}
	found, err := s.queryDiscovery(ctx,
		`SELECT schemas_with_expired_rows FROM `+quoteIdent(routineSchema)+`.schemas_with_expired_rows($1, $2, $3)`,
		table, tsColumn, days)
	if err != nil {
		return nil, s.classifyDiscoveryError(err)
	}
	schemas := make([]string, 0, len(found))
	for _, row := range found {
		schemas = append(schemas, textOf(row[0]))
	}
	return schemas, nil
}

// SchemasWithExpiredOperations lists the schemas holding terminal operations
// past their retention, through schemas_with_expired_operations().
func (s *PostgresStore) SchemasWithExpiredOperations(ctx context.Context, routineSchema string, days int) ([]string, error) {
	if days <= 0 {
		return nil, nil
	}
	found, err := s.queryDiscovery(ctx,
		`SELECT schemas_with_expired_operations FROM `+quoteIdent(routineSchema)+`.schemas_with_expired_operations($1)`,
		days)
	if err != nil {
		return nil, s.classifyDiscoveryError(err)
	}
	schemas := make([]string, 0, len(found))
	for _, row := range found {
		schemas = append(schemas, textOf(row[0]))
	}
	return schemas, nil
}

// queryDiscovery runs one discovery routine and reads every returned column as
// text, NULL included, so one reader serves all four routines.
func (s *PostgresStore) queryDiscovery(ctx context.Context, query string, args ...any) ([][]*string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]*string
	for rows.Next() {
		cells := make([]*string, len(columns))
		targets := make([]any, len(columns))
		for i := range cells {
			targets[i] = &cells[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		out = append(out, cells)
	}
	return out, rows.Err()
}

// textOf reads one discovery column, treating NULL as empty.
func textOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// parseDiscoveryTime reads a timestamptz column the driver handed back as text.
func parseDiscoveryTime(value *string) (time.Time, error) {
	if value == nil || *value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999-07:00"} {
		if parsed, err := time.Parse(layout, *value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("maintenance discovery returned an unreadable timestamp %q", *value)
}

// PurgeExpiredExportArchives deletes the stored archives of the export
// operations a prune is about to remove.
//
// It mirrors PruneTerminalOperations' predicate (terminal status + updated_at <
// cutoff), ordering and batch bound, so an export's archive is removed in step
// with its row instead of being orphaned in file storage. The bound matters: an
// unbounded purge would re-select every expired export on every run and reissue
// a file-storage delete for each, because storage_key stays in result_metadata
// until the row itself is gone. Sharing the prune's ordered window makes the
// two advance together. Best-effort throughout — a blob that cannot be deleted
// is logged, never returned, so archive cleanup cannot block the row prune
// (the reference issues both on the same connection, engine/maintenance.py:402-414).
func (s *PostgresStore) PurgeExpiredExportArchives(ctx context.Context, schema string, cutoff time.Time, batchSize int) {
	if batchSize <= 0 {
		return
	}
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
	s.PurgeExpiredExportArchives(ctx, schema, cutoff, batchSize)
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

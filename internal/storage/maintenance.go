package storage

import (
	"context"
	"time"
)

// The maintenance jobs each need one narrow capability rather than the whole
// store, so they are declared as separate interfaces. That keeps a job
// testable with a stub and keeps the store's own surface from growing a
// maintenance corner it does not otherwise need.

// RetentionPurger deletes rows past their retention window from one table.
//
// It is scoped to the two tables whose retention is configured (audit_log and
// llm_requests), because it deletes by primary key and assumes both carry an
// id column.
type RetentionPurger interface {
	// PurgeExpiredRows removes at most maxBatches * batchSize expired rows and
	// reports how many it removed.
	//
	// It is bounded rather than a single statement because an unbounded delete
	// on a table with a backlog holds row locks for minutes, and every process
	// in a fleet issues it at the same boundary.
	PurgeExpiredRows(ctx context.Context, schema, table, tsColumn string, cutoff time.Time, batchSize, maxBatches int) (int, error)
}

// ConsolidationReconciler answers "which banks have facts waiting to be
// consolidated and none queued", which is what the reconcile job re-schedules.
//
// The generic store does not do it: the reference's answer comes from a
// PL/pgSQL routine that scans every schema in one round-trip, and no Go-side
// query reproduces that (the reference's own Go-portable path falls back to the
// memories *extension*, whose absence means the job simply never enqueues —
// engine/maintenance.py:441-443). Absent capability, absent work.
type ConsolidationReconciler interface {
	// BanksNeedingConsolidation returns the stranded banks of every schema the
	// deployment serves, in discovery order. routineSchema is the schema holding
	// the discovery routine — the one named by the job's own query.
	BanksNeedingConsolidation(ctx context.Context, routineSchema string) ([]ReconcileTarget, error)
}

// ReconcileTarget is one bank the consolidation reconcile should reschedule,
// and the schema it lives in.
type ReconcileTarget struct {
	Schema string
	BankID string
}

// ScheduledMentalModel is one mental model carrying a refresh schedule.
//
// It carries both schedules, and each answers to its own clock: RefreshCron to
// the last refresh of any mode, FullRefreshCron to the last one that ran in
// full mode. A model may hold either, or both.
type ScheduledMentalModel struct {
	// Schema is the tenant schema the row was discovered in.
	Schema        string
	BankID        string
	MentalModelID string
	// RefreshCron is the trigger's refresh_cron, empty when the model has none.
	RefreshCron string
	// LastRefreshedAt is when the last refresh of any mode ran; zero when none
	// has, which takes the column's own default into account.
	LastRefreshedAt time.Time
	// FullRefreshCron is the trigger's full_refresh_cron, empty when the model
	// has none.
	FullRefreshCron string
	// LastFullRefreshedAt is when a refresh last ran in full mode; zero when
	// none has, and a discovery source that predates the column reports zero.
	LastFullRefreshedAt time.Time
}

// ScheduledMentalModelFinder lists the models whose schedule may be due.
//
// Like the consolidation reconcile, discovery is one cross-schema round-trip
// through the reference's mental_models_with_cron() routine, so only a backend
// that has it can answer.
type ScheduledMentalModelFinder interface {
	// MentalModelsWithSchedule returns the scheduled models of every schema the
	// deployment serves, excluding any that already have a refresh queued or
	// running. ScheduledMentalModel.Schema names the one each row came from.
	//
	// routineSchema is the schema holding the discovery routine.
	MentalModelsWithSchedule(ctx context.Context, routineSchema string) ([]ScheduledMentalModel, error)
}

// OperationPruner removes settled operations past their retention.
//
// The archive sweep that must precede the row prune belongs to this capability
// rather than to the job: the reference issues both on the same schema-scoped
// connection, scans the same ordered window, and treats neither as fatal
// (engine/memory_engine.py:4321-4361). A backend that cannot prune has no
// archives to sweep either.
type OperationPruner interface {
	// PurgeExpiredExportArchives deletes the stored archives of the export
	// operations a prune would remove, before the rows go. Best-effort: a
	// failure is logged and never returned, so archive cleanup cannot block the
	// row prune.
	PurgeExpiredExportArchives(ctx context.Context, schema string, cutoff time.Time, batchSize int)
	// PruneTerminalOperations removes at most batchSize operations that have
	// reached a terminal status and are older than the cutoff.
	PruneTerminalOperations(ctx context.Context, schema string, cutoff time.Time, batchSize int) (int, error)
}

// GraphMaintainer is the relink/prune queue the graph-maintenance job drains.
//
// It is taken as a narrow capability rather than the whole Store so the job is
// testable with a stub, and it carries the requeue side of the queue as well as
// the drain side: the drain removes each target before it is processed, so a
// target that fails has to go back on the queue explicitly or its work is lost.
type GraphMaintainer interface {
	// DrainGraphMaintenance claims up to limit queued graph-relink targets and
	// removes them from the queue.
	DrainGraphMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error)
	// RelinkUnit recomputes one unit's semantic links from its embedding.
	RelinkUnit(ctx context.Context, bankID, unitID string) error
	// DrainEntityMaintenance claims up to limit queued entity-prune targets and
	// removes them from the queue.
	DrainEntityMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error)
	// PruneEntity removes an entity no memory unit mentions any more.
	PruneEntity(ctx context.Context, bankID, entityID string) error
	// RequeueGraphMaintenance returns relink targets to the queue for a later
	// drain, deduplicating against rows already queued.
	RequeueGraphMaintenance(ctx context.Context, targets []MaintenanceTarget) error
	// RequeueEntityMaintenance returns entity-prune targets to the queue for a
	// later drain, deduplicating against rows already queued.
	RequeueEntityMaintenance(ctx context.Context, targets []MaintenanceTarget) error
}

// TerminalOperationStatuses are the statuses a prune may remove. A pending or
// processing operation is never prunable: its work is still outstanding, and
// removing the row would lose it silently.
var TerminalOperationStatuses = []string{
	string(StatusCompleted), string(StatusFailed), string(StatusCancelled),
}

// ExpiredRowFinder lists the schemas a retention sweep must visit, so a
// deployment with many tenant schemas issues one discovery query instead of a
// delete statement per schema.
//
// The reference probes through schemas_with_expired_rows(); a backend without
// that routine reports every schema and lets the purge itself settle it.
type ExpiredRowFinder interface {
	SchemasWithExpiredRows(ctx context.Context, routineSchema, table, tsColumn string, days int) ([]string, error)
}

// SchemasWithExpiredOperations lists the schemas holding terminal operations
// past their retention, through schemas_with_expired_operations().
type SchemasWithExpiredOperations interface {
	SchemasWithExpiredOperations(ctx context.Context, routineSchema string, days int) ([]string, error)
}

// SchemasWithExpiredRowsCapability is the finder the retention sweep uses; see
// ExpiredRowFinder above.

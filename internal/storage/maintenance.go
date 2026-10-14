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

// ConsolidationReconciler finds banks holding facts that are eligible for
// consolidation but have none queued.
type ConsolidationReconciler interface {
	// BanksNeedingConsolidation returns those bank identifiers for one schema.
	BanksNeedingConsolidation(ctx context.Context, schema string) ([]string, error)
}

// ScheduledMentalModel is one mental model carrying a refresh schedule.
type ScheduledMentalModel struct {
	BankID          string
	MentalModelID   string
	RefreshCron     string
	LastRefreshedAt time.Time
}

// ScheduledMentalModelFinder lists the models whose schedule may be due.
type ScheduledMentalModelFinder interface {
	// MentalModelsWithSchedule returns the scheduled models of one schema,
	// excluding any that already have a refresh queued or running.
	MentalModelsWithSchedule(ctx context.Context, schema string) ([]ScheduledMentalModel, error)
}

// OperationPruner removes settled operations past their retention.
type OperationPruner interface {
	// PruneTerminalOperations removes at most batchSize operations that have
	// reached a terminal status and are older than the cutoff.
	PruneTerminalOperations(ctx context.Context, schema string, cutoff time.Time, batchSize int) (int, error)
}

// TerminalOperationStatuses are the statuses a prune may remove. A pending or
// processing operation is never prunable: its work is still outstanding, and
// removing the row would lose it silently.
var TerminalOperationStatuses = []string{
	string(StatusCompleted), string(StatusFailed), string(StatusCancelled),
}

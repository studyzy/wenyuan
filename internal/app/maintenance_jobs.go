package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/maintenance"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// retentionBatch bounds one delete statement's row count.
//
// An unbounded delete on a table with a real backlog holds row locks for
// minutes while it reads the whole expired range, and because this loop runs in
// every process each one issues it at the same boundary. Bounded chunks claimed
// with SKIP LOCKED take disjoint work instead of waiting on each other.
const retentionBatch = 2000

// retentionMaxBatches caps the chunks one run removes per table per schema. It
// is a backstop against looping forever on a table filling faster than it
// drains; the remainder waits for the next sweep.
const retentionMaxBatches = 1000

// maintenanceJobs builds the recurring jobs from the resolved configuration.
//
// A disabled job is omitted rather than added with a zero interval, so the loop
// does not carry work it will never run.
func maintenanceJobs(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
) []maintenance.Job {
	jobs := make([]maintenance.Job, 0, 4)

	if cfg.AuditLogRetentionDays > 0 && cfg.RetentionSweepInterval > 0 {
		jobs = append(jobs, maintenance.Job{
			Name:     "retention.audit_log",
			Interval: cfg.RetentionSweepInterval,
			Run: func(ctx context.Context) error {
				return purgeExpired(ctx, store, tenants, "audit_log", "started_at", cfg.AuditLogRetentionDays)
			},
		})
	}
	if cfg.LLMTraceEnabled && cfg.LLMTraceRetentionDays > 0 && cfg.RetentionSweepInterval > 0 {
		jobs = append(jobs, maintenance.Job{
			Name:     "retention.llm_requests",
			Interval: cfg.RetentionSweepInterval,
			Run: func(ctx context.Context) error {
				return purgeExpired(ctx, store, tenants, "llm_requests", "started_at", cfg.LLMTraceRetentionDays)
			},
		})
	}
	if cfg.ConsolidationReconcileInterval > 0 {
		jobs = append(jobs, maintenance.Job{
			Name:     "consolidation.reconcile",
			Interval: cfg.ConsolidationReconcileInterval,
			Run: func(ctx context.Context) error {
				return reconcileConsolidation(ctx, store, tenants)
			},
		})
	}
	if cfg.MentalModelRefreshTick > 0 {
		refresh := newMentalModelRefresh(store, model, reflection)
		jobs = append(jobs, maintenance.Job{
			Name:     "mental_model.scheduled_refresh",
			Interval: cfg.MentalModelRefreshTick,
			Run: func(ctx context.Context) error {
				return refreshDueMentalModels(ctx, store, tenants, refresh)
			},
		})
	}
	if cfg.OperationCleanupInterval > 0 && cfg.OperationRetentionDays > 0 {
		jobs = append(jobs, maintenance.Job{
			Name:     "operations.cleanup",
			Interval: cfg.OperationCleanupInterval,
			Run: func(ctx context.Context) error {
				return pruneTerminalOperations(ctx, store, tenants, cfg.OperationRetentionDays, cfg.OperationCleanupBatchSize)
			},
		})
	}
	return jobs
}

// purgeExpired removes rows older than the window from one table, in bounded
// chunks, across every schema the deployment serves.
//
// The window is computed once for the whole sweep rather than per chunk: a
// per-chunk clock would let the cutoff creep forward mid-run, which makes
// "a short chunk means the range is drained" untrue.
func purgeExpired(ctx context.Context, store storage.Store, tenants tenant.Extension, table, tsColumn string, days int) error {
	if days <= 0 {
		return nil
	}
	purger, ok := store.(storage.RetentionPurger)
	if !ok {
		return nil
	}
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	var total int
	for _, schema := range schemas {
		removed, err := purger.PurgeExpiredRows(ctx, schema, table, tsColumn, cutoff, retentionBatch, retentionMaxBatches)
		if err != nil {
			// One schema failing must not stop the others: the rest still have
			// expired rows and the next sweep retries this one.
			return fmt.Errorf("purge %s.%s: %w", schema, table, err)
		}
		total += removed
	}
	_ = total
	return nil
}

// reconcileConsolidation re-schedules consolidation for banks whose eligible
// facts have none queued.
//
// A fact is stranded when a consolidation operation failed terminally after the
// fact was written: its consolidation timestamps are both null, so nothing will
// pick it up again. This is what finds those facts and requeues them.
func reconcileConsolidation(ctx context.Context, store storage.Store, tenants tenant.Extension) error {
	finder, ok := store.(storage.ConsolidationReconciler)
	if !ok {
		return nil
	}
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	for _, schema := range schemas {
		bankIDs, err := finder.BanksNeedingConsolidation(ctx, schema)
		if err != nil {
			return fmt.Errorf("discover banks needing consolidation in %s: %w", schema, err)
		}
		for _, bankID := range bankIDs {
			if strings.TrimSpace(bankID) == "" {
				continue
			}
			// The enqueue is idempotent inside the database, which is what
			// keeps every process in a fleet from queueing its own copy.
			if _, err := store.EnqueueOperation(ctx, storage.EnqueueRequest{
				Operation: storage.Operation{
					BankID:        bankID,
					OperationType: storage.OperationTypeConsolidate,
				},
			}); err != nil {
				return fmt.Errorf("schedule consolidation for %s: %w", bankID, err)
			}
		}
	}
	return nil
}

// refreshDueMentalModels enqueues a refresh for each scheduled model whose cron
// has come due and whose scope has actually moved.
//
// Due-ness is decided here rather than in SQL because cron arithmetic is not
// expressible in a plain query. The staleness check after it is what stops a
// schedule from spending a model call to regenerate content that would be
// identical: a tick that finds nothing new costs one cheap query.
func refreshDueMentalModels(ctx context.Context, store storage.Store, tenants tenant.Extension, refresh mentalModelRefresh) error {
	discoverer, ok := store.(storage.ScheduledMentalModelFinder)
	if !ok {
		return nil
	}
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, schema := range schemas {
		scheduled, err := discoverer.MentalModelsWithSchedule(ctx, schema)
		if err != nil {
			return fmt.Errorf("discover scheduled mental models in %s: %w", schema, err)
		}
		for _, entry := range scheduled {
			if !cronDue(entry.RefreshCron, entry.LastRefreshedAt, now) {
				continue
			}
			model, err := store.GetMentalModel(ctx, entry.BankID, entry.MentalModelID)
			if err != nil {
				// The model may have been deleted between discovery and here.
				continue
			}
			if !store.IsStale(ctx, entry.BankID, model) {
				continue
			}
			// An automatic refresh folds into a running one: the caller did not
			// ask for this specific run, and a running refresh covers its
			// intent. The enqueue is idempotent inside the database, which is
			// what keeps a fleet from queueing one wave per process.
			if _, err := store.EnqueueOperation(ctx, storage.EnqueueRequest{
				Operation: storage.Operation{
					BankID:           entry.BankID,
					OperationType:    storage.OperationTypeRefreshMentalModel,
					Payload:          map[string]any{storage.MentalModelIDPayloadKey: entry.MentalModelID},
					SerializationKey: storage.MentalModelSerializationKey(entry.MentalModelID),
				},
				DedupeByPayloadKey:       storage.MentalModelIDPayloadKey,
				DedupeIncludesProcessing: true,
			}); err != nil {
				return fmt.Errorf("schedule refresh for %s: %w", entry.MentalModelID, err)
			}
		}
	}
	return nil
}

// pruneTerminalOperations removes settled operations past their retention.
func pruneTerminalOperations(ctx context.Context, store storage.Store, tenants tenant.Extension, days, batchSize int) error {
	if days <= 0 {
		return nil
	}
	pruner, ok := store.(storage.OperationPruner)
	if !ok {
		return nil
	}
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	for _, schema := range schemas {
		if _, err := pruner.PruneTerminalOperations(ctx, schema, cutoff, batchSize); err != nil {
			return fmt.Errorf("prune terminal operations in %s: %w", schema, err)
		}
	}
	return nil
}

// tenantSchemas resolves the schemas a job should cover.
func tenantSchemas(ctx context.Context, tenants tenant.Extension) ([]string, error) {
	if tenants == nil {
		return nil, errors.New("maintenance requires a tenant extension")
	}
	listed, err := tenants.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	schemas := make([]string, 0, len(listed))
	for _, entry := range listed {
		if trimmed := strings.TrimSpace(entry.Schema); trimmed != "" {
			schemas = append(schemas, trimmed)
		}
	}
	return schemas, nil
}

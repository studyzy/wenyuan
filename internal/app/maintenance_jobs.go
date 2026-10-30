package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	vectorIndex config.VectorIndexConfig,
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
	if vectorIndex.MinRows > 0 {
		jobs = append(jobs, maintenance.Job{
			Name:     "vector_index.reconcile",
			Interval: time.Duration(vectorIndex.ReconcileInterval) * time.Second,
			Run: func(ctx context.Context) error {
				return reconcileVectorIndexes(ctx, store, tenants, vectorIndex.MinRows)
			},
		})
	}
	if cfg.ConsolidationReconcileInterval > 0 {
		jobs = append(jobs, maintenance.Job{
			Name:     "graph.maintenance",
			Interval: cfg.ConsolidationReconcileInterval,
			Run: func(ctx context.Context) error {
				// A store without the maintenance queue — the in-memory one —
				// has nothing to drain, so the job is a no-op there.
				maintainer, ok := store.(storage.GraphMaintainer)
				if !ok {
					return nil
				}
				return drainGraphMaintenance(ctx, maintainer, tenants)
			},
		})
	}
	return jobs
}

// reconcileVectorIndexes brings every qualifying bank's partial vector index
// coverage to the plan.
//
// A backend without per-bank indexes — the in-memory one — does not implement
// the capability, so the job is a no-op there. A single bank's failure is
// reported but does not stop the sweep, because the next tick retries it and
// one bad bank should not starve the rest.
func reconcileVectorIndexes(ctx context.Context, store storage.Store, tenants tenant.Extension, minRows int) error {
	reconciler, ok := store.(storage.VectorIndexReconciler)
	if !ok {
		return nil
	}
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	var firstErr error
	for _, schema := range schemas {
		schemaCtx, schemaErr := storage.WithSchema(ctx, schema)
		if schemaErr != nil {
			return schemaErr
		}
		banks, err := reconciler.BanksNeedingVectorIndexes(schemaCtx, minRows)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("discover banks needing vector indexes in %s: %w", schema, err)
			}
			continue
		}
		for _, bankID := range banks {
			if _, err := reconciler.ReconcileBankVectorIndexes(schemaCtx, bankID, minRows); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				slog.Default().Warn("vector index reconcile failed", "schema", schema, "bank_id", bankID, "error", err)
			}
		}
	}
	return firstErr
}

// drainGraphMaintenance relinks units left behind by a deletion and prunes
// entities no unit mentions any more, in bounded batches.
//
// The drain removes each target from its queue before the work runs, so a
// failure would strand it: the row is already gone and nothing else re-enqueues
// it. A failed target is therefore put back on the queue — with a fresh
// enqueued_at, sending it to the back — and the sweep keeps going, because one
// bad target must not discard the rest of the batch. The first error is still
// returned so the job's failure stays visible.
func drainGraphMaintenance(ctx context.Context, store storage.GraphMaintainer, tenants tenant.Extension) error {
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	var firstErr error
	for _, schema := range schemas {
		schemaCtx, schemaErr := storage.WithSchema(ctx, schema)
		if schemaErr != nil {
			return schemaErr
		}
		if err := drainGraphMaintenanceInSchema(schemaCtx, store); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func drainGraphMaintenanceInSchema(ctx context.Context, store storage.GraphMaintainer) error {
	var firstErr error
	relink, err := store.DrainGraphMaintenance(ctx, 500)
	if err != nil {
		return err
	}
	var failedRelink []storage.MaintenanceTarget
	for _, target := range relink {
		if err := store.RelinkUnit(ctx, target.BankID, target.ID); err != nil {
			slog.Default().Warn("graph relink failed; returning target to the queue",
				"bank_id", target.BankID, "unit_id", target.ID, "error", err)
			failedRelink = append(failedRelink, target)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := store.RequeueGraphMaintenance(ctx, failedRelink); err != nil {
		slog.Default().Error("could not requeue failed graph relink targets", "count", len(failedRelink), "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}
	prune, err := store.DrainEntityMaintenance(ctx, 500)
	if err != nil {
		return err
	}
	var failedPrune []storage.MaintenanceTarget
	for _, target := range prune {
		if err := store.PruneEntity(ctx, target.BankID, target.ID); err != nil {
			slog.Default().Warn("entity prune failed; returning target to the queue",
				"bank_id", target.BankID, "entity_id", target.ID, "error", err)
			failedPrune = append(failedPrune, target)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := store.RequeueEntityMaintenance(ctx, failedPrune); err != nil {
		slog.Default().Error("could not requeue failed entity prune targets", "count", len(failedPrune), "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
		schemaCtx, schemaErr := storage.WithSchema(ctx, schema)
		if schemaErr != nil {
			return schemaErr
		}
		bankIDs, err := finder.BanksNeedingConsolidation(schemaCtx, schema)
		if err != nil {
			return fmt.Errorf("discover banks needing consolidation in %s: %w", schema, err)
		}
		for _, bankID := range bankIDs {
			if strings.TrimSpace(bankID) == "" {
				continue
			}
			// One bank's failure must not strand the rest: discovery answers
			// across the whole schema, and a bank deleted between the scan and
			// the insert (or any other refusal) would otherwise abort the sweep
			// before every later bank is reached. The reference catches per
			// target for the same reason (engine/maintenance.py:479-482).
			//
			// The enqueue is idempotent inside the database, which is what keeps
			// every process in a fleet from queueing its own copy.
			if _, err := store.EnqueueOperation(schemaCtx, storage.EnqueueRequest{
				Operation: storage.Operation{
					BankID:           bankID,
					OperationType:    storage.OperationTypeConsolidate,
					SerializationKey: storage.ConsolidationSerializationKey(bankID),
				},
			}); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				slog.Default().Warn("could not schedule consolidation for a bank",
					"bank_id", bankID, "schema", schema, "error", err)
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
		schemaCtx, schemaErr := storage.WithSchema(ctx, schema)
		if schemaErr != nil {
			return schemaErr
		}
		scheduled, err := discoverer.MentalModelsWithSchedule(schemaCtx, schema)
		if err != nil {
			return fmt.Errorf("discover scheduled mental models in %s: %w", schema, err)
		}
		for _, entry := range scheduled {
			due, full := dueScheduledRefresh(entry, now)
			if !due {
				continue
			}
			model, err := store.GetMentalModel(schemaCtx, entry.BankID, entry.MentalModelID)
			if err != nil {
				// The model may have been deleted between discovery and here.
				continue
			}
			// A model whose last refresh failed is parked against every automatic
			// trigger, the full schedule included, so a tick does not re-queue the
			// same doomed run.
			//
			// A paused model is skipped by *submitting*, which is where the
			// reference's pause check sits (memory_engine.py:24146) — after the
			// tick's own gate. The difference is what a full tick does with its
			// clock: the gate above is "a refresh has run since the last full
			// one", and only a full run moves that clock, so a model skipped
			// before it is stamped stays due on every tick forever. A paused
			// full tick therefore stamps the clock it just answered for: it
			// found nothing to correct, which is the condition the schedule
			// exists to look for.
			if refreshPaused(model) {
				if full {
					if err := storage.StampFullRefresh(schemaCtx, store, entry.BankID, entry.MentalModelID); err != nil {
						return fmt.Errorf("stamp skipped full refresh for %s: %w", entry.MentalModelID, err)
					}
				}
				continue
			}
			// The plain schedule only runs when the model is stale: a tick that
			// finds nothing new must not spend a model call to regenerate identical
			// content. The full schedule skips this gate — it was already gated on
			// "a refresh ran since the last full one", and drift left by past
			// edits is still there on a bank nothing has been written to.
			if !full && !store.IsStale(schemaCtx, entry.BankID, model) {
				continue
			}
			payload := map[string]any{
				storage.MentalModelIDPayloadKey:    entry.MentalModelID,
				storage.AutomaticRefreshPayloadKey: true,
			}
			if full {
				payload[storage.RefreshModePayloadKey] = refreshModeFull
			}
			// An automatic refresh folds into a running one: the caller did not
			// ask for this specific run, and a running refresh covers its
			// intent. The enqueue is idempotent inside the database, which is
			// what keeps a fleet from queueing one wave per process. A full tick
			// that folds into a queued plain refresh upgrades it, so its "full"
			// is not silently dropped.
			enqueued, err := store.EnqueueOperation(schemaCtx, storage.EnqueueRequest{
				Operation: storage.Operation{
					BankID:           entry.BankID,
					OperationType:    storage.OperationTypeRefreshMentalModel,
					Payload:          payload,
					SerializationKey: storage.MentalModelSerializationKey(entry.MentalModelID),
				},
				DedupeByPayloadKey:       storage.MentalModelIDPayloadKey,
				DedupeIncludesProcessing: true,
			})
			if err == nil && full {
				_, err = storage.PromoteQueuedRefreshToFull(schemaCtx, store, entry.BankID, enqueued.Operation.OperationID)
			}
			if err != nil {
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

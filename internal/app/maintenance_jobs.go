package app

import (
	"context"
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

// operationCleanupBatch bounds one schema's operation prune when the
// configuration leaves it unset.
const operationCleanupBatch = 1000

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
	return maintenanceJobsWithStores(store, model, reflection, tenants, cfg, vectorIndex, nil, nil)
}

// bankConfigAnswers is what a maintenance job asks about one bank's effective
// configuration: does it resolve to this value.
//
// It is a narrow function rather than a config map because the reconcile only
// ever asks one question of it, and the answer is per bank.
type bankConfigAnswers func(ctx context.Context, schema, bankID, key string) (bool, error)

// maintenanceJobsWithStores builds every job the deployment's stores allow.
//
// The personal aggregate and the project merge are separate stores for separate
// planes: the aggregate is a person's own banks folded into one, the merge is a
// project's members' facts folded into a team bank. Either may be nil, and a nil
// store registers no job for its plane rather than an idle one.
//
// The four jobs the reference's MaintenanceLoop runs come first and in its
// order (engine/maintenance.py:225-242); the jobs this service adds follow.
func maintenanceJobsWithStores(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
	vectorIndex config.VectorIndexConfig,
	merges storage.MergeStore,
	personals storage.PersonalStore,
) []maintenance.Job {
	return maintenanceJobsWithBankConfig(store, model, reflection, tenants, cfg, vectorIndex, merges, personals, nil)
}

// maintenanceJobsWithBankConfig is maintenanceJobsWithStores plus the bank
// configuration resolver the consolidation reconcile needs.
//
// The reconcile mirrors the retain-time gate: an operation is only scheduled for
// a bank whose *effective* configuration enables observations, not merely one
// whose auto-consolidation switch is on (engine/maintenance.py:483-486). Nil
// means the caller has no resolver, and the job then schedules on the discovery
// routine's answer alone.
func maintenanceJobsWithBankConfig(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
	vectorIndex config.VectorIndexConfig,
	merges storage.MergeStore,
	personals storage.PersonalStore,
	enabled bankConfigAnswers,
) []maintenance.Job {
	jobs := make([]maintenance.Job, 0, 8)

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
				return reconcileConsolidationWithConfig(ctx, store, tenants, enabled)
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
	if cfg.PersonalRollupEnabled && cfg.PersonalRollupInterval > 0 && personals != nil {
		runner := &personalRunner{store: personals, models: func(string) provider.Provider { return model }, cfg: cfg}
		jobs = append(jobs, maintenance.Job{
			Name:     "personal.rollup",
			Interval: cfg.PersonalRollupInterval,
			Run: func(ctx context.Context) error {
				return runner.runPersonalRounds(ctx)
			},
		})
	}
	if cfg.ProjectMergeEnabled && cfg.ProjectMergeInterval > 0 && merges != nil {
		runner := &mergeRunner{merges: merges, store: store, cfg: cfg}
		jobs = append(jobs, maintenance.Job{
			Name:     "project.merge",
			Interval: cfg.ProjectMergeInterval,
			Run: func(ctx context.Context) error {
				return runner.runProjectMerges(ctx)
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
// chunks, across the schemas that hold expired rows.
//
// The window is computed once for the whole sweep rather than per chunk: a
// per-chunk clock would let the cutoff creep forward mid-run, which makes
// "a short chunk means the range is drained" untrue. Only schemas the discovery
// routine reports are visited — a deployment with thousands of tenants must not
// open one delete statement per schema per sweep.
//
// One schema failing does not stop the others: the reference catches per schema
// and continues (engine/maintenance.py:294-298), because the rest still have
// expired rows and the next sweep retries this one.
func purgeExpired(ctx context.Context, store storage.Store, tenants tenant.Extension, table, tsColumn string, days int) error {
	if days <= 0 {
		return nil
	}
	purger, ok := store.(storage.RetentionPurger)
	if !ok {
		return nil
	}
	schemas, err := expiredRowSchemas(ctx, store, tenants, table, tsColumn, days)
	if err != nil {
		// A failed discovery is the reference's "nothing to sweep this tick":
		// logged, not a job failure (engine/maintenance.py:282-284).
		slog.Default().Warn("retention sweep discovery failed", "table", table, "error", err)
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	var firstErr error
	for _, schema := range schemas {
		removed, err := purger.PurgeExpiredRows(ctx, schema, table, tsColumn, cutoff, retentionBatch, retentionMaxBatches)
		if err != nil {
			slog.Default().Warn("retention sweep failed for a schema", "schema", schema, "table", table, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if removed > 0 {
			slog.Default().Info("retention sweep deleted rows", "schema", schema, "table", table, "deleted", removed)
		}
	}
	return firstErr
}

// expiredRowSchemas resolves which schemas the sweep must visit.
//
// The database answers with the reference's schemas_with_expired_rows() probe;
// a backend without it reports every schema the deployment serves, which is what
// that probe would return for a deployment where everything is expired.
func expiredRowSchemas(ctx context.Context, store storage.Store, tenants tenant.Extension, table, tsColumn string, days int) ([]string, error) {
	finder, ok := store.(storage.ExpiredRowFinder)
	if !ok {
		return tenantSchemas(ctx, tenants)
	}
	routineSchema, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return nil, err
	}
	if len(routineSchema) == 0 {
		return nil, nil
	}
	return finder.SchemasWithExpiredRows(ctx, routineSchema[0], table, tsColumn, days)
}

// reconcileConsolidation re-schedules consolidation for banks whose eligible
// facts have none queued.
//
// A fact is stranded when a consolidation operation failed terminally after the
// fact was written, so nothing will pick it up again. This is what finds those
// facts and requeues them.
func reconcileConsolidation(ctx context.Context, store storage.Store, tenants tenant.Extension) error {
	return reconcileConsolidationWithConfig(ctx, store, tenants, nil)
}

// reconcileConsolidationWithConfig is the reconcile with the bank-configuration
// gate the reference applies before it schedules anything.
func reconcileConsolidationWithConfig(ctx context.Context, store storage.Store, tenants tenant.Extension, enabled bankConfigAnswers) error {
	finder, ok := store.(storage.ConsolidationReconciler)
	if !ok {
		return nil
	}
	routineSchema, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	if len(routineSchema) == 0 {
		return nil
	}
	targets, err := finder.BanksNeedingConsolidation(ctx, routineSchema[0])
	if err != nil {
		// A failed discovery leaves this tick with nothing to schedule, which is
		// the reference's answer to it (engine/maintenance.py:435-437).
		slog.Default().Warn("consolidation reconcile discovery failed", "error", err)
		return nil
	}
	if len(targets) == 0 {
		return nil
	}
	known, err := knownSchemas(ctx, tenants, routineSchema[0])
	if err != nil {
		slog.Default().Warn("consolidation reconcile tenant discovery failed", "error", err)
		return nil
	}
	submitted, skipped, skippedDisabled := 0, 0, 0
	for _, target := range targets {
		// Only enqueue into schemas the worker actually polls: an operation in a
		// schema without a tenant never gets claimed, and would block every later
		// reconcile of that bank (engine/maintenance.py:445-477).
		if !known[target.Schema] {
			skipped++
			continue
		}
		if strings.TrimSpace(target.BankID) == "" {
			continue
		}
		schemaCtx, schemaErr := storage.WithSchema(ctx, target.Schema)
		if schemaErr != nil {
			return schemaErr
		}
		// The reference re-checks the resolved bank configuration before it
		// schedules, mirroring the retain-time auto-consolidation gate: an
		// observation-disabled bank is not given consolidation work
		// (engine/maintenance.py:483-486). The discovery routine already filters
		// on enable_auto_consolidation; this adds the other half of the gate.
		if enabled != nil {
			observations, configErr := enabled(schemaCtx, target.Schema, target.BankID, "enable_observations")
			if configErr != nil {
				slog.Default().Warn("could not resolve a bank's configuration for consolidation",
					"bank_id", target.BankID, "schema", target.Schema, "error", configErr)
				continue
			}
			if !observations {
				skippedDisabled++
				continue
			}
		}
		// One bank's failure must not strand the rest: a bank deleted between
		// the scan and the insert would otherwise abort the sweep before every
		// later bank is reached (engine/maintenance.py:489-490).
		//
		// The enqueue itself is idempotent inside the database, which is what
		// keeps every process in a fleet from queueing its own copy.
		if _, err := store.EnqueueOperation(schemaCtx, storage.EnqueueRequest{
			Operation: storage.Operation{
				BankID:           target.BankID,
				OperationType:    storage.OperationTypeConsolidate,
				SerializationKey: storage.ConsolidationSerializationKey(target.BankID),
			},
		}); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			slog.Default().Warn("could not schedule consolidation for a bank",
				"bank_id", target.BankID, "schema", target.Schema, "error", err)
			continue
		}
		submitted++
	}
	if submitted > 0 || skipped > 0 || skippedDisabled > 0 {
		attrs := []any{"scheduled", submitted}
		if skipped > 0 {
			attrs = append(attrs, "skipped_unknown_schemas", skipped)
		}
		if skippedDisabled > 0 {
			attrs = append(attrs, "skipped_observations_disabled", skippedDisabled)
		}
		slog.Default().Info("consolidation reconcile", attrs...)
	}
	return nil
}

// knownSchemas is the set a job may enqueue into: the tenant discovery's
// schemas plus the deployment's own.
func knownSchemas(ctx context.Context, tenants tenant.Extension, defaultSchema string) (map[string]bool, error) {
	schemas, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(schemas)+1)
	for _, schema := range schemas {
		known[schema] = true
	}
	if defaultSchema != "" {
		known[defaultSchema] = true
	}
	return known, nil
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
	routineSchema, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	if len(routineSchema) == 0 {
		return nil
	}
	scheduled, err := discoverer.MentalModelsWithSchedule(ctx, routineSchema[0])
	if err != nil {
		// A failed discovery leaves this tick with nothing to refresh, which is
		// the reference's answer to it (engine/maintenance.py:566-568).
		slog.Default().Warn("scheduled mental model refresh discovery failed", "error", err)
		return nil
	}
	if len(scheduled) == 0 {
		return nil
	}
	now := time.Now().UTC()
	due := make([]scheduledRefreshDue, 0, len(scheduled))
	for _, entry := range scheduled {
		fire, full := dueScheduledRefresh(entry, now)
		if fire {
			due = append(due, scheduledRefreshDue{entry: entry, full: full})
		}
	}
	if len(due) == 0 {
		return nil
	}
	known, err := knownSchemas(ctx, tenants, routineSchema[0])
	if err != nil {
		slog.Default().Warn("scheduled mental model refresh tenant discovery failed", "error", err)
		return nil
	}
	counts := refreshTally{}
	for _, item := range due {
		outcome, err := refreshOneDueModel(ctx, store, refresh, item, known)
		if err != nil {
			slog.Default().Warn("scheduled mental model refresh failed",
				"mental_model_id", item.entry.MentalModelID, "schema", item.entry.Schema, "error", err)
			continue
		}
		counts.add(outcome)
	}
	counts.log()
	return nil
}

// scheduledRefreshDue is one discovery row whose schedule fired.
type scheduledRefreshDue struct {
	entry storage.ScheduledMentalModel
	full  bool
}

// refreshOutcome is what the tick's gate and the enqueue decided for one model.
type refreshOutcome int

const (
	refreshSubmitted refreshOutcome = iota
	refreshSkippedFresh
	refreshSkippedInFlight
	refreshSkippedPaused
	refreshSkippedUnknown
	refreshVanished
)

// refreshTally is the per-tick counter block the reference logs
// (engine/maintenance.py:677-684).
type refreshTally struct {
	submitted int
	fresh     int
	inFlight  int
	paused    int
	unknown   int
}

func (t *refreshTally) add(outcome refreshOutcome) {
	switch outcome {
	case refreshSubmitted:
		t.submitted++
	case refreshSkippedFresh:
		t.fresh++
	case refreshSkippedInFlight:
		t.inFlight++
	case refreshSkippedPaused:
		t.paused++
	case refreshSkippedUnknown:
		t.unknown++
	}
}

// log emits the reference's per-tick summary line, with only the non-zero parts.
func (t *refreshTally) log() {
	attrs := []any{"scheduled", t.submitted}
	if t.fresh > 0 {
		attrs = append(attrs, "up_to_date", t.fresh)
	}
	if t.inFlight > 0 {
		attrs = append(attrs, "already_in_flight", t.inFlight)
	}
	if t.paused > 0 {
		attrs = append(attrs, "paused_after_failed_refresh", t.paused)
	}
	if t.unknown > 0 {
		attrs = append(attrs, "skipped_unknown_schemas", t.unknown)
	}
	slog.Default().Info("scheduled mental model refresh", attrs...)
}

// refreshOneDueModel applies the reference's per-model gates in its order.
func refreshOneDueModel(ctx context.Context, store storage.Store, refresh mentalModelRefresh, item scheduledRefreshDue, known map[string]bool) (refreshOutcome, error) {
	entry := item.entry
	if !known[entry.Schema] {
		return refreshSkippedUnknown, nil
	}
	schemaCtx, schemaErr := storage.WithSchema(ctx, entry.Schema)
	if schemaErr != nil {
		return refreshVanished, schemaErr
	}
	model, err := store.GetMentalModel(schemaCtx, entry.BankID, entry.MentalModelID)
	if err != nil {
		// The model may have been deleted between discovery and here.
		return refreshVanished, nil
	}
	// The plain schedule only runs when the model is stale: a tick that finds
	// nothing new must not spend a model call to regenerate identical content.
	// The full schedule skips this gate — it was already gated on "a refresh ran
	// since the last full one", and drift left by past edits is still there on a
	// bank nothing has been written to.
	if !item.full && !store.IsStale(schemaCtx, entry.BankID, model) {
		return refreshSkippedFresh, nil
	}
	// A model whose last refresh failed is parked against every automatic
	// trigger, the full schedule included, so a tick does not re-queue the same
	// doomed run. The reference counts it and moves on
	// (engine/maintenance.py:666-667).
	if refreshPaused(model) {
		return refreshSkippedPaused, nil
	}
	payload := map[string]any{
		storage.MentalModelIDPayloadKey:    entry.MentalModelID,
		storage.AutomaticRefreshPayloadKey: true,
	}
	if item.full {
		payload[storage.RefreshModePayloadKey] = refreshModeFull
	}
	// An automatic refresh folds into a running one: the caller did not ask for
	// this specific run, and a running refresh covers its intent. The enqueue is
	// idempotent inside the database, which is what keeps a fleet from queueing
	// one wave per process.
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
	if err != nil {
		return refreshVanished, fmt.Errorf("schedule refresh for %s: %w", entry.MentalModelID, err)
	}
	if enqueued.Deduplicated {
		return refreshSkippedInFlight, nil
	}
	if item.full {
		// A full tick that folds into a queued plain refresh upgrades it, so its
		// "full" is not silently dropped.
		if _, err := storage.PromoteQueuedRefreshToFull(schemaCtx, store, entry.BankID, enqueued.Operation.OperationID); err != nil {
			return refreshVanished, fmt.Errorf("promote refresh to full for %s: %w", entry.MentalModelID, err)
		}
	}
	return refreshSubmitted, nil
}

// pruneTerminalOperations removes settled operations past their retention.
//
// Discovery names the schemas holding expired rows — one cross-tenant
// round-trip, not a prune transaction per tenant — and only schemas the
// deployment actually serves are pruned (engine/maintenance.py:379-387).
func pruneTerminalOperations(ctx context.Context, store storage.Store, tenants tenant.Extension, days, batchSize int) error {
	if days <= 0 {
		return nil
	}
	pruner, ok := store.(storage.OperationPruner)
	if !ok {
		return nil
	}
	if batchSize <= 0 {
		batchSize = operationCleanupBatch
	}
	routineSchema, err := tenantSchemas(ctx, tenants)
	if err != nil {
		return err
	}
	if len(routineSchema) == 0 {
		return nil
	}
	finder, hasFinder := store.(storage.SchemasWithExpiredOperations)
	var schemas []string
	if hasFinder {
		schemas, err = finder.SchemasWithExpiredOperations(ctx, routineSchema[0], days)
		if err != nil {
			slog.Default().Warn("operation cleanup discovery failed", "error", err)
			return nil
		}
	} else {
		schemas = routineSchema
	}
	if len(schemas) == 0 {
		return nil
	}
	known, err := knownSchemas(ctx, tenants, routineSchema[0])
	if err != nil {
		slog.Default().Warn("operation cleanup tenant discovery failed", "error", err)
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	pruned := 0
	var firstErr error
	for _, schema := range schemas {
		if !known[schema] {
			continue
		}
		// Delete the export archives the rows own before the rows go, so the
		// blobs do not outlive their only handle. Best-effort, and bounded by the
		// same window the prune walks.
		pruner.PurgeExpiredExportArchives(ctx, schema, cutoff, batchSize)
		deleted, err := pruner.PruneTerminalOperations(ctx, schema, cutoff, batchSize)
		if err != nil {
			slog.Default().Warn("operation cleanup failed for a schema", "schema", schema, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if deleted > 0 {
			pruned += deleted
			slog.Default().Info("operation cleanup pruned expired terminal operations",
				"schema", schema, "deleted", deleted)
		}
	}
	if pruned > 0 {
		slog.Default().Info("operation cleanup", "pruned", pruned)
	}
	return firstErr
}

// tenantSchemas resolves the schemas a job should cover.
func tenantSchemas(ctx context.Context, tenants tenant.Extension) ([]string, error) {
	if tenants == nil {
		return nil, fmt.Errorf("maintenance requires a tenant extension")
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

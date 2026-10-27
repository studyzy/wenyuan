package app

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The backlog gauge names, matching the reference's instruments. The reference
// exposes the async-operation queue as one series per operation type and status,
// and the consolidation backlog as one pair per tenant (or per bank).
const (
	metricAsyncOperations      = "wenyuan_async_operations"
	metricConsolidationBacklog = "wenyuan_consolidation_backlog"
	metricConsolidationFailed  = "wenyuan_consolidation_failed"
)

// The backlog HELP text, copied from the reference's instrument descriptions.
const (
	helpAsyncOperations      = "Async operations in a non-terminal state, by operation_type and status (pending=queued backlog, processing=in-flight, failed=stranded)"
	helpConsolidationBacklog = "Source memories (experience/world) still queued for consolidation into observations; excludes permanently failed ones, which are in hindsight.consolidation.failed"
	helpConsolidationFailed  = "Source memories whose consolidation permanently failed (recoverable via the consolidation recovery endpoint)"
)

// The database-pool gauge names, matching the reference's instruments.
const (
	metricDBPoolSize = "wenyuan_db_pool_size"
	metricDBPoolIdle = "wenyuan_db_pool_idle"
	metricDBPoolMin  = "wenyuan_db_pool_min"
	metricDBPoolMax  = "wenyuan_db_pool_max"
	// metricDBPoolWaiting is the callers-queued gauge. The reference reports it
	// from its pool instrumentation, so it is present even at zero.
	metricDBPoolWaiting = "wenyuan_db_pool_waiting"
)

// expositionExtras composes every family the exposition renders beyond the HTTP
// instruments: the backlog gauges, the database-pool gauges and the process
// gauges. Each provider is gated by configuration or by the store's
// capabilities, so a scrape carries exactly the families the deployment can
// report and nothing it cannot.
func expositionExtras(store storage.Store, cfg config.Config) func() []monitoring.Metric {
	backlog := backlogMetrics(store, cfg)
	pool := dbPoolMetrics(store, cfg)
	return func() []monitoring.Metric {
		var out []monitoring.Metric
		if backlog != nil {
			out = append(out, backlog()...)
		}
		if pool != nil {
			out = append(out, pool()...)
		}
		out = append(out, monitoring.ProcessMetrics()...)
		if len(out) == 0 {
			return nil
		}
		return out
	}
}

// backlogMetrics reports the async-operation queue and the consolidation backlog
// as Prometheus gauges, so a scrape shows how much queued work a deployment is
// carrying. The reference provisions these gauges only when
// metrics_backlog_enabled is set, so an unset key yields a nil renderer and no
// series at all rather than a zeroed one. The counts are computed at scrape time
// and bounded, so a slow store cannot hang the scrape.
func backlogMetrics(store storage.Store, cfg config.Config) func() []monitoring.Metric {
	if !cfg.MetricsTuning.BacklogEnabled {
		return nil
	}
	return func() []monitoring.Metric {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out := asyncOperationSamples(ctx, store, cfg)
		page, err := store.ListBanks(ctx, storage.BankQuery{Limit: 1000})
		if err != nil {
			return out
		}
		return append(out, consolidationSamples(ctx, store, page.Banks, cfg)...)
	}
}

// asyncOperationSamples renders one series per (operation_type, status[,bank_id])
// bucket of non-terminal operations. Each carries the tenant label, as the
// reference does unconditionally for this gauge, and the bank_id label only when
// the bank-id label is enabled.
func asyncOperationSamples(ctx context.Context, store storage.Store, cfg config.Config) []monitoring.Metric {
	counter, ok := store.(storage.AsyncOperationCounter)
	if !ok {
		return nil
	}
	perBank := cfg.MetricsTuning.IncludeBankID
	groups, err := counter.CountAsyncOperations(ctx, perBank)
	if err != nil {
		return nil
	}
	out := make([]monitoring.Metric, 0, len(groups))
	for _, group := range groups {
		labels := map[string]string{
			"tenant":         cfg.DatabaseSchema,
			"operation_type": group.OperationType,
			"status":         group.Status,
		}
		if perBank && group.BankID != "" {
			labels["bank_id"] = group.BankID
		}
		out = append(out, monitoring.Metric{
			Name: metricAsyncOperations, Help: helpAsyncOperations,
			Value: float64(group.Count), Labels: labels,
		})
	}
	return out
}

// consolidationSamples reports the consolidation backlog and stranded counts.
// With the bank-id label on each bank gets its own pair; otherwise the counts
// are summed into one pair per tenant, the shape the reference produces when
// bank-id labelling is off.
func consolidationSamples(ctx context.Context, store storage.Store, banks []storage.Bank, cfg config.Config) []monitoring.Metric {
	if cfg.MetricsTuning.IncludeBankID {
		out := make([]monitoring.Metric, 0, len(banks)*2)
		for _, bank := range banks {
			stats, err := store.BankStats(ctx, bank.BankID)
			if err != nil {
				continue
			}
			out = append(out, consolidationPair(stats.PendingConsolidation, stats.FailedConsolidation,
				map[string]string{"tenant": cfg.DatabaseSchema, "bank_id": bank.BankID})...)
		}
		return out
	}
	var pending, failed int
	for _, bank := range banks {
		stats, err := store.BankStats(ctx, bank.BankID)
		if err != nil {
			continue
		}
		pending += stats.PendingConsolidation
		failed += stats.FailedConsolidation
	}
	return consolidationPair(pending, failed, map[string]string{"tenant": cfg.DatabaseSchema})
}

// consolidationPair renders the backlog and failed gauges for one label set.
func consolidationPair(pending, failed int, labels map[string]string) []monitoring.Metric {
	return []monitoring.Metric{
		{Name: metricConsolidationBacklog, Help: helpConsolidationBacklog, Value: float64(pending), Labels: labels},
		{Name: metricConsolidationFailed, Help: helpConsolidationFailed, Value: float64(failed), Labels: labels},
	}
}

// dbPoolMetrics reports the database connection-pool gauges. A store that does
// not expose its pool yields a nil renderer, so a non-pool backend reports no
// pool series. The waiter gauge comes from the store's acquire instrumentation
// and is reported whenever the store can count, which is what lets a saturated
// pool be told apart from a healthy one.
func dbPoolMetrics(store storage.Store, cfg config.Config) func() []monitoring.Metric {
	provider, ok := store.(storage.PoolStatter)
	if !ok {
		return nil
	}
	return func() []monitoring.Metric {
		stats, ok := provider.PoolStats()
		if !ok {
			return nil
		}
		max := stats.Max
		if max <= 0 {
			max = cfg.DBPoolMaxSize
		}
		series := []monitoring.Metric{
			{Name: metricDBPoolSize, Help: "Current number of connections in the pool", Value: float64(stats.InUse + stats.Idle)},
			{Name: metricDBPoolIdle, Help: "Number of idle connections in the pool", Value: float64(stats.Idle)},
			{Name: metricDBPoolMin, Help: "Minimum pool size", Value: float64(cfg.DBPoolMinSize)},
			{Name: metricDBPoolMax, Help: "Maximum pool size", Value: float64(max)},
		}
		if waiter, ok := store.(interface {
			PoolWaiting() (int, bool)
		}); ok {
			if waiting, ok := waiter.PoolWaiting(); ok {
				series = append(series, monitoring.Metric{
					Name:  metricDBPoolWaiting,
					Help:  "Callers currently blocked waiting to acquire a pooled connection",
					Value: float64(waiting),
				})
			}
		}
		return series
	}
}

// loopLagHistograms exposes the event-loop lag histogram when the deployment
// opts in. The probe records into the package-level accumulator regardless, so
// enabling the metric surfaces the samples gathered since start.
func loopLagHistograms(tuning config.MetricsTuning) func() []monitoring.Histogram {
	if !tuning.LoopLagMetric {
		return nil
	}
	return func() []monitoring.Histogram {
		return []monitoring.Histogram{monitoring.LoopLag.Snapshot()}
	}
}

// WorkerMetricsHandler builds the metrics exposition handler the worker
// process serves on its own port. It carries the same backlog gauges and loop
// lag histogram as the API server's, so a scrape of either process sees the
// deployment's metrics.
func WorkerMetricsHandler(store storage.Store, cfg config.Config) *monitoring.MetricsHandler {
	return &monitoring.MetricsHandler{
		Extra:      expositionExtras(store, cfg),
		Histograms: loopLagHistograms(cfg.MetricsTuning),
		Tenant:     metricsTenant(cfg),
	}
}

// metricsTenant returns the tenant label value for the exposition: the database
// schema the deployment is scoped to, or empty when the deployment has not opted
// into the label.
func metricsTenant(cfg config.Config) string {
	if !cfg.MetricsTuning.IncludeTenant {
		return ""
	}
	return cfg.DatabaseSchema
}

// recallPhaseRecorder forwards the store's per-phase recall observations into
// the workload registry, so the recall-phase histogram is populated from the
// same accounting the `[phases]` log line prints. The phase names, the
// diagnostic split and the sampling all come from the store, so the histogram
// carries exactly the phases a recall measured.
type recallPhaseRecorder struct{}

// ObserveRecallPhase records one phase of a recall.
func (recallPhaseRecorder) ObserveRecallPhase(observation storage.RecallPhaseObservation) {
	monitoring.Workload.ObserveRecallPhase(observation.Phase, observation.Diagnostic, observation.Seconds)
}

// InstallRecallPhaseObserver wires the store's phase observer when it offers
// one, so a scrape of this process carries the recall-phase histogram. A store
// without the seam — or with the phase accounting off — records no phases and
// the family stays absent.
func InstallRecallPhaseObserver(store storage.Store) {
	installer, ok := store.(interface {
		SetRecallPhaseObserver(storage.RecallPhaseObserver)
	})
	if !ok {
		return
	}
	installer.SetRecallPhaseObserver(recallPhaseRecorder{})
}

// retainPhaseRecorder forwards the store's per-phase retain observations into
// the workload registry, so the retain-phase families are populated from the
// same accounting the store's own phase hooks collect: the embedder's time and
// each pooled-connection acquire. A phase the deployment never reaches records
// no series.
type retainPhaseRecorder struct{}

// ObserveRetainPhase records one phase of a retain.
func (retainPhaseRecorder) ObserveRetainPhase(observation storage.RetainPhaseObservation) {
	monitoring.Workload.ObserveRetainPhase(observation.Phase, observation.Store, observation.Seconds, observation.Calls)
}

// InstallRetainPhaseObserver wires the store's retain-phase observer into the
// workload registry, so a scrape of this process carries the retain-phase
// families. A process that installs nothing records no phases and both families
// stay absent, which is the reference's behaviour outside a retain.
func InstallRetainPhaseObserver() {
	storage.SetRetainPhaseObserver(retainPhaseRecorder{})
}

// timingValidator wraps a deployment's operation validator so each hook it
// implements records its own duration. The validator runs outside the
// operation's own timer — before the work starts and after it ends — so
// whatever it does (a database read, a charge) is otherwise invisible in the
// operation's own accounting.
//
// Wrapped once, at registration, rather than timed at each call site: the
// reference instruments the validator's hooks the same way, so a hook costs one
// timer wherever it is called from. The hook and operation labels are the
// reference's: an operation gate is the "pre" hook of its operation, and the
// tag-scope resolvers carry no operation-verb prefix, so they are their own.
type timingValidator struct {
	inner httpx.OperationValidator
}

// ValidateOperation times the operation gate, which the reference labels as the
// "pre" hook of the operation it gates.
func (v timingValidator) ValidateOperation(ctx context.Context, op httpx.OperationRequest) httpx.OperationDecision {
	start := time.Now()
	decision := v.inner.ValidateOperation(ctx, op)
	monitoring.Workload.ObserveValidatorPhase(op.Operation, "pre", time.Since(start).Seconds())
	return decision
}

// ResolveTagScope times the read-scope resolver, whose hook name carries no
// operation verb and so reports under "other", as the reference does.
func (v timingValidator) ResolveTagScope(ctx context.Context, bankID string) []httpx.TagGroup {
	start := time.Now()
	scope := v.inner.ResolveTagScope(ctx, bankID)
	monitoring.Workload.ObserveValidatorPhase("resolve_tag_scope", "other", time.Since(start).Seconds())
	return scope
}

// ValidateBankWrite times the bank-write gate. A validator that carries no bank
// rule accepts every write without a timer, exactly as it did before wrapping.
func (v timingValidator) ValidateBankWrite(ctx context.Context, bankID, apiKey string) httpx.OperationDecision {
	gate, ok := v.inner.(httpx.BankWriteGate)
	if !ok {
		return httpx.OperationDecision{Allowed: true}
	}
	start := time.Now()
	decision := gate.ValidateBankWrite(ctx, bankID, apiKey)
	monitoring.Workload.ObserveValidatorPhase("bank_write", "pre", time.Since(start).Seconds())
	return decision
}

// ResolveWriteTagScope times the write-scope resolver, reporting under "other"
// as its name carries no operation verb. A validator that carries no write
// scope confines nobody, exactly as before wrapping.
func (v timingValidator) ResolveWriteTagScope(ctx context.Context, bankID string) ([]string, bool) {
	resolver, ok := v.inner.(httpx.TagWriteScopeResolver)
	if !ok {
		return nil, false
	}
	start := time.Now()
	tags, confined := resolver.ResolveWriteTagScope(ctx, bankID)
	monitoring.Workload.ObserveValidatorPhase("resolve_write_tag_scope", "other", time.Since(start).Seconds())
	return tags, confined
}

// instrumentOperationValidator wraps a validator so every hook it implements
// reports its duration. A nil validator is returned unchanged, so an unextended
// deployment records no validator phases and the family stays absent.
func instrumentOperationValidator(v httpx.OperationValidator) httpx.OperationValidator {
	if v == nil {
		return nil
	}
	return timingValidator{inner: v}
}

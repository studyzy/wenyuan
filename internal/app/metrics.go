package app

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The backlog gauge names. They are emitted as one aggregate pair by default and
// as one pair per bank when the bank-id label is enabled.
const (
	metricAsyncOperationsPending = "hindsight_async_operations_pending"
	metricAsyncOperationsFailed  = "hindsight_async_operations_failed"
)

// operationMetrics reports the async-operation backlog as Prometheus gauges, so
// a scrape shows how much queued work a deployment is carrying. The reference
// provisions the backlog gauges only when metrics_backlog_enabled is set, so an
// unset key yields a nil renderer and no series at all rather than a zeroed one.
// The counts are computed at scrape time and bounded, so a slow store cannot hang
// the scrape.
func operationMetrics(store storage.Store, tuning config.MetricsTuning) func() []monitoring.Metric {
	if !tuning.BacklogEnabled {
		return nil
	}
	return func() []monitoring.Metric {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		page, err := store.ListBanks(ctx, storage.BankQuery{Limit: 1000})
		if err != nil {
			return nil
		}
		if tuning.IncludeBankID {
			return perBankBacklogMetrics(ctx, store, page.Banks)
		}
		return aggregateBacklogMetrics(ctx, store, page.Banks)
	}
}

// aggregateBacklogMetrics sums the backlog over every bank, the shape the
// reference produces when the bank-id label is off.
func aggregateBacklogMetrics(ctx context.Context, store storage.Store, banks []storage.Bank) []monitoring.Metric {
	var pending, failed float64
	for _, bank := range banks {
		stats, err := store.BankStats(ctx, bank.BankID)
		if err != nil {
			continue
		}
		pending += float64(stats.PendingOperations)
		failed += float64(stats.FailedOperations)
	}
	return []monitoring.Metric{
		{Name: metricAsyncOperationsPending, Help: "Async operations queued but not yet finished", Value: pending},
		{Name: metricAsyncOperationsFailed, Help: "Async operations that exhausted their retries", Value: failed},
	}
}

// perBankBacklogMetrics emits one pending and one failed series per bank, each
// carrying the bank_id label, the shape the reference produces when the bank-id
// label is on.
func perBankBacklogMetrics(ctx context.Context, store storage.Store, banks []storage.Bank) []monitoring.Metric {
	out := make([]monitoring.Metric, 0, len(banks)*2)
	for _, bank := range banks {
		stats, err := store.BankStats(ctx, bank.BankID)
		if err != nil {
			continue
		}
		labels := map[string]string{"bank_id": bank.BankID}
		out = append(out,
			monitoring.Metric{Name: metricAsyncOperationsPending, Help: "Async operations queued but not yet finished", Value: float64(stats.PendingOperations), Labels: labels},
			monitoring.Metric{Name: metricAsyncOperationsFailed, Help: "Async operations that exhausted their retries", Value: float64(stats.FailedOperations), Labels: labels},
		)
	}
	return out
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

// metricsTenant returns the tenant label value for the exposition: the database
// schema the deployment is scoped to, or empty when the deployment has not opted
// into the label.
func metricsTenant(cfg config.Config) string {
	if !cfg.MetricsTuning.IncludeTenant {
		return ""
	}
	return cfg.DatabaseSchema
}

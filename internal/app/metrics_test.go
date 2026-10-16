package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// statsStore is a store double that serves the two methods operationMetrics
// reads, letting a test script per-bank backlog without a real backend.
type statsStore struct {
	storage.Store
	banks []storage.Bank
	stats map[string]storage.BankStats
	err   error
}

func (s statsStore) ListBanks(context.Context, storage.BankQuery) (storage.BankPage, error) {
	if s.err != nil {
		return storage.BankPage{}, s.err
	}
	return storage.BankPage{Banks: s.banks}, nil
}

func (s statsStore) BankStats(_ context.Context, bankID string) (storage.BankStats, error) {
	return s.stats[bankID], nil
}

// An unset backlog key yields no renderer at all, so the exposition carries no
// backlog series — the reference's default.
func TestOperationMetricsDisabledByDefault(t *testing.T) {
	if got := operationMetrics(statsStore{}, config.MetricsTuning{}); got != nil {
		t.Fatalf("disabled backlog must yield a nil renderer")
	}
}

// With the backlog enabled and no bank-id label the counts are summed across
// banks into one unlabelled series pair.
func TestOperationMetricsAggregate(t *testing.T) {
	store := statsStore{
		banks: []storage.Bank{{BankID: "b1"}, {BankID: "b2"}},
		stats: map[string]storage.BankStats{
			"b1": {PendingOperations: 3, FailedOperations: 1},
			"b2": {PendingOperations: 4},
		},
	}
	samples := operationMetrics(store, config.MetricsTuning{BacklogEnabled: true})()
	if len(samples) != 2 {
		t.Fatalf("samples = %+v", samples)
	}
	if samples[0].Name != "hindsight_async_operations_pending" || samples[0].Value != 7 || samples[0].Labels != nil {
		t.Fatalf("pending sample = %+v", samples[0])
	}
	if samples[1].Name != "hindsight_async_operations_failed" || samples[1].Value != 1 || samples[1].Labels != nil {
		t.Fatalf("failed sample = %+v", samples[1])
	}
}

// With the bank-id label on, each bank gets its own pending and failed series
// carrying the bank_id label instead of one aggregate pair.
func TestOperationMetricsPerBank(t *testing.T) {
	store := statsStore{
		banks: []storage.Bank{{BankID: "b1"}, {BankID: "b2"}},
		stats: map[string]storage.BankStats{
			"b1": {PendingOperations: 3, FailedOperations: 1},
			"b2": {PendingOperations: 4},
		},
	}
	samples := operationMetrics(store, config.MetricsTuning{BacklogEnabled: true, IncludeBankID: true})()
	if len(samples) != 4 {
		t.Fatalf("samples = %+v", samples)
	}
	for _, want := range []struct {
		name   string
		value  float64
		bankID string
	}{
		{"hindsight_async_operations_pending", 3, "b1"},
		{"hindsight_async_operations_failed", 1, "b1"},
		{"hindsight_async_operations_pending", 4, "b2"},
		{"hindsight_async_operations_failed", 0, "b2"},
	} {
		found := false
		for _, s := range samples {
			if s.Name == want.name && s.Value == want.value && s.Labels["bank_id"] == want.bankID {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing series %+v in %+v", want, samples)
		}
	}
}

// A failed bank listing yields no samples rather than a broken scrape.
func TestOperationMetricsStoreError(t *testing.T) {
	store := statsStore{err: fmt.Errorf("boom")}
	if got := operationMetrics(store, config.MetricsTuning{BacklogEnabled: true})(); got != nil {
		t.Fatalf("samples = %+v", got)
	}
}

// The loop-lag histogram is only exposed when the metric is enabled.
func TestLoopLagHistogramsGated(t *testing.T) {
	if got := loopLagHistograms(config.MetricsTuning{}); got != nil {
		t.Fatalf("disabled loop-lag metric must yield a nil renderer")
	}
	families := loopLagHistograms(config.MetricsTuning{LoopLagMetric: true})()
	if len(families) != 1 || families[0].Name != monitoring.LoopLagMetricName {
		t.Fatalf("families = %+v", families)
	}
}

// The tenant label is only produced when the deployment opts in.
func TestMetricsTenantGated(t *testing.T) {
	if got := metricsTenant(config.Config{DatabaseSchema: "tenant_a"}); got != "" {
		t.Fatalf("tenant = %q, want empty", got)
	}
	cfg := config.Config{DatabaseSchema: "tenant_a"}
	cfg.MetricsTuning.IncludeTenant = true
	if got := metricsTenant(cfg); got != "tenant_a" {
		t.Fatalf("tenant = %q, want tenant_a", got)
	}
}

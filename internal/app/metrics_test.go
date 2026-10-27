package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// statsStore is a store double that serves the methods the backlog gauges read,
// letting a test script per-bank consolidation counts and async-operation
// groupings without a real backend.
type statsStore struct {
	storage.Store
	banks  []storage.Bank
	stats  map[string]storage.BankStats
	groups []storage.AsyncOperationGroup
	err    error
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

func (s statsStore) CountAsyncOperations(context.Context, bool) ([]storage.AsyncOperationGroup, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.groups, nil
}

// poolStore is a store double that exposes a database pool, for the pool gauges.
type poolStore struct {
	storage.Store
	stats storage.PoolStats
	ok    bool
}

func (s poolStore) PoolStats() (storage.PoolStats, bool) { return s.stats, s.ok }

func (s poolStore) ListBanks(context.Context, storage.BankQuery) (storage.BankPage, error) {
	return storage.BankPage{}, nil
}

func backlogConfig() config.Config {
	cfg := config.Config{DatabaseSchema: "public"}
	cfg.DBPoolMaxSize = 100
	cfg.DBPoolMinSize = 5
	cfg.MetricsTuning.BacklogEnabled = true
	return cfg
}

// An unset backlog key yields no renderer at all, so the exposition carries no
// backlog series — the reference's default.
func TestBacklogMetricsDisabledByDefault(t *testing.T) {
	if got := backlogMetrics(statsStore{}, config.Config{}); got != nil {
		t.Fatalf("disabled backlog must yield a nil renderer")
	}
}

// With the backlog enabled and no bank-id label, the async queue renders one
// series per (operation_type, status) and the consolidation counts sum across
// banks into one pair, all carrying the tenant label.
func TestBacklogMetricsAggregate(t *testing.T) {
	store := statsStore{
		banks: []storage.Bank{{BankID: "b1"}, {BankID: "b2"}},
		stats: map[string]storage.BankStats{
			"b1": {PendingConsolidation: 3, FailedConsolidation: 1},
			"b2": {PendingConsolidation: 4},
		},
		groups: []storage.AsyncOperationGroup{
			{OperationType: "retain", Status: "pending", Count: 3},
			{OperationType: "consolidate", Status: "processing", Count: 2},
		},
	}
	samples := backlogMetrics(store, backlogConfig())()
	want := map[string]float64{
		`wenyuan_async_operations{operation_type="retain",status="pending",tenant="public"}`:         3,
		`wenyuan_async_operations{operation_type="consolidate",status="processing",tenant="public"}`: 2,
		`wenyuan_consolidation_backlog{tenant="public"}`:                                             7,
		`wenyuan_consolidation_failed{tenant="public"}`:                                              1,
	}
	assertSeries(t, samples, want)
}

// With the bank-id label on, each bank gets its own consolidation pair and each
// async-operation series carries the bank_id label too.
func TestBacklogMetricsPerBank(t *testing.T) {
	cfg := backlogConfig()
	cfg.MetricsTuning.IncludeBankID = true
	store := statsStore{
		banks: []storage.Bank{{BankID: "b1"}, {BankID: "b2"}},
		stats: map[string]storage.BankStats{
			"b1": {PendingConsolidation: 3, FailedConsolidation: 1},
			"b2": {PendingConsolidation: 4},
		},
		groups: []storage.AsyncOperationGroup{
			{OperationType: "retain", Status: "pending", BankID: "b1", Count: 2},
		},
	}
	samples := backlogMetrics(store, cfg)()
	want := map[string]float64{
		`wenyuan_async_operations{bank_id="b1",operation_type="retain",status="pending",tenant="public"}`: 2,
		`wenyuan_consolidation_backlog{bank_id="b1",tenant="public"}`:                                     3,
		`wenyuan_consolidation_failed{bank_id="b1",tenant="public"}`:                                      1,
		`wenyuan_consolidation_backlog{bank_id="b2",tenant="public"}`:                                     4,
		`wenyuan_consolidation_failed{bank_id="b2",tenant="public"}`:                                      0,
	}
	assertSeries(t, samples, want)
}

// A failed store yields no series rather than a broken scrape.
func TestBacklogMetricsStoreError(t *testing.T) {
	store := statsStore{err: fmt.Errorf("boom")}
	samples := backlogMetrics(store, backlogConfig())()
	if len(samples) != 0 {
		t.Fatalf("samples = %+v", samples)
	}
}

// The pool gauges render from the store's pool when it exposes one, and are
// absent for a store that does not.
func TestDBPoolMetrics(t *testing.T) {
	if got := dbPoolMetrics(statsStore{}, backlogConfig()); got != nil {
		t.Fatalf("a store without a pool must yield a nil renderer")
	}
	store := poolStore{stats: storage.PoolStats{InUse: 2, Idle: 3, Max: 100}, ok: true}
	samples := dbPoolMetrics(store, backlogConfig())()
	want := map[string]float64{
		`wenyuan_db_pool_size{}`: 5,
		`wenyuan_db_pool_idle{}`: 3,
		`wenyuan_db_pool_min{}`:  5,
		`wenyuan_db_pool_max{}`:  100,
	}
	assertSeries(t, samples, want)
}

// The exposition's extras compose the backlog, pool and process families.
func TestExpositionExtrasComposes(t *testing.T) {
	store := poolStore{stats: storage.PoolStats{InUse: 2, Idle: 3, Max: 100}, ok: true}
	extras := expositionExtras(store, backlogConfig())
	if extras == nil {
		t.Fatal("extras must be non-nil")
	}
	samples := extras()
	names := map[string]bool{}
	for _, sample := range samples {
		names[sample.Name] = true
	}
	for _, want := range []string{"wenyuan_db_pool_size", "wenyuan_process_threads"} {
		if !names[want] {
			t.Fatalf("missing %q in %+v", want, samples)
		}
	}
}

// assertSeries checks that each expected rendered series is present with its
// value, and no unexpected one.
func assertSeries(t *testing.T, samples []monitoring.Metric, want map[string]float64) {
	t.Helper()
	got := map[string]float64{}
	for _, sample := range samples {
		got[renderSeries(sample)] = sample.Value
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("series %q = %v, want %v (all: %+v)", key, got[key], value, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("series count = %d, want %d: %+v", len(got), len(want), got)
	}
}

// renderSeries renders a sample to the stable "name{labels}" key a test can
// compare against, ordering labels as the exposition format does.
func renderSeries(sample monitoring.Metric) string {
	if len(sample.Labels) == 0 {
		return sample.Name + "{}"
	}
	names := make([]string, 0, len(sample.Labels))
	for name := range sample.Labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(sample.Name)
	b.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(sample.Labels[name])
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
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

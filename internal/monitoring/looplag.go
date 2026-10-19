package monitoring

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LoopLagMetricName is the exposition name of the event-loop lag histogram. The
// reference declares its unit "s", so the exporter appends the "seconds" suffix
// to the instrument name.
const LoopLagMetricName = "hindsight_event_loop_lag_seconds"

// LoopLagBuckets are the histogram boundaries for the event-loop lag metric, in
// seconds. Lag is sub-millisecond on a healthy loop and reaches seconds only
// when the loop is oversubscribed, so the buckets climb from 1ms to 5s.
var LoopLagBuckets = []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1.0, 2.0, 5.0}

// LoopLag is the process-wide event-loop lag histogram. The stall probe records
// every sample into it and the metrics handler renders it, and because the probe
// is started by the process entrypoint while the handler is built by the
// application, a package-level value is what lets them agree on one accumulator.
var LoopLag = NewLagHistogram()

// LagHistogram accumulates event-loop lag samples (in seconds) as a Prometheus
// histogram. It is safe for concurrent use: the probe observes from its own
// goroutine while a scrape reads it.
type LagHistogram struct {
	mu     sync.Mutex
	counts []uint64
	sum    float64
	count  uint64
}

// NewLagHistogram returns an empty histogram with one bucket per LoopLagBuckets
// boundary.
func NewLagHistogram() *LagHistogram {
	return &LagHistogram{counts: make([]uint64, len(LoopLagBuckets))}
}

// Observe records one lag sample. Bucket counts are cumulative, so every bucket
// whose bound the sample does not exceed is incremented.
func (h *LagHistogram) Observe(seconds float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += seconds
	for i, bound := range LoopLagBuckets {
		if seconds <= bound {
			h.counts[i]++
		}
	}
}

// Snapshot returns the accumulated samples as a renderable family.
func (h *LagHistogram) Snapshot() Histogram {
	h.mu.Lock()
	defer h.mu.Unlock()
	counts := make([]uint64, len(h.counts))
	copy(counts, h.counts)
	return Histogram{
		Name:    LoopLagMetricName,
		Help:    "Event-loop lag: extra time a ready coroutine waited before it ran",
		Buckets: LoopLagBuckets,
		Counts:  counts,
		Sum:     h.sum,
		Count:   h.count,
	}
}

// LoopLagProbe measures how late the scheduler runs a periodic tick. A process
// whose goroutine scheduler is blocked runs the tick late, which is the
// symptom a watchdog would otherwise only see as a failing liveness probe.
type LoopLagProbe struct {
	// Interval is how often the probe schedules a tick. A non-positive value
	// disables the probe.
	Interval time.Duration
	// Stall is the lag above which the tick is reported as a stall.
	Stall time.Duration
	// Logger receives the stall reports. A nil logger discards them.
	Logger *slog.Logger
	// Now is injectable so a test can drive the clock.
	Now func() time.Time
}

// Run samples until the context ends. Each tick compares the wall-clock time to
// the deadline it was scheduled for; the difference is the lag.
func (p *LoopLagProbe) Run(ctx context.Context) {
	if p.Interval <= 0 {
		return
	}
	// A watchdog that will report stalls publishes its counter up front, the
	// point at which the reference declares the stall instruments: a scrape
	// before the first stall then reads zero stalls rather than no series.
	if p.Stall > 0 {
		DeclareEventLoopStall()
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}
	deadline := now().Add(p.Interval)
	for {
		wait := time.Until(deadline)
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		lag := now().Sub(deadline)
		if lag < 0 {
			lag = 0
		}
		// Record every sample: the histogram is cheap to fill and the metrics
		// handler only ever renders it when the deployment opts in, so recording
		// here keeps the probe independent of the exposition's tuning.
		LoopLag.Observe(lag.Seconds())
		if p.Stall > 0 && lag >= p.Stall {
			// A tick the loop ran this late is a stall: the watchdog counts it
			// and records how long the loop was blocked, so an operator scrapes
			// the same signal the log line reports.
			Workload.ObserveLoopStall(lag.Seconds())
			if p.Logger != nil {
				p.Logger.Warn("event loop lag", "lag_ms", lag.Milliseconds())
			}
		}
		deadline = deadline.Add(p.Interval)
		if now().After(deadline) {
			// A tick so late that the next one is already overdue: resync
			// rather than firing a burst of catch-up ticks.
			deadline = now().Add(p.Interval)
		}
	}
}

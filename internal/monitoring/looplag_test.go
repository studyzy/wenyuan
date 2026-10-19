package monitoring

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The probe reports a stall and stops when the context ends.
func TestLoopLagProbeReportsAndStops(t *testing.T) {
	var out bytes.Buffer
	probe := &LoopLagProbe{
		Interval: time.Millisecond,
		Stall:    time.Nanosecond,
		Logger:   slog.New(slog.NewTextHandler(&out, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		probe.Run(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe must stop when the context ends")
	}
	if !strings.Contains(out.String(), "event loop lag") {
		t.Fatalf("expected a stall report, got %q", out.String())
	}
}

// A non-positive interval disables the probe.
func TestLoopLagProbeDisabled(t *testing.T) {
	probe := &LoopLagProbe{Interval: 0}
	done := make(chan struct{})
	go func() { defer close(done); probe.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a disabled probe must return immediately")
	}
}

// Starting the watchdog publishes the stall counter, so a scrape taken before
// the first stall reports zero stalls. The reference declares
// hindsight.event_loop.stalls as it builds its collector (metrics.py:623), so
// its exposition carries the zero-valued series on a healthy loop.
func TestLoopLagProbeDeclaresStallCounter(t *testing.T) {
	// The registry is process-wide and a sibling test records a real stall into
	// it, so this asserts the declaration path itself: a watchdog that will
	// report stalls publishes the counter even when no stall has happened.
	saved := Workload
	Workload = NewWorkloadRegistry()
	t.Cleanup(func() { Workload = saved })

	probe := &LoopLagProbe{Interval: time.Hour, Stall: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); probe.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !hasSample(Workload.Samples(), EventLoopStallsName) {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("the watchdog did not declare %s", EventLoopStallsName)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if value := findSample(t, Workload.Samples(), EventLoopStallsName).Value; value != 0 {
		t.Fatalf("declared stall counter = %v, want 0", value)
	}
	if len(Workload.Histograms()) != 0 {
		t.Fatalf("declaring the counter must not fabricate a duration observation: %v", Workload.Histograms())
	}
}

// hasSample reports whether a family already has a series.
func hasSample(samples []Metric, name string) bool {
	for _, sample := range samples {
		if sample.Name == name {
			return true
		}
	}
	return false
}

// The accumulator keeps cumulative bucket counts, a running sum and a count, and
// snapshots them as a renderable family.
func TestLagHistogramAccumulates(t *testing.T) {
	h := NewLagHistogram()
	h.Observe(0.0005) // <= 0.001
	h.Observe(0.003)  // <= 0.005
	h.Observe(9.0)    // only +Inf
	snap := h.Snapshot()
	if snap.Name != LoopLagMetricName || snap.Count != 3 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Counts[0] != 1 || snap.Counts[2] != 2 || snap.Counts[len(snap.Counts)-1] != 2 {
		t.Fatalf("cumulative counts = %v", snap.Counts)
	}
	if snap.Sum < 9.0034 || snap.Sum > 9.0036 {
		t.Fatalf("sum = %v", snap.Sum)
	}
}

package monitoring

import (
	"context"
	"log/slog"
	"time"
)

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
		if p.Stall > 0 && lag >= p.Stall && p.Logger != nil {
			p.Logger.Warn("event loop lag", "lag_ms", lag.Milliseconds())
		}
		deadline = deadline.Add(p.Interval)
		if now().After(deadline) {
			// A tick so late that the next one is already overdue: resync
			// rather than firing a burst of catch-up ticks.
			deadline = now().Add(p.Interval)
		}
	}
}

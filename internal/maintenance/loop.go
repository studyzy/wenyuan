// Package maintenance runs the recurring housekeeping a deployment needs, from
// one periodic loop.
//
// It exists because the work is not request-driven: rows age out, consolidation
// work gets stranded by a terminal failure, and a schedule comes due. A
// separate task per concern would be one goroutine each with its own cadence to
// reason about; one loop that wakes on a short tick and runs whichever job is
// due keeps the cadences in one place and keeps them from bursting together.
//
// The loop runs in every process with no leader election, which is the
// constraint the whole design serves. It is safe because every job's *work* is
// collision-proof — deletes claim their rows with SKIP LOCKED, and every enqueue
// is idempotent by subject — rather than because one process is chosen. The
// discovery in front of each job is not free, which is why every cadence is
// configuration: a job whose window is measured in days has no reason to probe
// every schema every minute.
package maintenance

import (
	"context"
	"log/slog"
	"math/rand"
	"time"
)

// DefaultTick is how often the loop looks for a job that is due.
//
// It is short so jobs with different cadences share one loop without a task
// each; the cadences themselves live on the job, not here.
const DefaultTick = 60 * time.Second

// Job is one recurring piece of housekeeping.
type Job struct {
	// Name labels the job in logs and in the last-run table.
	Name string
	// Interval is how long between runs. A non-positive interval disables the
	// job, which is how configuration turns one off.
	Interval time.Duration
	// Run performs the work. An error is logged and the loop continues: a job
	// that fails must not stop the others from running.
	Run func(ctx context.Context) error
}

// Loop runs the due jobs on a fixed tick.
type Loop struct {
	// Jobs are the recurring jobs, in the order they are attempted within a
	// tick.
	Jobs []Job
	// Tick is how often to look for due work. Zero uses DefaultTick.
	Tick time.Duration
	// StartJitter bounds a random delay before the first tick. Zero means no
	// delay.
	StartJitter time.Duration
	// Logger receives the operator-visible progress lines.
	Logger *slog.Logger
	// Now is injectable so cadence is testable without waiting.
	Now func() time.Time
	// Sleep, when set, replaces the loop's wait entirely, so a test drives the
	// loop without waiting in real time. Unset uses a context-aware timer.
	Sleep func(time.Duration)
	// Rand is injectable so the jitter is deterministic under test.
	Rand *rand.Rand

	// lastRun is the monotonic time of each job's last run. Monotonic rather
	// than wall clock, because a cadence must not be disturbed by the clock
	// being adjusted underneath it.
	lastRun map[string]time.Time
}

// New builds a loop over the jobs, applying defaults to any unset tuning.
func New(jobs []Job, logger *slog.Logger) *Loop {
	if logger == nil {
		logger = slog.Default()
	}
	return &Loop{
		Jobs:   jobs,
		Tick:   DefaultTick,
		Logger: logger,
		Now:    func() time.Time { return time.Now() },
		Rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Enabled reports whether any job could run, so a caller can skip starting the
// loop rather than waking it to do nothing.
func (l *Loop) Enabled() bool {
	for _, job := range l.Jobs {
		if job.Interval > 0 {
			return true
		}
	}
	return false
}

// Run drives the loop until the context ends.
//
// A job that fails is logged and the loop continues: a cross-schema probe
// failing for one reason must not stop retention from running, and the next tick
// is the retry.
func (l *Loop) Run(ctx context.Context) error {
	if !l.Enabled() {
		l.logger().Debug("maintenance loop not started: no jobs enabled")
		return nil
	}
	if !l.waitStartJitter(ctx) {
		return ctx.Err()
	}
	l.logger().Info("maintenance loop started", "jobs", len(l.Jobs), "tick", l.tick())
	for {
		l.RunDueJobs(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !l.sleep(ctx, l.tick()) {
			return ctx.Err()
		}
	}
}

// RunDueJobs runs every job that is due. It is exported so a test can drive
// one iteration without starting the loop.
func (l *Loop) RunDueJobs(ctx context.Context) {
	for _, job := range l.Jobs {
		if ctx.Err() != nil {
			return
		}
		if job.Interval <= 0 || !l.due(job.Name, job.Interval) {
			continue
		}
		l.runTimed(ctx, job)
	}
}

// runTimed runs one job and emits a uniform timing line for it.
//
// Each job keeps its own summary log of what it did; this adds the one line
// that makes the cost of every job observable regardless of what it found.
func (l *Loop) runTimed(ctx context.Context, job Job) {
	start := l.now()
	err := job.Run(ctx)
	elapsed := l.now().Sub(start)
	switch {
	case err != nil && ctx.Err() != nil:
		// A shutdown mid-job is not a failure of the job.
		l.logger().Debug("maintenance job interrupted by shutdown", "job", job.Name)
	case err != nil:
		l.logger().Warn("maintenance job failed", "job", job.Name, "error", err, "took", elapsed)
	default:
		l.logger().Info("maintenance job finished", "job", job.Name, "took", elapsed)
	}
}

// due reports whether a job should run now, recording the run.
//
// A job is due the first time it is asked about, which is what makes a restart
// run its housekeeping rather than waiting a full interval.
func (l *Loop) due(name string, interval time.Duration) bool {
	if l.lastRun == nil {
		l.lastRun = map[string]time.Time{}
	}
	now := l.now()
	if last, ok := l.lastRun[name]; ok && now.Sub(last) < interval {
		return false
	}
	l.lastRun[name] = now
	return true
}

// waitStartJitter delays the first tick by a random offset, returning false if
// the context ended while waiting.
//
// Every job is due on the first tick, so a fleet started together — a deploy, a
// rolling restart — would otherwise fire every cross-schema probe in every
// process at the same instant. Spreading the first tick is enough: from then on
// each process keeps its own phase.
func (l *Loop) waitStartJitter(ctx context.Context) bool {
	if l.StartJitter <= 0 {
		return true
	}
	delay := time.Duration(l.rand().Float64() * float64(l.StartJitter))
	l.logger().Debug("maintenance loop: delaying the first tick", "delay", delay)
	return l.sleep(ctx, delay)
}

// sleep waits for a duration, returning false if the context ended first.
//
// A non-nil Sleep replaces the wait entirely, so a test drives the loop without
// waiting in real time. With none set the wait is a context-aware timer rather
// than a plain sleep, so a shutdown interrupts it immediately: a loop that
// waited out a full tick before noticing cancellation would delay every
// shutdown by up to one tick.
func (l *Loop) sleep(ctx context.Context, d time.Duration) bool {
	if l.Sleep != nil {
		l.Sleep(d)
		return ctx.Err() == nil
	}
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) tick() time.Duration {
	if l.Tick > 0 {
		return l.Tick
	}
	return DefaultTick
}

func (l *Loop) logger() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

func (l *Loop) rand() *rand.Rand {
	if l.Rand != nil {
		return l.Rand
	}
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}

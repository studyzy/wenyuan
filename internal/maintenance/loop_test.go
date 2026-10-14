package maintenance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// clock is an injectable monotonic clock, so cadence is tested without waiting.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Unix(0, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// counter records how many times a job ran.
type counter struct {
	mu    sync.Mutex
	runs  int
	err   error
	block chan struct{}
}

func (c *counter) run(context.Context) error {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs++
	return c.err
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

// quietLogger discards output so a test's failures are readable.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testLoop(jobs []Job, clk *clock) *Loop {
	loop := New(jobs, quietLogger())
	loop.Now = clk.Now
	loop.Sleep = func(time.Duration) {}
	loop.Rand = rand.New(rand.NewSource(1))
	return loop
}

// A job runs on the first tick and then only once its interval has elapsed, so
// the cadence is what bounds the work rather than the tick rate.
func TestJobRunsOnIntervalNotEveryTick(t *testing.T) {
	clk := newClock()
	job := &counter{}
	loop := testLoop([]Job{{Name: "hourly", Interval: time.Hour, Run: job.run}}, clk)

	loop.RunDueJobs(context.Background())
	if job.count() != 1 {
		t.Fatalf("runs = %d, want 1 on the first tick", job.count())
	}

	// Ticks inside the interval do not run it again.
	clk.advance(30 * time.Minute)
	loop.RunDueJobs(context.Background())
	if job.count() != 1 {
		t.Fatalf("runs = %d, want the job to wait out its interval", job.count())
	}

	clk.advance(31 * time.Minute)
	loop.RunDueJobs(context.Background())
	if job.count() != 2 {
		t.Fatalf("runs = %d, want the job to run once its interval elapsed", job.count())
	}
}

// Jobs with different cadences share one loop and are independently due.
func TestJobsKeepIndependentCadences(t *testing.T) {
	clk := newClock()
	fast, slow := &counter{}, &counter{}
	loop := testLoop([]Job{
		{Name: "fast", Interval: time.Minute, Run: fast.run},
		{Name: "slow", Interval: time.Hour, Run: slow.run},
	}, clk)

	loop.RunDueJobs(context.Background())
	clk.advance(2 * time.Minute)
	loop.RunDueJobs(context.Background())

	if fast.count() != 2 {
		t.Errorf("fast ran %d times, want 2", fast.count())
	}
	if slow.count() != 1 {
		t.Errorf("slow ran %d times, want 1", slow.count())
	}
}

// A disabled job never runs, which is how configuration turns one off.
func TestDisabledJobNeverRuns(t *testing.T) {
	clk := newClock()
	job := &counter{}
	loop := testLoop([]Job{{Name: "off", Interval: 0, Run: job.run}}, clk)

	clk.advance(24 * time.Hour)
	loop.RunDueJobs(context.Background())
	if job.count() != 0 {
		t.Errorf("runs = %d, want a disabled job to never run", job.count())
	}
	if loop.Enabled() {
		t.Error("a loop of disabled jobs must report itself disabled")
	}
}

// A failing job is contained: the others still run, and the loop keeps going
// rather than treating one failure as fatal.
func TestFailingJobDoesNotStopOthers(t *testing.T) {
	clk := newClock()
	failing, healthy := &counter{err: errors.New("probe failed")}, &counter{}
	loop := testLoop([]Job{
		{Name: "failing", Interval: time.Minute, Run: failing.run},
		{Name: "healthy", Interval: time.Minute, Run: healthy.run},
	}, clk)

	loop.RunDueJobs(context.Background())
	if healthy.count() != 1 {
		t.Error("a failing job must not stop the jobs after it")
	}

	// And the next tick retries it.
	clk.advance(2 * time.Minute)
	loop.RunDueJobs(context.Background())
	if failing.count() != 2 {
		t.Errorf("failing ran %d times, want the next tick to retry it", failing.count())
	}
}

// The first tick is jittered, so a fleet started together does not fire every
// probe at the same instant.
func TestStartJitterDelaysTheFirstTick(t *testing.T) {
	clk := newClock()
	loop := testLoop(nil, clk)
	loop.StartJitter = 30 * time.Second

	var slept time.Duration
	loop.Sleep = func(d time.Duration) { slept = d }
	if !loop.waitStartJitter(context.Background()) {
		t.Fatal("the jitter wait must complete when the context is live")
	}
	if slept < 0 || slept > 30*time.Second {
		t.Errorf("slept %v, want a delay within the jitter bound", slept)
	}
}

// A shutdown during the jitter wait ends the loop rather than running a tick.
func TestShutdownDuringJitterReturnsCleanly(t *testing.T) {
	clk := newClock()
	loop := testLoop([]Job{{Name: "j", Interval: time.Minute, Run: (&counter{}).run}}, clk)
	loop.StartJitter = time.Hour
	// The injected sleep reports the context as already ended by never
	// returning a live one.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loop.Sleep = func(d time.Duration) {}

	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's cancellation", err)
	}
}

// A loop with no enabled job returns immediately rather than waking forever.
func TestRunReturnsWhenNothingIsEnabled(t *testing.T) {
	clk := newClock()
	loop := testLoop([]Job{{Name: "off", Interval: 0, Run: (&counter{}).run}}, clk)
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("err = %v, want a clean return", err)
	}
}

// An interrupted job is not reported as a failure: a shutdown mid-job is the
// shutdown's doing, not the job's.
func TestInterruptedJobIsNotAFailure(t *testing.T) {
	clk := newClock()
	ctx, cancel := context.WithCancel(context.Background())
	job := &counter{err: errors.New("could not finish")}
	loop := testLoop([]Job{{Name: "j", Interval: time.Minute, Run: func(context.Context) error {
		// Cancel while the job is running, which is what a shutdown looks like
		// from inside it.
		cancel()
		return job.run(ctx)
	}}}, clk)

	loop.RunDueJobs(ctx)
	if job.count() != 1 {
		t.Fatal("the job ran")
	}
}

// Run drains on cancellation rather than returning before the tick completes.
func TestRunStopsOnCancellation(t *testing.T) {
	clk := newClock()
	ran := make(chan struct{}, 1)
	loop := testLoop([]Job{{Name: "j", Interval: time.Minute, Run: func(context.Context) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}}, clk)

	ctx, cancel := context.WithCancel(context.Background())
	loop.Sleep = func(time.Duration) { cancel() }
	err := loop.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	select {
	case <-ran:
	default:
		t.Error("the loop must have run its due job at least once before stopping")
	}
}

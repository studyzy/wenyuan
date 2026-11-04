package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// WallCeiling is how one task type's wall-clock ceiling is configured, the
// reference's `_WallCeiling` (worker/poller.py:76-96).
//
// ExtendsOnProgress picks the semantics:
//
//   - false (retain) — an absolute ceiling on total runtime. A retain is one
//     document; if it is still going an hour later, something is wrong.
//   - true (consolidation) — an *idle* ceiling. A consolidation job is a loop
//     over batches, each committing its own memories, and a big backlog is
//     legitimately long. Every committed batch reports progress, which restarts
//     the clock, so the ceiling only fires on a job that has stopped making
//     progress — a stall, not a slow bank.
type WallCeiling struct {
	// EnvVar names the knob an operator turns, so the failure message can never
	// drift from the value the ceiling was resolved from.
	EnvVar string
	// ExtendsOnProgress marks an idle ceiling.
	ExtendsOnProgress bool
}

// wallCeilings is the reference's `_WALL_CEILINGS` (poller.py:96-118) keyed by the
// DB operation_type, which is the key the reference uses too — the task dict's
// "type" is the operation type written at submit, and the poller looks the ceiling
// up by it.
//
// The reference spells its three retain variants separately because its claim hands
// them back as distinct types; here a batch parent is never claimed and a
// file-convert retain runs the plain retain handler, so one "retain" entry bounds
// every claimable retain-shaped operation identically — which is what the
// reference's `dict.fromkeys(_RETAIN_OP_TYPES, ...)` produces anyway.
var wallCeilings = map[string]WallCeiling{
	string(storage.OperationTypeRetain):             {EnvVar: "WENYUAN_API_RETAIN_WALL_TIMEOUT"},
	string(storage.OperationTypeConsolidate):        {EnvVar: "WENYUAN_API_CONSOLIDATION_WALL_TIMEOUT", ExtendsOnProgress: true},
	string(storage.OperationTypeRefreshMentalModel): {EnvVar: "WENYUAN_API_REFLECT_WALL_TIMEOUT"},
}

// WallCeilingOf returns how one operation type's ceiling is enforced, or the zero
// value for a type that has none. An unmapped type stays unbounded until it gets an
// explicit ceiling, exactly as the reference leaves one.
func WallCeilingOf(operationType string) WallCeiling {
	return wallCeilings[operationType]
}

// WallTimeoutExceeded is a task cancelled because it blew through its wall-clock
// ceiling. It is the reference's `_WallTimeoutExceeded` (poller.py:174-195): a
// terminal failure with an operator-facing explanation, not an ordinary retryable
// error, and it spends no attempt.
type WallTimeoutExceeded struct {
	// Timeout is the ceiling that fired.
	Timeout time.Duration
	// Ceiling is the configuration the ceiling was resolved from.
	Ceiling WallCeiling
	// TaskType is the operation type the ceiling applies to.
	TaskType string
	// Stage is what the task was doing when the ceiling fired.
	Stage string
}

func (e *WallTimeoutExceeded) Error() string { return e.Describe() }

// Describe renders what fired, where, and which knob moves it — the reference's
// `_WallTimeoutExceeded.describe`.
func (e *WallTimeoutExceeded) Describe() string {
	seconds := int64(e.Timeout / time.Second)
	stage := e.Stage
	if stage == "" {
		stage = "unknown"
	}
	var what string
	if e.Ceiling.ExtendsOnProgress {
		what = fmt.Sprintf("Task made no progress for %ds, its wall-clock limit for '%s' "+
			"(each committed batch restarts the clock, so this is a stall, not a slow job)", seconds, e.TaskType)
	} else {
		what = fmt.Sprintf("Task exceeded the %ds wall-clock limit for '%s'", seconds, e.TaskType)
	}
	return fmt.Sprintf("%s (stage=%s) and was cancelled. Raise %s if this is a legitimately long operation, "+
		"or set it to 0 to disable the limit.", what, stage, e.Ceiling.EnvVar)
}

// progressKey carries the running task's ceiling so ReportProgress can reach it. It
// is unexported so only this package installs or reads it.
type progressKey struct{}

// progressState is what a running ceiling-bounded task shares with ReportProgress.
type progressState struct {
	ceiling *activeCeiling

	mu   sync.Mutex
	last string
}

// ReportProgress tells the running operation's wall-clock ceiling that the work is
// still advancing, optionally naming the stage. It is the reference's
// `stage.set_stage()` (worker/stage.py:63-75): a no-op outside a ceiling-bounded
// task, so a processor run directly in a test never needs a ceiling.
//
// Only an idle ceiling is affected: an absolute ceiling ignores progress by
// construction, which is the reference's `extends_on_progress` split.
func ReportProgress(ctx context.Context, stage string) {
	state, _ := ctx.Value(progressKey{}).(*progressState)
	if state == nil {
		return
	}
	if stage != "" {
		state.mu.Lock()
		state.last = stage
		state.mu.Unlock()
	}
	state.ceiling.reschedule()
}

// stage names what the task was doing when the ceiling fired: the last stage the
// processor named, or "unknown" when it named none — the same fallback the
// reference uses when no holder is bound.
func (s *progressState) stage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == "" {
		return "unknown"
	}
	return s.last
}

// activeCeiling is one running ceiling: the reference's `asyncio.timeout(...)`
// block with `reschedule()`.
type activeCeiling struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timeout time.Duration
	extend  bool
	now     func() time.Time

	// extended wakes the watcher when an idle ceiling's deadline moves.
	extended chan struct{}

	mu       sync.Mutex
	deadline time.Time
	expired  bool
}

// newActiveCeiling starts a ceiling of timeout over parent's values.
func newActiveCeiling(parent context.Context, timeout time.Duration, ceiling WallCeiling, now func() time.Time) *activeCeiling {
	a := &activeCeiling{
		timeout:  timeout,
		extend:   ceiling.ExtendsOnProgress,
		now:      now,
		extended: make(chan struct{}, 1),
		deadline: now().Add(timeout),
	}
	a.ctx, a.cancel = context.WithCancelCause(parent)
	return a
}

// stop releases the ceiling. It must be called once the work has returned.
func (a *activeCeiling) stop() {
	a.mu.Lock()
	a.expired = true
	a.mu.Unlock()
	a.cancel(nil)
}

// reschedule pushes the deadline out a full ceiling from now — the reference's
// `cm.reschedule(loop.time() + wall_timeout)`. A late report from an unwinding task
// must never raise, so an expired ceiling ignores it, and an absolute ceiling
// ignores it by construction.
func (a *activeCeiling) reschedule() {
	if !a.extend {
		return
	}
	a.mu.Lock()
	if a.expired {
		a.mu.Unlock()
		return
	}
	a.deadline = a.now().Add(a.timeout)
	a.mu.Unlock()
	// Buffered to one: a burst of reports only needs to wake the watcher once, and
	// it must never block the engine's hot path.
	select {
	case a.extended <- struct{}{}:
	default:
	}
}

// remaining reports how long is left before the current deadline.
func (a *activeCeiling) remaining() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deadline.Sub(a.now())
}

// watch cancels the ceiling once its deadline passes, re-arming whenever an idle
// ceiling is extended. The returned channel stops the watcher.
func (a *activeCeiling) watch(exceeded *WallTimeoutExceeded, stage func() string) chan struct{} {
	stop := make(chan struct{})
	go func() {
		timer := time.NewTimer(a.remaining())
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-a.ctx.Done():
				return
			case <-a.extended:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(a.remaining())
			case <-timer.C:
				a.expire(exceeded, stage)
				return
			}
		}
	}()
	return stop
}

// expire marks the ceiling spent and cancels the work under it.
func (a *activeCeiling) expire(exceeded *WallTimeoutExceeded, stage func() string) {
	a.mu.Lock()
	if a.expired {
		a.mu.Unlock()
		return
	}
	a.expired = true
	exceeded.Stage = stage()
	a.mu.Unlock()
	a.cancel(exceeded)
}

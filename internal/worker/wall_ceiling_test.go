package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference's ceiling table (worker/poller.py:96-118), by operation type.
// This pins it so a key added here without the reference's counterpart — or a
// retain variant that lost its ceiling — fails the build's tests rather than
// silently running unbounded.
func TestWallCeilingTableMatchesTheReference(t *testing.T) {
	cases := []struct {
		opType       string
		wantEnv      string
		wantProgress bool
	}{
		// dict.fromkeys(_RETAIN_OP_TYPES, _WallCeiling(retain_wall_timeout)) — the
		// three retain-shaped types share one absolute ceiling.
		{storage.OperationTypeRetain, "WENYUAN_API_RETAIN_WALL_TIMEOUT", false},
		// _WallCeiling(consolidation_wall_timeout, extends_on_progress=True).
		{storage.OperationTypeConsolidate, "WENYUAN_API_CONSOLIDATION_WALL_TIMEOUT", true},
		// _WallCeiling(reflect_wall_timeout) — a refresh borrows reflect's budget.
		{storage.OperationTypeRefreshMentalModel, "WENYUAN_API_REFLECT_WALL_TIMEOUT", false},
	}
	for _, tc := range cases {
		got := WallCeilingOf(tc.opType)
		if got.EnvVar != tc.wantEnv {
			t.Errorf("%s ceiling env = %q, want %q", tc.opType, got.EnvVar, tc.wantEnv)
		}
		if got.ExtendsOnProgress != tc.wantProgress {
			t.Errorf("%s extends_on_progress = %v, want %v", tc.opType, got.ExtendsOnProgress, tc.wantProgress)
		}
	}
	// An unmapped type has no ceiling at all — the reference leaves one unbounded
	// until it gets an explicit ceiling (`_wall_timeout_for` returns None).
	for _, unmapped := range []string{
		storage.OperationTypeWebhookDelivery,
		storage.OperationTypeTransferImport,
		storage.OperationTypeClone,
		"A_grand_unheard_type",
	} {
		if got := WallCeilingOf(unmapped); got.EnvVar != "" {
			t.Errorf("%s must have no ceiling, got %+v", unmapped, got)
		}
	}
}

// The failure text is the reference's `_WallTimeoutExceeded.describe`
// (poller.py:182-195), verbatim, for both semantics.
func TestWallTimeoutDescriptionMatchesTheReference(t *testing.T) {
	absolute := &WallTimeoutExceeded{
		Timeout: 3600 * time.Second, Ceiling: WallCeilingOf(storage.OperationTypeRetain),
		TaskType: "retain", Stage: "retain.facts.llm",
	}
	wantAbsolute := "Task exceeded the 3600s wall-clock limit for 'retain' (stage=retain.facts.llm) " +
		"and was cancelled. Raise WENYUAN_API_RETAIN_WALL_TIMEOUT if this is a legitimately long " +
		"operation, or set it to 0 to disable the limit."
	if got := absolute.Describe(); got != wantAbsolute {
		t.Errorf("absolute ceiling text =\n  %q\nwant\n  %q", got, wantAbsolute)
	}

	idle := &WallTimeoutExceeded{
		Timeout: 7200 * time.Second, Ceiling: WallCeilingOf(storage.OperationTypeConsolidate),
		TaskType: "consolidation", Stage: "consolidation.batch",
	}
	wantIdle := "Task made no progress for 7200s, its wall-clock limit for 'consolidation' " +
		"(each committed batch restarts the clock, so this is a stall, not a slow job) " +
		"(stage=consolidation.batch) and was cancelled. Raise WENYUAN_API_CONSOLIDATION_WALL_TIMEOUT " +
		"if this is a legitimately long operation, or set it to 0 to disable the limit."
	if got := idle.Describe(); got != wantIdle {
		t.Errorf("idle ceiling text =\n  %q\nwant\n  %q", got, wantIdle)
	}
	// A task that never named a stage still says "unknown", not a blank.
	blank := &WallTimeoutExceeded{
		Timeout: time.Second, Ceiling: WallCeilingOf(storage.OperationTypeRetain), TaskType: "retain",
	}
	if !strings.Contains(blank.Describe(), "(stage=unknown)") {
		t.Errorf("unnamed stage must read 'unknown': %q", blank.Describe())
	}
}

// reportingProcessor reports progress in a loop until stopped, so an idle ceiling
// has something to extend on. It stops itself after `for` elapses rather than
// waiting for a cancellation, because a Stop() only ends the claim loop — the
// work context is deliberately detached from it so a shutdown drains the task.
type reportingProcessor struct {
	period time.Duration
	runFor time.Duration
}

func (p reportingProcessor) Process(ctx context.Context, _ storage.Operation) error {
	stop := time.Now().Add(p.runFor)
	next := time.Now().Add(p.period)
	for time.Now().Before(stop) {
		if !time.Now().Before(next) {
			ReportProgress(ctx, "consolidation.batch")
			next = next.Add(p.period)
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// An idle ceiling (consolidation) is restarted by progress, so a job that keeps
// committing batches is never killed — it only fires on a stall. This is the
// reference's `extends_on_progress` (poller.py:1225-1240): every committed batch
// calls set_stage, which reschedules the deadline a full ceiling out.
//
// Falsification: drop the reschedule from `activeCeiling.watch` and this FAILs —
// the 60ms ceiling fires while the task is still advancing.
func TestIdleCeilingIsRestartedByProgress(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeConsolidate)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, reportingProcessor{period: 10 * time.Millisecond, runFor: 300 * time.Millisecond}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now:          func() time.Time { return time.Now().UTC() },
		Sleep:        func(time.Duration) {},
		WallTimeouts: map[string]time.Duration{storage.OperationTypeConsolidate: 60 * time.Millisecond},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusCompleted {
		t.Fatalf("an advancing consolidation must not be killed by its idle ceiling: %+v", after)
	}
}

// The same loop under an ABSOLUTE ceiling (retain) is killed, because retain does
// not extend on progress: it bounds total runtime by construction.
func TestAbsoluteCeilingIgnoresProgress(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeRetain)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, reportingProcessor{period: 5 * time.Millisecond, runFor: 400 * time.Millisecond}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now:          func() time.Time { return time.Now().UTC() },
		Sleep:        func(time.Duration) {},
		WallTimeouts: map[string]time.Duration{storage.OperationTypeRetain: 60 * time.Millisecond},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusFailed || !strings.Contains(after.Error, "wall-clock limit for 'retain'") {
		t.Fatalf("an absolute ceiling must fire regardless of progress: %+v", after)
	}
}

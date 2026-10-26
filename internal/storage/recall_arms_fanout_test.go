package storage

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// armIDs turns a fan-out's outcomes into the identifiers of the hits they
// carry, so a test can read the merge order the scheduler guarantees.
func armIDs(outcomes []recallArmOutcome) []string {
	ids := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		for _, hit := range outcome.hits {
			ids = append(ids, hit.match.ID)
		}
	}
	return ids
}

// stubArm returns an arm job whose permit-guarded work is body.
func stubArm(name string, body func(ctx context.Context) ([]recallHit, error)) recallArmJob {
	return recallArmJob{name: name, run: body}
}

// budgetTuning returns the reference defaults with the supplied per-recall
// connection budget, so a store-level test can drive the fan-out's bound.
func budgetTuning(budget int) Tuning {
	tune := DefaultTuning()
	tune.ConnectionBudget = budget
	return tune
}

// Two independent arms run at the same time when the budget allows it: each
// waits for the other to have started, so a serialized fan-out could never
// complete. The outcomes come back in job order regardless of which arm
// finished first.
func TestRecallArmFanOutOverlapsArms(t *testing.T) {
	fan := newRecallArmFanOut(2)
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	slow := func(started chan struct{}, other chan struct{}, id string) func(context.Context) ([]recallHit, error) {
		return func(ctx context.Context) ([]recallHit, error) {
			close(started)
			select {
			case <-other:
			case <-time.After(5 * time.Second):
				t.Errorf("arm %s never saw its sibling start, so the arms did not overlap", id)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []recallHit{{match: RecallMatch{ID: id}}}, nil
		}
	}
	jobs := []recallArmJob{
		stubArm("alpha", slow(firstStarted, secondStarted, "a")),
		stubArm("beta", slow(secondStarted, firstStarted, "b")),
	}
	outcomes, err := runRecallArms(context.Background(), fan, nil, jobs)
	if err != nil {
		t.Fatalf("a fan-out whose arms overlapped must not fail: %v", err)
	}
	if got, want := armIDs(outcomes), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes must be returned in job order: got %v, want %v", got, want)
	}
}

// The budget caps how many arms are in flight at once: with a budget of one the
// arms serialize, with a larger budget they overlap up to that bound. Each arm
// holds its permit for a window far longer than the time it takes to start one,
// so the arms that may run together do, and the gauge observes it.
func TestRecallArmFanOutBudgetCapsOverlap(t *testing.T) {
	const arms = 4
	tests := []struct {
		name   string
		budget int
	}{
		{name: "a budget of one serializes the arms", budget: 1},
		{name: "a budget of two counts two at a time", budget: 2},
		{name: "a budget of four runs every arm at once", budget: arms},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fan := newRecallArmFanOut(test.budget)
			var inFlight, peak atomic.Int64
			jobs := make([]recallArmJob, 0, arms)
			for range arms {
				jobs = append(jobs, stubArm("arm", func(context.Context) ([]recallHit, error) {
					running := inFlight.Add(1)
					for {
						seen := peak.Load()
						if running <= seen || peak.CompareAndSwap(seen, running) {
							break
						}
					}
					time.Sleep(20 * time.Millisecond)
					inFlight.Add(-1)
					return nil, nil
				}))
			}
			if _, err := runRecallArms(context.Background(), fan, nil, jobs); err != nil {
				t.Fatalf("the fan-out failed: %v", err)
			}
			if observed := peak.Load(); observed != int64(test.budget) {
				t.Fatalf("a budget of %d must let exactly %d arms run at once, observed %d",
					test.budget, test.budget, observed)
			}
		})
	}
}

// An arm that failed fails the recall, and it is the first failure in job order
// that is reported: which arm lost the race must not decide the error a caller
// sees.
func TestRecallArmFanOutReportsArmError(t *testing.T) {
	fan := newRecallArmFanOut(3)
	sentinel := errors.New("arm failed")
	ran := make([]bool, 3)
	jobs := []recallArmJob{
		stubArm("first", func(context.Context) ([]recallHit, error) {
			ran[0] = true
			return []recallHit{{match: RecallMatch{ID: "first"}}}, nil
		}),
		stubArm("second", func(context.Context) ([]recallHit, error) {
			ran[1] = true
			return nil, sentinel
		}),
		stubArm("third", func(context.Context) ([]recallHit, error) {
			ran[2] = true
			return nil, errors.New("a later failure")
		}),
	}
	outcomes, err := runRecallArms(context.Background(), fan, nil, jobs)
	if !errors.Is(err, sentinel) {
		t.Fatalf("the first failure in job order must be the recall's error: got %v", err)
	}
	// Every arm is joined before the fan-out returns, so no sibling is still
	// reading the store once the recall has failed.
	for index, ok := range ran {
		if !ok {
			t.Fatalf("arm %d must be joined before the fan-out returns", index)
		}
	}
	if got := outcomes[0].hits; len(got) != 1 || got[0].match.ID != "first" {
		t.Fatalf("a sibling's hits must still be delivered: got %v", got)
	}
}

// A recall whose context ends while an arm waits for a permit reports the
// context's error rather than waiting forever or running without a permit.
func TestRecallArmFanOutRespectsContextWhileWaitingForAPermit(t *testing.T) {
	fan := newRecallArmFanOut(1)
	// The only permit is taken before the arms are started, so every one of
	// them has to wait for it.
	fan.permits <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := []recallArmJob{
		stubArm("alpha", func(context.Context) ([]recallHit, error) {
			t.Error("an arm must not run without a permit")
			return nil, nil
		}),
		stubArm("beta", func(context.Context) ([]recallHit, error) {
			t.Error("an arm must not run without a permit")
			return nil, nil
		}),
	}
	_, err := runRecallArms(ctx, fan, nil, jobs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait for a permit must report the context's error: got %v", err)
	}
}

// An unset budget is the reference's default rather than unbounded, and a set
// budget is what bounds the fan-out.
func TestRecallArmFanOutBudgetResolution(t *testing.T) {
	tests := []struct {
		name   string
		budget int
		want   int
	}{
		{name: "unset takes the reference default", budget: 0, want: DefaultRecallConnectionBudget},
		{name: "a negative value takes the reference default", budget: -1, want: DefaultRecallConnectionBudget},
		{name: "a configured budget is used as it is", budget: 7, want: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cap(newRecallArmFanOut(test.budget).permits); got != test.want {
				t.Fatalf("budget %d must resolve to %d permits, got %d", test.budget, test.want, got)
			}
		})
	}
}

// fanOutFixtureStore builds an in-process store holding a bank the whole arm
// set answers: shared words for the dense and lexical arms, entities for the
// graph and entity arms, and mention times for the temporal arm.
func fanOutFixtureStore(t *testing.T, budget int) *MemoryStore {
	t.Helper()
	store := NewMemoryStore("public")
	store.SetTuning(budgetTuning(budget))
	mentioned := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	seedBank(t, store, "b1",
		RecallMatch{ID: "alpha-beta", Text: "alpha beta deploy", Type: "world", Entities: []string{"alpha", "beta"}, MentionedAt: &mentioned},
		RecallMatch{ID: "alpha-only", Text: "alpha rollback", Type: "world", Entities: []string{"alpha"}, MentionedAt: &earlier},
		RecallMatch{ID: "beta-only", Text: "beta pipeline", Type: "observation", Entities: []string{"beta"}, MentionedAt: &earlier},
		RecallMatch{ID: "dated", Text: "unrelated wording", Type: "world", MentionedAt: &mentioned},
	)
	return store
}

// fanOutQueries are the queries the equality check runs: one that exercises
// every arm, one that names no word (the temporal and entity arms only), and
// one that matches nothing. Each pins the recency anchor, because an unpinned
// query grades recency against the wall clock and two recalls would then differ
// in their last digits for reasons that have nothing to do with the fan-out.
func fanOutQueries() []RecallQuery {
	anchor := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	return []RecallQuery{
		{BankID: "b1", Query: "alpha beta", QueryTimestamp: &anchor},
		{BankID: "b1", Query: "", QueryTimestamp: &anchor},
		{BankID: "b1", Query: "zzz", QueryTimestamp: &anchor},
	}
}

// The arms run concurrently when the budget allows it and one at a time when it
// does not, and both orders produce the same recall: the same matches in the
// same order, the same recorded ranks and the same trace.
//
// Run with the race detector, this is also the check that the concurrent
// fan-out does not race the diagnostic accounting: every recall here is
// measured, because the default tuning samples every one.
func TestRecallArmsConcurrentAndSerializedAgree(t *testing.T) {
	serial := fanOutFixtureStore(t, 1)
	concurrent := fanOutFixtureStore(t, len(RecallArms()))
	for _, query := range fanOutQueries() {
		serialResult, err := serial.Recall(context.Background(), query)
		if err != nil {
			t.Fatalf("serialized recall failed: %v", err)
		}
		concurrentResult, err := concurrent.Recall(context.Background(), query)
		if err != nil {
			t.Fatalf("concurrent recall failed: %v", err)
		}
		if len(serialResult.Matches) == 0 {
			t.Fatalf("the fixture must surface something for %q, or the comparison is vacuous", query.Query)
		}
		if !reflect.DeepEqual(recallIDs(serialResult), recallIDs(concurrentResult)) {
			t.Fatalf("query %q: matches must not depend on the arm concurrency: serial %v, concurrent %v",
				query.Query, recallIDs(serialResult), recallIDs(concurrentResult))
		}
		for index := range serialResult.Matches {
			serialMatch := serialResult.Matches[index]
			concurrentMatch := concurrentResult.Matches[index]
			if !reflect.DeepEqual(serialMatch.Scores, concurrentMatch.Scores) || serialMatch.Final != concurrentMatch.Final {
				t.Fatalf("query %q: match %s must score identically: serial %v/%v, concurrent %v/%v",
					query.Query, serialMatch.ID, serialMatch.Scores, serialMatch.Final,
					concurrentMatch.Scores, concurrentMatch.Final)
			}
		}
		// The trace's phase list is a wall-clock measurement, so it differs run to
		// run by construction; the deterministic half is the candidate payload.
		serialTrace, concurrentTrace := serialResult.Trace, concurrentResult.Trace
		serialTrace.Detail, concurrentTrace.Detail = copyTraceDetail(serialTrace.Detail), copyTraceDetail(concurrentTrace.Detail)
		if !reflect.DeepEqual(serialTrace, concurrentTrace) || !reflect.DeepEqual(serialTrace.Detail, concurrentTrace.Detail) {
			t.Fatalf("query %q: the trace (candidate counts and recorded ranks) must not depend on the arm concurrency:\nserial %+v\nconcurrent %+v",
				query.Query, serialResult.Trace, concurrentResult.Trace)
		}
	}
}

// copyTraceDetail drops the wall-clock measurements (each phase's seconds and
// the search's total duration) so two traces can be compared on the payload
// they actually describe.
func copyTraceDetail(detail *RecallTraceDetail) *RecallTraceDetail {
	if detail == nil {
		return nil
	}
	clone := *detail
	clone.PhaseMetrics = nil
	clone.TotalDurationSeconds = 0
	clone.ArmDurations = nil
	return &clone
}

// A concurrent fan-out still measures every arm's phase exactly once, so the
// diagnostic accounting of a recall is the same whether its arms overlapped or
// not.
func TestRecallArmsConcurrentAccountingCoversEveryArm(t *testing.T) {
	observer := &recordingPhaseObserver{}
	store := fanOutFixtureStore(t, len(RecallArms()))
	store.SetRecallPhaseObserver(observer)
	if _, err := store.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha beta"}); err != nil {
		t.Fatal(err)
	}
	counts := observer.phaseNames()
	for _, arm := range RecallArms() {
		if counts[armPhaseName(arm)] != 1 {
			t.Fatalf("arm %s must report its phase exactly once under a concurrent fan-out: %v", arm, counts)
		}
	}
}

// A recall whose query names no words carries no vector and matches nothing
// still answers from the list arm, which the in-process store runs as one of
// the fan-out's arms.
func TestRecallArmsEmptyQueryStillRecalls(t *testing.T) {
	store := fanOutFixtureStore(t, 1)
	result, err := store.Recall(context.Background(), RecallQuery{BankID: "b1", Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("a query with no words must still recall through the temporal and entity arms")
	}
}

package storage

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// recordingPhaseObserver collects the observations a store reports, which is
// the seam a deployment's metrics registry plugs into.
type recordingPhaseObserver struct {
	mu   sync.Mutex
	seen []RecallPhaseObservation
}

func (r *recordingPhaseObserver) ObserveRecallPhase(o RecallPhaseObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, o)
}

func (r *recordingPhaseObserver) snapshot() []RecallPhaseObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecallPhaseObservation, len(r.seen))
	copy(out, r.seen)
	return out
}

// phaseNames counts how many observations each phase name drew.
func (r *recordingPhaseObserver) phaseNames() map[string]int {
	counts := map[string]int{}
	for _, observation := range r.snapshot() {
		counts[observation.Phase]++
	}
	return counts
}

// captureLogs points the default logger at a buffer for the duration of the
// test, so the accounting's own line can be asserted. No test in this package
// runs in parallel, so the default logger is the only writer.
func capturePhaseLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

// phaseRecallStore builds a bank holding one fact and installs the observer.
func phaseRecallStore(t *testing.T, observer RecallPhaseObserver) *MemoryStore {
	t.Helper()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Items: []RetainItem{
		{Content: "a fact about the topic", DocumentID: "doc-1"},
	}}); err != nil {
		t.Fatal(err)
	}
	store.SetRecallPhaseObserver(observer)
	return store
}

func runPhaseRecall(t *testing.T, store *MemoryStore) {
	t.Helper()
	if _, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "topic"}); err != nil {
		t.Fatal(err)
	}
}

// With the diagnostic accounting on — the reference's shipped default — one
// recall reports every phase it measured exactly once, and marks the phases
// that are subsets of another as diagnostic so a consumer summing phases into a
// request total does not double-count them.
func TestRecallPhaseAccountingReportsEveryPhase(t *testing.T) {
	observer := &recordingPhaseObserver{}
	store := phaseRecallStore(t, observer)
	runPhaseRecall(t, store)

	observations := observer.snapshot()
	if len(observations) == 0 {
		t.Fatal("the default accounting reported no phases")
	}
	counts := map[string]int{}
	diagnostics := 0
	for _, observation := range observations {
		counts[observation.Phase]++
		if observation.BankID != "b1" {
			t.Fatalf("phase %q reported bank %q, want b1", observation.Phase, observation.BankID)
		}
		if observation.Seconds < 0 {
			t.Fatalf("phase %q reported %v seconds", observation.Phase, observation.Seconds)
		}
		if observation.Diagnostic {
			diagnostics++
		}
	}
	for _, name := range []string{"bank_lookup", "parallel_retrieval", "score", "hydrate_results", "side_channels", "token_filter", "trace"} {
		if counts[name] != 1 {
			t.Fatalf("phase %q was reported %d times, want once (all: %v)", name, counts[name], counts)
		}
	}
	// The per-arm timings are subsets of the fan-out, not siblings of it.
	if diagnostics == 0 {
		t.Fatalf("no phase was marked diagnostic: %v", counts)
	}
	if counts[armPhaseName(ArmSemantic)] != 1 {
		t.Fatalf("the semantic arm's own phase was not reported once: %v", counts)
	}
}

// The accounting writes the reference's line: the sibling phases summed against
// the recall's total, and the subsets on their own line so the two cannot be
// added together.
func TestRecallPhaseAccountingLogsTheLine(t *testing.T) {
	logs := capturePhaseLogs(t)
	store := phaseRecallStore(t, nil)
	runPhaseRecall(t, store)

	output := logs.String()
	if !strings.Contains(output, "[phases]") || !strings.Contains(output, "accounted=") {
		t.Fatalf("no accounting line was logged: %q", output)
	}
	if !strings.Contains(output, "parallel_retrieval=") {
		t.Fatalf("the accounting line names no phase: %q", output)
	}
	if !strings.Contains(output, "[phases:subsets]") {
		t.Fatalf("the subset phases were not reported on their own line: %q", output)
	}
}

// A deployment that turns the accounting off pays nothing and reports nothing.
func TestRecallPhaseAccountingOffRecordsNothing(t *testing.T) {
	logs := capturePhaseLogs(t)
	observer := &recordingPhaseObserver{}
	store := phaseRecallStore(t, observer)
	tuneStore(t, store, func(tune *Tuning) { tune.DiagnosticPhases = false })
	runPhaseRecall(t, store)

	if seen := observer.snapshot(); len(seen) != 0 {
		t.Fatalf("the accounting reported %d phases while off: %v", len(seen), seen)
	}
	if strings.Contains(logs.String(), "[phases") {
		t.Fatalf("the accounting logged while off: %q", logs.String())
	}
	// The recorder itself is nil, which is what makes a phase measurement free.
	tuning := DefaultTuning()
	tuning.DiagnosticPhases = false
	if phases := beginRecallPhases(tuning, &phaseSampler{}, observer, "b1"); phases != nil {
		t.Fatal("a disabled accounting built a recorder")
	}
}

// A sampling rate reports one recall in N, and the recalls in between measure
// nothing.
func TestRecallPhaseSamplingReportsOneRecallInN(t *testing.T) {
	tests := []struct {
		name        string
		every       int
		recalls     int
		wantReports int
	}{
		{"every recall", 1, 3, 3},
		{"one in three", 3, 6, 2},
		{"an unset rate reports every recall", 0, 2, 2},
		{"a rate below one reports every recall", -4, 2, 2},
		{"a rate above the number of recalls reports none", 10, 3, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &recordingPhaseObserver{}
			store := phaseRecallStore(t, observer)
			tuneStore(t, store, func(tune *Tuning) { tune.PhaseSampleEvery = test.every })
			for range test.recalls {
				runPhaseRecall(t, store)
			}
			// Every reported recall carries exactly one bank_lookup, so its
			// count is the number of recalls that reported their phases.
			reports := observer.phaseNames()["bank_lookup"]
			if reports != test.wantReports {
				t.Fatalf("a rate of one in %d reported %d of %d recalls, want %d",
					test.every, reports, test.recalls, test.wantReports)
			}
		})
	}
}

// The sampling decision is per recall, so a reported recall reports all of its
// phases rather than a partial breakdown.
func TestRecallPhaseSamplingIsPerRecall(t *testing.T) {
	observer := &recordingPhaseObserver{}
	store := phaseRecallStore(t, observer)
	tuneStore(t, store, func(tune *Tuning) { tune.PhaseSampleEvery = 2 })
	for range 4 {
		runPhaseRecall(t, store)
	}
	counts := observer.phaseNames()
	if counts["bank_lookup"] != 2 {
		t.Fatalf("2 of 4 recalls should have been sampled: %v", counts)
	}
	for name, count := range counts {
		if count != counts["bank_lookup"] {
			t.Fatalf("phase %q was reported %d times, not once per sampled recall: %v", name, count, counts)
		}
	}
}

// A recall that fails still accounts for the phase it failed in, so a refused
// recall is not silently missing from the breakdown.
func TestRecallPhaseAccountingCoversTheFailingPhase(t *testing.T) {
	observer := &recordingPhaseObserver{}
	store := phaseRecallStore(t, observer)
	if _, err := store.Recall(context.TODO(), RecallQuery{BankID: "missing", Query: "topic"}); err == nil {
		t.Fatal("a recall against a missing bank succeeded")
	}
	counts := observer.phaseNames()
	if counts["bank_lookup"] != 1 {
		t.Fatalf("the failing phase was not accounted for: %v", counts)
	}
}

// The sampler itself: a rate below two passes every recall, and a rate of N
// passes exactly every Nth one.
func TestPhaseSampler(t *testing.T) {
	for _, every := range []int{1, 0, -3} {
		sampler := &phaseSampler{}
		for range 5 {
			if !sampler.sample(every) {
				t.Fatalf("a rate of one in %d passed a recall over", every)
			}
		}
	}
	sampler := &phaseSampler{}
	var passed []int
	for recall := 1; recall <= 6; recall++ {
		if sampler.sample(3) {
			passed = append(passed, recall)
		}
	}
	if len(passed) != 2 || passed[0] != 3 || passed[1] != 6 {
		t.Fatalf("a rate of one in three passed recalls %v, want [3 6]", passed)
	}
}

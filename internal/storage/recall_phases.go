package storage

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// armPhaseName names one retrieval arm's phase in the diagnostic accounting: the
// arm's own time inside the retrieval fan-out, reported as a subset of it.
func armPhaseName(arm string) string { return arm + "_arm" }

// RecallPhaseObservation is one phase's duration from one recall, as the
// diagnostic accounting reports it.
//
// Diagnostic marks a phase that is a SUBSET of another rather than a sibling of
// it — a per-arm timing inside the retrieval fan-out, say — so a consumer that
// sums phases into a request total can exclude it instead of double-counting.
type RecallPhaseObservation struct {
	// Phase is the phase's stable name, the same name the `[phases]` log line
	// prints.
	Phase string
	// Seconds is how long the phase took.
	Seconds float64
	// Diagnostic reports whether the phase is a subset of another.
	Diagnostic bool
	// BankID names the bank the recall ran against.
	BankID string
}

// RecallPhaseObserver receives one observation per phase of a sampled recall.
//
// The storage area has no metrics registry of its own, so a deployment that
// scrapes installs its histogram here; a store with no observer still logs the
// same accounting as a `[phases]` line, which is the minimum the reference
// gives its operators.
type RecallPhaseObserver interface {
	ObserveRecallPhase(RecallPhaseObservation)
}

// phaseSampler draws the per-recall sampling decision. Reporting one recall in
// N is decided once per recall rather than once per phase: the phases of one
// recall are what a reader compares, and drawing per phase would report a
// partial breakdown of the recalls it did report.
type phaseSampler struct {
	recalls atomic.Uint64
}

// sample reports whether the recall that is starting is one of the sampled
// ones. A rate below two samples every recall.
func (s *phaseSampler) sample(every int) bool {
	if every <= 1 {
		return true
	}
	return s.recalls.Add(1)%uint64(every) == 0
}

// recallPhaseEntry is one measured phase.
type recallPhaseEntry struct {
	name       string
	seconds    float64
	diagnostic bool
}

// recallPhases collects the phase timings of one recall and reports them when
// the recall ends.
//
// A nil recorder measures nothing, which is what a recall in a deployment with
// the diagnostic accounting off, or one that was not sampled, gets: the
// pipeline then pays a nil check and a call per phase.
//
// The retrieval arms are measured concurrently, so the entry list is guarded:
// one arm appends its entry while a sibling is closing its own. The clock each
// phase runs on is read before the lock, so contention between arms cannot
// lengthen the phase it measures.
type recallPhases struct {
	observer RecallPhaseObserver
	bankID   string
	started  time.Time
	mu       sync.Mutex
	entries  []recallPhaseEntry
}

// noopPhaseStop is the stop function an unmeasured phase gets, so a recall with
// the accounting off allocates neither a closure nor a slice entry per phase.
func noopPhaseStop() {}

// beginRecallPhases starts the accounting for one recall. It returns nil — the
// recorder whose methods do nothing — when the diagnostic switch is off or this
// recall is not one of the sampled ones.
func beginRecallPhases(tuning Tuning, sampler *phaseSampler, observer RecallPhaseObserver, bankID string) *recallPhases {
	if !tuning.DiagnosticPhases {
		return nil
	}
	if sampler != nil && !sampler.sample(tuning.PhaseSampleEvery) {
		return nil
	}
	return &recallPhases{observer: observer, bankID: bankID, started: time.Now()}
}

// measure starts one phase and returns the function that ends it. The returned
// function is called exactly once, including on a path that returns early, so a
// phase that failed still reports the time it spent.
//
// Phases are measured concurrently by the retrieval fan-out, so the entry is
// published under the recorder's lock and its duration is written under the
// same lock.
func (p *recallPhases) measure(name string, diagnostic bool) func() {
	if p == nil {
		return noopPhaseStop
	}
	start := time.Now()
	p.mu.Lock()
	p.entries = append(p.entries, recallPhaseEntry{name: name, diagnostic: diagnostic})
	index := len(p.entries) - 1
	p.mu.Unlock()
	return func() {
		seconds := time.Since(start).Seconds()
		p.mu.Lock()
		p.entries[index].seconds = seconds
		p.mu.Unlock()
	}
}

// snapshot returns the measured phases in the order they ran, plus the
// wall-clock seconds the whole recall took. It is how the reference's trace
// reports its `summary.phase_metrics` list (engine/search/tracer.py:203-236,
// 406-418): one entry per phase that ran, each with the seconds it took.
//
// A nil recorder — the accounting off, or a recall the sampler passed over —
// returns an empty list, which is the accurate answer: nothing was measured.
func (p *recallPhases) snapshot() ([]TracePhaseMetric, float64) {
	if p == nil {
		return []TracePhaseMetric{}, 0
	}
	p.mu.Lock()
	entries := make([]recallPhaseEntry, len(p.entries))
	copy(entries, p.entries)
	p.mu.Unlock()
	out := make([]TracePhaseMetric, 0, len(entries))
	for _, entry := range entries {
		details := map[string]any{}
		if entry.diagnostic {
			// The reference marks a phase whose time is a SUBSET of another's
			// `{"diagnostic": true}` so the coverage sum does not double-count
			// it (engine/search/tracer.py:203-236, memory_engine.py:9960).
			details["diagnostic"] = true
		}
		out = append(out, TracePhaseMetric{Name: entry.name, Seconds: entry.seconds, Details: details})
	}
	return out, time.Since(p.started).Seconds()
}

// finish reports the collected phases, as one observation per phase to the
// store's observer and as the reference's own accounting line. It is a no-op on
// a nil recorder and on a recall that measured nothing.
func (p *recallPhases) finish() {
	if p == nil {
		return
	}
	// Every arm has been joined by the time the recall finishes, so this is the
	// read side of the recorder's lock rather than a race against a live arm.
	p.mu.Lock()
	entries := make([]recallPhaseEntry, len(p.entries))
	copy(entries, p.entries)
	p.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	// Diagnostics are subsets of other phases, so summing them would
	// double-count: they are reported on their own line instead, and the
	// sibling sum is what the total is measured against.
	siblings := make([]recallPhaseEntry, 0, len(entries))
	subsets := make([]recallPhaseEntry, 0, len(entries))
	accounted := 0.0
	for _, entry := range entries {
		if entry.diagnostic {
			subsets = append(subsets, entry)
			continue
		}
		siblings = append(siblings, entry)
		accounted += entry.seconds
	}
	if p.observer != nil {
		for _, entry := range entries {
			p.observer.ObserveRecallPhase(RecallPhaseObservation{
				Phase:      entry.name,
				Seconds:    entry.seconds,
				Diagnostic: entry.diagnostic,
				BankID:     p.bankID,
			})
		}
	}
	total := time.Since(p.started).Seconds()
	slog.Default().Info(
		"  [phases] "+formatPhaseList(siblings)+
			" | accounted="+phaseMillis(accounted)+" of "+phaseMillis(total),
		"bank_id", p.bankID,
	)
	if len(subsets) > 0 {
		slog.Default().Info("  [phases:subsets] "+formatPhaseList(subsets), "bank_id", p.bankID)
	}
}

// formatPhaseList renders phases as `name=12ms`, slowest first: the line is
// read for where the time went, so the top of the list is where a reader looks
// first.
func formatPhaseList(entries []recallPhaseEntry) string {
	ordered := make([]recallPhaseEntry, len(entries))
	copy(ordered, entries)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].seconds > ordered[j].seconds })
	parts := make([]string, 0, len(ordered))
	for _, entry := range ordered {
		parts = append(parts, entry.name+"="+phaseMillis(entry.seconds))
	}
	return strings.Join(parts, ", ")
}

// phaseMillis renders a duration the way the accounting line prints it: whole
// milliseconds, because the line compares phases rather than measuring them.
func phaseMillis(seconds float64) string {
	return strconv.FormatFloat(seconds*1000, 'f', 0, 64) + "ms"
}

// recallArmJob is one arm of a recall's retrieval fan-out: the arm identifier
// the accounting reports it under, and the work that produces its hits.
type recallArmJob struct {
	// name is the arm identifier; its phase is reported as "<name>_arm".
	name string
	// run performs the arm and returns its best-first hits.
	run func(ctx context.Context) ([]recallHit, error)
}

// recallArmOutcome is one arm's result from the fan-out.
type recallArmOutcome struct {
	hits []recallHit
	err  error
}

// recallArmFanOut runs one recall's retrieval arms concurrently, bounded by the
// recall's connection budget.
//
// The arms of one recall are independent, so running them one after another
// makes a recall cost the sum of its arms; running them concurrently makes it
// cost the slowest one. The budget is what keeps that from becoming an
// unbounded drain on the connection pool: the arms of a single recall are the
// fan-out the reference bounds this way, and one recall must not be able to
// take every connection in the pool.
type recallArmFanOut struct {
	permits chan struct{}
}

// newRecallArmFanOut returns a scheduler that runs at most budget arms at once.
//
// A non-positive budget is the reference's shipped default rather than
// unbounded: the budget exists to protect the pool, so "unset" must not mean
// "no limit".
func newRecallArmFanOut(budget int) *recallArmFanOut {
	if budget <= 0 {
		budget = DefaultRecallConnectionBudget
	}
	return &recallArmFanOut{permits: make(chan struct{}, budget)}
}

// runRecallArms runs every job concurrently, bounded by the scheduler's budget,
// and returns one outcome per job in job order.
//
// The order is the contract: the caller merges the arms in a fixed order, so
// which arm happened to finish first must not decide what the fused pool holds
// or which arm's copy of a fact the result carries.
//
// Every job is joined before this returns, including when one of them failed: a
// sibling left running would measure its phase into a recorder the caller has
// already reported and would still be reading the store after the recall
// returned. The first failure in job order is returned as the recall's error,
// so the error surface does not depend on the race either; the outcomes of the
// other arms are still delivered alongside it.
func runRecallArms(ctx context.Context, fan *recallArmFanOut, phases *recallPhases, jobs []recallArmJob) ([]recallArmOutcome, error) {
	outcomes := make([]recallArmOutcome, len(jobs))
	var wg sync.WaitGroup
	for index, job := range jobs {
		wg.Add(1)
		go func(index int, job recallArmJob) {
			defer wg.Done()
			// The permit is taken before the arm's own phase starts, so a wait
			// for a sibling is charged to the fan-out rather than to the arm
			// that waited: the arm's phase is its own work.
			select {
			case fan.permits <- struct{}{}:
			case <-ctx.Done():
				outcomes[index].err = ctx.Err()
				return
			}
			stop := phases.measure(armPhaseName(job.name), true)
			hits, err := job.run(ctx)
			stop()
			<-fan.permits
			outcomes[index] = recallArmOutcome{hits: hits, err: err}
		}(index, job)
	}
	wg.Wait()
	for _, outcome := range outcomes {
		if outcome.err != nil {
			return outcomes, outcome.err
		}
	}
	return outcomes, nil
}

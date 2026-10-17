package storage

import (
	"context"
	"sync"
	"time"
)

// RetainPhaseObservation is one phase's accounting from one retain: the wall
// time the phase took and the number of round trips it made to get there.
//
// A phase that is slow because each call is slow and one that is slow because
// it makes ten times as many calls need opposite fixes, so the call count is
// carried beside the seconds rather than folded into them.
type RetainPhaseObservation struct {
	// Phase is the phase's stable name.
	Phase string
	// Store names the memories backend that served the retain, so a store-owned
	// bank's profile is separable from a Postgres one on the same deployment.
	Store string
	// Seconds is the time attributed to the phase, summed across the calls it
	// made. Phases overlap under concurrency, so their sum exceeds the retain's
	// own duration by design.
	Seconds float64
	// Calls is how many times the phase ran: the round-trip count.
	Calls int
}

// RetainPhaseObserver receives one observation per phase of a retain.
//
// The storage area has no metrics registry of its own, so a deployment that
// scrapes installs its histogram here; a process with no observer records
// nothing and a retain pays a context lookup per phase.
type RetainPhaseObserver interface {
	ObserveRetainPhase(RetainPhaseObservation)
}

// retainPhaseBox holds the process-wide observer. A package-level holder rather
// than a field on each store, because a deployment runs one store per process
// and both backends report into the same registry the exposition reads.
var retainPhaseBox struct {
	mu       sync.Mutex
	observer RetainPhaseObserver
}

// SetRetainPhaseObserver installs the process-wide receiver of retain phase
// observations. Registering nil removes it, so a process that measures nothing
// records nothing.
func SetRetainPhaseObserver(observer RetainPhaseObserver) {
	retainPhaseBox.mu.Lock()
	retainPhaseBox.observer = observer
	retainPhaseBox.mu.Unlock()
}

// retainPhaseObserver returns the installed observer, or nil when none is set.
func retainPhaseObserver() RetainPhaseObserver {
	retainPhaseBox.mu.Lock()
	defer retainPhaseBox.mu.Unlock()
	return retainPhaseBox.observer
}

// retainPhasesKey keys the accumulator of the retain in scope on its context,
// so the phases do not have to be threaded through every call: the embedder and
// the pool's acquire both read it from the context they already have.
type retainPhasesKey struct{}

// retainPhases accumulates one retain's phase totals. It is safe for concurrent
// use because sub-batches and pooled acquires can record from more than one
// goroutine.
type retainPhases struct {
	observer RetainPhaseObserver
	store    string
	mu       sync.Mutex
	order    []string
	seconds  map[string]float64
	calls    map[string]int
}

// noopRetainPhaseStop is the stop function a phase measured with no retain in
// scope gets, so an uninstrumented path allocates neither a closure entry nor a
// map write per phase.
func noopRetainPhaseStop() {}

// beginRetainPhases opens a phase accumulator for one retain and returns the
// context that carries it. With no observer installed it returns the context
// unchanged and a nil accumulator, whose methods are no-ops.
func beginRetainPhases(ctx context.Context, store string) (context.Context, *retainPhases) {
	observer := retainPhaseObserver()
	if observer == nil {
		return ctx, nil
	}
	accumulator := &retainPhases{
		observer: observer,
		store:    store,
		seconds:  map[string]float64{},
		calls:    map[string]int{},
	}
	return context.WithValue(ctx, retainPhasesKey{}, accumulator), accumulator
}

// measureRetainPhase starts one phase of the retain in scope and returns the
// function that ends it. Outside a retain it returns a no-op, so an instrumented
// helper (the embedder, the pool) can be called from a non-retain path without
// guarding every call site.
func measureRetainPhase(ctx context.Context, phase string) func() {
	accumulator, _ := ctx.Value(retainPhasesKey{}).(*retainPhases)
	if accumulator == nil {
		return noopRetainPhaseStop
	}
	return accumulator.measure(phase)
}

// recordRetainAcquireWait attributes one wait for a pooled connection to the
// retain in scope, if there is one. A no-op outside a retain.
func recordRetainAcquireWait(ctx context.Context, wait time.Duration) {
	accumulator, _ := ctx.Value(retainPhasesKey{}).(*retainPhases)
	if accumulator == nil {
		return
	}
	accumulator.record("pg.acquire", wait.Seconds(), 1)
}

// measure starts one phase and returns the function that ends it. The returned
// function is called exactly once, including on a path that returns early, so a
// phase that failed still reports the time it spent.
func (p *retainPhases) measure(phase string) func() {
	if p == nil {
		return noopRetainPhaseStop
	}
	start := time.Now()
	return func() { p.record(phase, time.Since(start).Seconds(), 1) }
}

// record adds one phase's time and call count to the accumulator.
func (p *retainPhases) record(phase string, seconds float64, calls int) {
	p.mu.Lock()
	if _, seen := p.seconds[phase]; !seen {
		p.order = append(p.order, phase)
	}
	p.seconds[phase] += seconds
	p.calls[phase] += calls
	p.mu.Unlock()
}

// finish reports the collected phases, one observation per phase. It is a no-op
// on a nil accumulator and on a retain that recorded no phase.
func (p *retainPhases) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	order := append([]string(nil), p.order...)
	observer := p.observer
	store := p.store
	seconds := p.seconds
	calls := p.calls
	p.mu.Unlock()
	if len(order) == 0 {
		return
	}
	for _, phase := range order {
		observer.ObserveRetainPhase(RetainPhaseObservation{
			Phase:   phase,
			Store:   store,
			Seconds: seconds[phase],
			Calls:   calls[phase],
		})
	}
}

// The store label each backend reports its phases under: the memories backend's
// own name, so a bank served by one backend is separable from another on the
// same deployment.
const (
	memoryStorePhaseLabel = "MemoryStore"
	// The reference's only memories backend is PostgresMemories, and the label
	// value is what a dashboard keys on, so the served backend reports that name
	// rather than this implementation's type name.
	postgresStorePhaseLabel = "PostgresMemories"
)

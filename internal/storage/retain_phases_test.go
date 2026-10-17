package storage

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingRetainPhases captures the per-phase observations a store reports,
// summing repeats so a multi-item retain reports one entry per phase.
type recordingRetainPhases struct {
	mu      sync.Mutex
	byPhase map[string]RetainPhaseObservation
}

func (r *recordingRetainPhases) ObserveRetainPhase(observation RetainPhaseObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byPhase == nil {
		r.byPhase = map[string]RetainPhaseObservation{}
	}
	entry := r.byPhase[observation.Phase]
	entry.Phase = observation.Phase
	entry.Store = observation.Store
	entry.Seconds += observation.Seconds
	entry.Calls += observation.Calls
	r.byPhase[observation.Phase] = entry
}

func (r *recordingRetainPhases) phase(name string) (RetainPhaseObservation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byPhase[name]
	return entry, ok
}

// installRetainPhaseRecorder installs a recorder for one test and removes it
// afterwards, so a test cannot leak an observer into its neighbours.
func installRetainPhaseRecorder(t *testing.T) *recordingRetainPhases {
	t.Helper()
	recorder := &recordingRetainPhases{}
	SetRetainPhaseObserver(recorder)
	t.Cleanup(func() { SetRetainPhaseObserver(nil) })
	return recorder
}

// ts-d8-retain-embed: a retain that embeds reports one embed phase labelled by
// the backend that served it, with the call count the phase made.
func TestRetainRecordsEmbedPhase(t *testing.T) {
	recorder := installRetainPhaseRecorder(t)
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "hello world", DocumentID: "doc-1"}},
		Embeddings: &stubEmbedder{dim: 3},
	}); err != nil {
		t.Fatal(err)
	}
	embed, ok := recorder.phase("embed")
	if !ok {
		t.Fatalf("no embed phase recorded: %+v", recorder.byPhase)
	}
	if embed.Store != memoryStorePhaseLabel {
		t.Fatalf("embed store = %q, want %q", embed.Store, memoryStorePhaseLabel)
	}
	if embed.Calls != 1 {
		t.Fatalf("embed calls = %d, want 1", embed.Calls)
	}
}

// A retain with no embedder runs no embed phase, so the family stays absent
// rather than reporting a zero-second phase nothing performed.
func TestRetainWithoutEmbedderRecordsNoEmbedPhase(t *testing.T) {
	recorder := installRetainPhaseRecorder(t)
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "hello world", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := recorder.phase("embed"); ok {
		t.Fatal("a retain with no embedder recorded an embed phase")
	}
}

// A retain that runs with no observer installed records nothing and does not
// fail, so a deployment that scrapes nothing pays no phase accounting.
func TestRetainWithoutObserverStillWrites(t *testing.T) {
	SetRetainPhaseObserver(nil)
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "hello world", DocumentID: "doc-1"}},
		Embeddings: &stubEmbedder{dim: 3},
	}); err != nil {
		t.Fatal(err)
	}
}

// ts-d8-retain-pg-acquire: a pooled acquire is attributed to the retain in
// scope and to no other, so the wait lands in that retain's breakdown rather
// than in a neighbour's.
func TestAcquireWaitAttributedToRetainInScope(t *testing.T) {
	recorder := installRetainPhaseRecorder(t)
	ctx, phases := beginRetainPhases(context.Background(), postgresStorePhaseLabel)
	recordRetainAcquireWait(ctx, 12*time.Millisecond)
	// An acquire with no retain in scope records nothing.
	recordRetainAcquireWait(context.Background(), time.Second)
	phases.finish()

	acquire, ok := recorder.phase("pg.acquire")
	if !ok {
		t.Fatalf("no pg.acquire phase recorded: %+v", recorder.byPhase)
	}
	if acquire.Store != postgresStorePhaseLabel {
		t.Fatalf("pg.acquire store = %q, want %q", acquire.Store, postgresStorePhaseLabel)
	}
	if acquire.Calls != 1 {
		t.Fatalf("pg.acquire calls = %d, want 1", acquire.Calls)
	}
}

// The acquire observer reports its wait to the retain in scope through the same
// path a statement's wait takes, so the pool's share of a retain is measurable.
func TestAcquireObserverReportsWaitToRetain(t *testing.T) {
	recorder := installRetainPhaseRecorder(t)
	ctx, phases := beginRetainPhases(context.Background(), postgresStorePhaseLabel)
	observer := acquireObserver{
		now: advancingClock(time.Unix(0, 0), 250*time.Millisecond),
	}
	if _, err := observer.acquire(ctx, func(context.Context) (connHandle, error) {
		return observerConn{}, nil
	}); err != nil {
		t.Fatalf("acquire error = %v", err)
	}
	phases.finish()
	acquire, ok := recorder.phase("pg.acquire")
	if !ok {
		t.Fatalf("no pg.acquire phase recorded: %+v", recorder.byPhase)
	}
	if acquire.Seconds != 0.25 {
		t.Fatalf("pg.acquire seconds = %v, want 0.25", acquire.Seconds)
	}
}

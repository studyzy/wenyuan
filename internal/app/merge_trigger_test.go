package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// slowMergeStore is a merge store whose round blocks until released, so a test can
// observe the trigger's in-flight handling.
type slowMergeStore struct {
	fakeMergeStore
	entered chan struct{}
	release chan struct{}
	// rounds counts completed BeginMergeStanza calls.
	rounds atomic.Int32
}

func (s *slowMergeStore) BeginMergeStanza(
	ctx context.Context, teamID, projectID string, batch int, stale time.Duration,
	fn func(storage.MergeStanza) error,
) (bool, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	s.rounds.Add(1)
	return false, nil
}

// newTriggerForTest builds a trigger over a project the fixture always reports.
func newTriggerForTest(t *testing.T, store storage.MergeStore) *mergeTrigger {
	t.Helper()
	runner := &mergeRunner{
		merges: store, model: &answerProvider{name: "fake"}, cfg: mergeTestCfg(),
	}
	return newMergeTrigger(runner)
}

// TestTriggerRunsTheProject pins the happy path: the project is merged in the
// background and the caller is not blocked on it.
func TestTriggerRunsTheProject(t *testing.T) {
	store := &slowMergeStore{
		fakeMergeStore: fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}},
		entered:        make(chan struct{}, 1),
		release:        make(chan struct{}),
	}
	trigger := newTriggerForTest(t, store)
	trigger.Trigger("acme", "web")
	// The call returned without waiting for the round.
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the round never started")
	}
	close(store.release)
	trigger.Wait()
	if got := store.rounds.Load(); got != 1 {
		t.Fatalf("rounds = %d, want 1", got)
	}
}

// TestTriggerCoalescesAnInFlightProject pins that a second request for a project
// already being merged does not start a second round: its inputs are re-read each
// round, and the claim would refuse the duplicate anyway.
func TestTriggerCoalescesAnInFlightProject(t *testing.T) {
	store := &slowMergeStore{
		fakeMergeStore: fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}},
		entered:        make(chan struct{}, 1),
		release:        make(chan struct{}),
	}
	trigger := newTriggerForTest(t, store)
	trigger.Trigger("acme", "web")
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first round never started")
	}
	// While the first round is blocked, ask twice more.
	trigger.Trigger("acme", "web")
	trigger.Trigger("acme", "web")
	close(store.release)
	trigger.Wait()
	if got := store.rounds.Load(); got != 1 {
		t.Fatalf("rounds = %d, want the duplicates coalesced into one", got)
	}
}

// TestTriggerKeepsProjectsIndependent pins that coalescing is per project: two
// different projects both run.
func TestTriggerKeepsProjectsIndependent(t *testing.T) {
	other := mergeTestProject()
	other.ProjectID = "api"
	other.BankID = "acme::Api"
	other.Name = "Api"
	store := &slowMergeStore{
		fakeMergeStore: fakeMergeStore{projects: []storage.MergeProject{mergeTestProject(), other}},
		entered:        make(chan struct{}, 2),
		release:        make(chan struct{}),
	}
	trigger := newTriggerForTest(t, store)
	trigger.Trigger("acme", "web")
	trigger.Trigger("acme", "api")
	close(store.release)
	trigger.Wait()
	if got := store.rounds.Load(); got != 2 {
		t.Fatalf("rounds = %d, want both projects merged", got)
	}
}

// TestTriggerIgnoresAnUnknownProject pins that a project that went away between
// the request and the round is not an error.
func TestTriggerIgnoresAnUnknownProject(t *testing.T) {
	store := &slowMergeStore{
		fakeMergeStore: fakeMergeStore{projects: nil},
		entered:        make(chan struct{}, 1),
		release:        make(chan struct{}),
	}
	close(store.release)
	trigger := newTriggerForTest(t, store)
	trigger.Trigger("acme", "gone")
	trigger.Wait()
	if got := store.rounds.Load(); got != 0 {
		t.Fatalf("rounds = %d, want none for a project that is not listed", got)
	}
}

// TestTriggerIsNilSafe pins that a deployment without a merge store does not
// panic on a management action that asks for one.
func TestTriggerIsNilSafe(t *testing.T) {
	var trigger *mergeTrigger
	trigger.Trigger("acme", "web")
	trigger.Wait()
	var application *App
	application.TriggerMerge("acme", "web")
	application.CloseMerges()
}

// TestTriggerIsConcurrencySafe exercises the running map under the race detector.
func TestTriggerIsConcurrencySafe(t *testing.T) {
	store := &slowMergeStore{
		fakeMergeStore: fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}},
		entered:        make(chan struct{}, 32),
		release:        make(chan struct{}),
	}
	trigger := newTriggerForTest(t, store)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			trigger.Trigger("acme", "web")
		}()
	}
	wg.Wait()
	close(store.release)
	trigger.Wait()
	if got := store.rounds.Load(); got > 1 {
		t.Fatalf("rounds = %d, want at most one for one project", got)
	}
}

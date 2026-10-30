package app

import (
	"context"
	"log/slog"
	"sync"
)

// On-demand merging: a management action asks for one project's round now.
//
// The scheduled job would reach the project on its next tick, but a member just
// added to a project expects their existing facts to appear in the project's
// shared content without waiting. The round is the same one the job runs; only
// its start time differs, so this adds a trigger rather than a second merge
// path.

// mergeTrigger runs on-demand merge rounds for one deployment.
//
// A round spends a model call, so the work runs in the background and the
// request that asked for it returns at once. The project's claim already makes a
// concurrent round harmless — and necessary: the claim is what stops a trigger
// that fires while the scheduled tick is mid-round from producing a second one.
type mergeTrigger struct {
	runner *mergeRunner

	mu sync.Mutex
	// running holds the project keys a goroutine is currently merging.
	running map[string]bool
	// wg lets a shutdown wait for the rounds in flight.
	wg sync.WaitGroup
}

// newMergeTrigger returns a trigger over a runner.
func newMergeTrigger(runner *mergeRunner) *mergeTrigger {
	return &mergeTrigger{runner: runner, running: map[string]bool{}}
}

// Trigger asks for one project's round.
//
// It is deliberately quiet: a merge that cannot start — no model, no store, or a
// round already in flight for this project — leaves the scheduled job to catch up
// on its next tick, and no management request should fail because of it.
func (t *mergeTrigger) Trigger(teamID, projectID string) {
	if t == nil || t.runner == nil {
		return
	}
	key := teamID + "\x00" + projectID
	t.mu.Lock()
	if t.running[key] {
		// A round for this project is already running. Its inputs are read from
		// the database at the start of the round, so the member this request just
		// added may or may not be in it; the scheduled tick covers the gap. A
		// second concurrent round would only contend for the same claim.
		t.mu.Unlock()
		return
	}
	t.running[key] = true
	t.mu.Unlock()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer func() {
			t.mu.Lock()
			delete(t.running, key)
			t.mu.Unlock()
		}()
		t.run(teamID, projectID)
	}()
}

// run merges one project.
func (t *mergeTrigger) run(teamID, projectID string) {
	// The trigger outlives the request that asked for it, so it gets a context of
	// its own with the same per-round budget the scheduled job uses.
	deadline := roundDeadline(t.runner.cfg.ProjectMergeInterval)
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	projects, err := t.runner.merges.ListMergeProjects(ctx)
	if err != nil {
		slog.Default().Warn("could not list projects for an on-demand merge",
			"team_id", teamID, "project_id", projectID, "error", err)
		return
	}
	for _, project := range projects {
		if project.TeamID != teamID || project.ProjectID != projectID {
			continue
		}
		if err := t.runner.runMergeRound(ctx, project, deadline); err != nil {
			slog.Default().Warn("on-demand merge round failed",
				"team_id", teamID, "project_id", projectID, "error", err)
		}
		return
	}
	// The project went away between the request and the round, or it has no
	// members left, which is not an error worth reporting.
}

// Wait blocks until the in-flight rounds finish, so a shutdown does not abandon
// one mid-transaction.
func (t *mergeTrigger) Wait() {
	if t == nil {
		return
	}
	t.wg.Wait()
}

// TriggerMerge asks for one project's merge round to run now.
//
// It is the management plane's entry point; see mergeTrigger.Trigger.
func (a *App) TriggerMerge(teamID, projectID string) {
	if a == nil || a.mergeTrigger == nil {
		return
	}
	a.mergeTrigger.Trigger(teamID, projectID)
}

// CloseMerges waits for the in-flight merge rounds to finish.
func (a *App) CloseMerges() {
	if a == nil {
		return
	}
	a.mergeTrigger.Wait()
}

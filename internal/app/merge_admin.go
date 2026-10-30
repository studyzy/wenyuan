package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// mergeAdminFor is the management plane's handle on the merge job.
//
// It pairs the store's ledger operations with the trigger that runs a round now:
// the plane changes membership, and this decides what the merge job does about
// it. The two are wired together here rather than in the store because the
// trigger needs the model and the job's tuning, which the store does not carry.
type mergeAdminFor struct {
	store   storage.MergeStore
	trigger *mergeTrigger
}

// ClearMemberProject removes one member's ledger rows and state for one project.
func (m mergeAdminFor) ClearMemberProject(ctx context.Context, teamID, projectID, tenantID string) error {
	return m.store.ClearMemberProject(ctx, teamID, projectID, tenantID)
}

// RequestRebuild asks the next merge round to ignore the cursors.
func (m mergeAdminFor) RequestRebuild(ctx context.Context, teamID, projectID string) error {
	return m.store.RequestRebuild(ctx, teamID, projectID)
}

// TriggerMerge runs one project's merge round now, in the background.
func (m mergeAdminFor) TriggerMerge(_ context.Context, teamID, projectID string) {
	m.trigger.Trigger(teamID, projectID)
}

// MergeStatus reports a project's merge state.
func (m mergeAdminFor) MergeStatus(ctx context.Context, teamID, projectID string) (storage.MergeStatus, error) {
	return m.store.MergeStatus(ctx, teamID, projectID)
}

// ResetMergeState clears a halted project's circuit breaker.
func (m mergeAdminFor) ResetMergeState(ctx context.Context, teamID, projectID string) error {
	return m.store.ResetMergeState(ctx, teamID, projectID)
}

package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/config"
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

// personalAdminFor is the management plane's handle on the personal aggregate.
//
// It exists because the plane speaks tenant ids, while the aggregate's store
// reports the shared-schema fact itself: a single-tenant deployment resolves every
// credential to one schema, so "this person's aggregate" is the deployment's.
type personalAdminFor struct {
	store storage.PersonalStore
	cfg   config.Config
}

// PersonalAggregateStatus reports one tenant's aggregate state.
func (p personalAdminFor) PersonalAggregateStatus(ctx context.Context, tenantID string) (storage.PersonalStatus, error) {
	return p.store.PersonalStatus(ctx, tenantID, p.cfg.TenantMode == config.TenantModeSingle)
}

// ResetPersonalAggregate clears a halted tenant's circuit breaker.
func (p personalAdminFor) ResetPersonalAggregate(ctx context.Context, tenantID string) error {
	return p.store.ResetPersonalState(ctx, tenantID)
}

// RebuildPersonalAggregate asks the next round to ignore its watermarks.
func (p personalAdminFor) RebuildPersonalAggregate(ctx context.Context, tenantID string) error {
	return p.store.RequestRebuild(ctx, tenantID)
}

package app

import (
	"context"
	"errors"

	"github.com/studyzy/wenyuan/internal/adminapi"
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
	// teams is the team registry, present only in a multi-tenant deployment. It
	// is what lets a team's page show its members' aggregates; without teams
	// there is no team to show.
	teams storage.TeamRegistry
	cfg   config.Config
}

// PersonalAggregateStatus reports one tenant's aggregate state.
func (p personalAdminFor) PersonalAggregateStatus(ctx context.Context, tenantID string) (storage.PersonalStatus, error) {
	status, err := p.store.PersonalStatus(ctx, tenantID, p.cfg.TenantMode == config.TenantModeSingle)
	if errors.Is(err, storage.ErrPersonalAggregateIsTeam) {
		return status, adminapi.ErrNotPersonal
	}
	return status, err
}

// TeamMembers returns a team's member tenants, which is whose aggregates a team's
// page lists.
func (p personalAdminFor) TeamMembers(ctx context.Context, teamID string) ([]string, error) {
	if p.teams == nil {
		return nil, nil
	}
	members, err := p.teams.ListTeamMembers(ctx, teamID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(members))
	for _, member := range members {
		out = append(out, member.TenantID)
	}
	return out, nil
}

// ResetPersonalAggregate clears a halted tenant's circuit breaker.
func (p personalAdminFor) ResetPersonalAggregate(ctx context.Context, tenantID string) error {
	return p.store.ResetPersonalState(ctx, tenantID)
}

// RebuildPersonalAggregate asks the next round to ignore its watermarks.
func (p personalAdminFor) RebuildPersonalAggregate(ctx context.Context, tenantID string) error {
	return p.store.RequestRebuild(ctx, tenantID)
}

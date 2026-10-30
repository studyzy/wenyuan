package storage

import (
	"context"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// TeamRegistry is the control-plane store for teams and their membership.
//
// A team is a tenant of kind "team"; the registry holds which tenants belong to
// which team and which projects a team owns. Like TenantRegistry it lives in
// the admin schema and is separate from Store, which is bound to one tenant's
// schema.
//
// The methods report three sentinels: tenant.ErrNotFound when a row does not
// exist, tenant.ErrExists when a unique constraint is violated, and
// tenant.ErrKindMismatch when an operation names a tenant that is not a team.
// Anything else is a storage failure.
type TeamRegistry interface {
	// ListTeams returns the team tenants matching filter, in id order.
	ListTeams(ctx context.Context, filter TenantFilter) ([]tenant.Team, error)
	// GetTeam returns one team by tenant id.
	GetTeam(ctx context.Context, teamID string) (tenant.Team, error)
	// UpdateTeamName renames a team's display name.
	UpdateTeamName(ctx context.Context, teamID, displayName string) (tenant.Team, error)
	// SoftDeleteTeam marks a team deleted. Its schema, banks and rows stay.
	SoftDeleteTeam(ctx context.Context, teamID string) (tenant.Team, error)

	// ListTeamMembers returns a team's members in join order.
	ListTeamMembers(ctx context.Context, teamID string) ([]tenant.TeamMember, error)
	// AddTeamMember adds a member, or updates the role of an existing one.
	AddTeamMember(ctx context.Context, teamID, tenantID string, role tenant.MemberRole) error
	// RemoveTeamMember removes a member and their project membership with them.
	RemoveTeamMember(ctx context.Context, teamID, tenantID string) error

	// ListTeamProjects returns a team's projects in creation order.
	ListTeamProjects(ctx context.Context, teamID string) ([]tenant.TeamProject, error)
	// GetTeamProject returns one project of a team.
	GetTeamProject(ctx context.Context, teamID, projectID string) (tenant.TeamProject, error)
	// CreateTeamProject records a project. Its bank is created by the caller.
	CreateTeamProject(ctx context.Context, project tenant.TeamProject) error
	// DeleteTeamProject removes a project and its membership rows. The banks the
	// project owns are the caller's to delete, first.
	DeleteTeamProject(ctx context.Context, teamID, projectID string) error

	// ListProjectMembers returns the members taking part in a project.
	ListProjectMembers(ctx context.Context, teamID, projectID string) ([]tenant.ProjectMember, error)
	// AddProjectMember adds a team member to a project.
	AddProjectMember(ctx context.Context, teamID, projectID, tenantID string) error
	// RemoveProjectMember removes a member from a project.
	RemoveProjectMember(ctx context.Context, teamID, projectID, tenantID string) error
}

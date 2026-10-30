package storage

import (
	"context"
	"sort"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// MemoryTeamRegistry is the in-process team registry used by tests and by a
// deployment that runs the control plane without PostgreSQL.
//
// It shares its tenant map with a MemoryTenantRegistry, so the rules that tie
// teams to tenants — a member must be a tenant, a team must be a tenant of kind
// team — hold on this backend exactly as they do in the database.
type MemoryTeamRegistry struct {
	tenants *MemoryTenantRegistry

	members  map[string]map[string]tenant.TeamMember   // team -> tenant -> row
	projects map[string]map[string]tenant.TeamProject  // team -> project -> row
	taken    map[string]map[string]string              // team -> project name -> project id
	joined   map[string]map[string]map[string]struct{} // team -> project -> tenant
}

// NewMemoryTeamRegistry returns an empty registry over the given tenant
// registry. A nil tenant registry gets a fresh one of its own.
func NewMemoryTeamRegistry(tenants *MemoryTenantRegistry) *MemoryTeamRegistry {
	if tenants == nil {
		tenants = NewMemoryTenantRegistry()
	}
	return &MemoryTeamRegistry{
		tenants:  tenants,
		members:  make(map[string]map[string]tenant.TeamMember),
		projects: make(map[string]map[string]tenant.TeamProject),
		taken:    make(map[string]map[string]string),
		joined:   make(map[string]map[string]map[string]struct{}),
	}
}

// ListTeams returns the team tenants matching filter, in id order.
func (r *MemoryTeamRegistry) ListTeams(ctx context.Context, filter TenantFilter) ([]tenant.Team, error) {
	filter.Kind = tenant.KindTeam
	records, err := r.tenants.ListTenants(ctx, filter)
	if err != nil {
		return nil, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	teams := make([]tenant.Team, 0, len(records))
	for _, record := range records {
		teams = append(teams, tenant.Team{
			Record:       record,
			MemberCount:  len(r.members[record.ID]),
			ProjectCount: len(r.projects[record.ID]),
		})
	}
	return teams, nil
}

// GetTeam returns one team by tenant id.
func (r *MemoryTeamRegistry) GetTeam(ctx context.Context, teamID string) (tenant.Team, error) {
	record, err := r.teamRecord(ctx, teamID)
	if err != nil {
		return tenant.Team{}, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	return r.teamView(record), nil
}

// UpdateTeamName renames a team's display name.
func (r *MemoryTeamRegistry) UpdateTeamName(ctx context.Context, teamID, displayName string) (tenant.Team, error) {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return tenant.Team{}, err
	}
	updated, err := r.tenants.UpdateTenant(ctx, teamID, TenantUpdate{DisplayName: &displayName})
	if err != nil {
		return tenant.Team{}, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	return r.teamView(updated), nil
}

// SoftDeleteTeam marks a team deleted and leaves its rows in place.
func (r *MemoryTeamRegistry) SoftDeleteTeam(ctx context.Context, teamID string) (tenant.Team, error) {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return tenant.Team{}, err
	}
	updated, err := r.tenants.DeleteTenant(ctx, teamID, time.Now().UTC())
	if err != nil {
		return tenant.Team{}, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	return r.teamView(updated), nil
}

// ListTeamMembers returns a team's members in join order.
func (r *MemoryTeamRegistry) ListTeamMembers(ctx context.Context, teamID string) ([]tenant.TeamMember, error) {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return nil, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	members := make([]tenant.TeamMember, 0, len(r.members[teamID]))
	for _, member := range r.members[teamID] {
		member.DisplayName = r.displayName(member.TenantID)
		members = append(members, member)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].TenantID < members[j].TenantID })
	return members, nil
}

// AddTeamMember adds a member, or updates the role of one already there.
func (r *MemoryTeamRegistry) AddTeamMember(ctx context.Context, teamID, tenantID string, role tenant.MemberRole) error {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return err
	}
	if !role.Valid() {
		return tenant.ErrInvalidTransition
	}
	r.tenants.mu.Lock()
	defer r.tenants.mu.Unlock()
	if record, ok := r.tenants.tenants[tenantID]; !ok || record.Status != tenant.StatusActive {
		// The member does not exist, or exists in a state a team must not admit.
		// Both are the same refusal to the caller; the message is built there.
		if !ok {
			return tenant.ErrNotFound
		}
		return tenant.ErrInvalidTransition
	}
	if r.members[teamID] == nil {
		r.members[teamID] = make(map[string]tenant.TeamMember)
	}
	member, exists := r.members[teamID][tenantID]
	if !exists {
		member = tenant.TeamMember{TeamID: teamID, TenantID: tenantID, JoinedAt: time.Now().UTC()}
	}
	member.Role = role
	r.members[teamID][tenantID] = member
	return nil
}

// RemoveTeamMember removes a member and their project membership with them.
func (r *MemoryTeamRegistry) RemoveTeamMember(ctx context.Context, teamID, tenantID string) error {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return err
	}
	r.tenants.mu.Lock()
	defer r.tenants.mu.Unlock()
	if _, ok := r.members[teamID][tenantID]; !ok {
		return tenant.ErrNotFound
	}
	delete(r.members[teamID], tenantID)
	for projectID := range r.joined[teamID] {
		delete(r.joined[teamID][projectID], tenantID)
	}
	return nil
}

// ListTeamProjects returns a team's projects in creation order.
func (r *MemoryTeamRegistry) ListTeamProjects(ctx context.Context, teamID string) ([]tenant.TeamProject, error) {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return nil, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	projects := make([]tenant.TeamProject, 0, len(r.projects[teamID]))
	for _, project := range r.projects[teamID] {
		project.MemberCount = len(r.joined[teamID][project.ProjectID])
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].CreatedAt.Equal(projects[j].CreatedAt) {
			return projects[i].CreatedAt.Before(projects[j].CreatedAt)
		}
		return projects[i].ProjectID < projects[j].ProjectID
	})
	return projects, nil
}

// GetTeamProject returns one project of a team.
func (r *MemoryTeamRegistry) GetTeamProject(ctx context.Context, teamID, projectID string) (tenant.TeamProject, error) {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return tenant.TeamProject{}, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	project, ok := r.projects[teamID][projectID]
	if !ok {
		return tenant.TeamProject{}, tenant.ErrNotFound
	}
	project.MemberCount = len(r.joined[teamID][projectID])
	return project, nil
}

// CreateTeamProject records a project. The bank id is computed here when the
// caller left it empty, which keeps both backends agreeing on the shape.
func (r *MemoryTeamRegistry) CreateTeamProject(ctx context.Context, project tenant.TeamProject) error {
	if _, err := r.teamRecord(ctx, project.TeamID); err != nil {
		return err
	}
	if err := tenant.ValidateProjectID(project.ProjectID); err != nil {
		return err
	}
	if err := tenant.ValidateProjectName(project.Name); err != nil {
		return err
	}
	if project.BankID == "" {
		project.BankID = tenant.ProjectBankID(project.TeamID, project.Name)
	}
	if project.CreatedAt.IsZero() {
		project.CreatedAt = time.Now().UTC()
	}
	r.tenants.mu.Lock()
	defer r.tenants.mu.Unlock()
	if _, ok := r.projects[project.TeamID][project.ProjectID]; ok {
		return tenant.ErrExists
	}
	if r.taken[project.TeamID] == nil {
		r.taken[project.TeamID] = make(map[string]string)
	}
	if _, ok := r.taken[project.TeamID][project.Name]; ok {
		return tenant.ErrExists
	}
	for _, existing := range r.projects[project.TeamID] {
		if existing.BankID == project.BankID {
			return tenant.ErrExists
		}
	}
	if r.projects[project.TeamID] == nil {
		r.projects[project.TeamID] = make(map[string]tenant.TeamProject)
	}
	r.projects[project.TeamID][project.ProjectID] = project
	r.taken[project.TeamID][project.Name] = project.ProjectID
	return nil
}

// DeleteTeamProject removes a project and its membership rows.
func (r *MemoryTeamRegistry) DeleteTeamProject(ctx context.Context, teamID, projectID string) error {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return err
	}
	r.tenants.mu.Lock()
	defer r.tenants.mu.Unlock()
	project, ok := r.projects[teamID][projectID]
	if !ok {
		return tenant.ErrNotFound
	}
	delete(r.projects[teamID], projectID)
	delete(r.taken[teamID], project.Name)
	delete(r.joined[teamID], projectID)
	return nil
}

// ListProjectMembers returns the members taking part in a project.
func (r *MemoryTeamRegistry) ListProjectMembers(ctx context.Context, teamID, projectID string) ([]tenant.ProjectMember, error) {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return nil, err
	}
	r.tenants.mu.RLock()
	defer r.tenants.mu.RUnlock()
	if _, ok := r.projects[teamID][projectID]; !ok {
		return nil, tenant.ErrNotFound
	}
	members := make([]tenant.ProjectMember, 0, len(r.joined[teamID][projectID]))
	for tenantID := range r.joined[teamID][projectID] {
		member := tenant.ProjectMember{TeamID: teamID, ProjectID: projectID, TenantID: tenantID}
		member.DisplayName = r.displayName(tenantID)
		if row, ok := r.members[teamID][tenantID]; ok {
			member.AddedAt = row.JoinedAt
		}
		members = append(members, member)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].TenantID < members[j].TenantID })
	return members, nil
}

// AddProjectMember adds a team member to a project.
func (r *MemoryTeamRegistry) AddProjectMember(ctx context.Context, teamID, projectID, tenantID string) error {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return err
	}
	r.tenants.mu.Lock()
	defer r.tenants.mu.Unlock()
	if _, ok := r.projects[teamID][projectID]; !ok {
		return tenant.ErrNotFound
	}
	if _, ok := r.members[teamID][tenantID]; !ok {
		// Only a member of the team may take part in one of its projects. The
		// management plane reports this as a conflict, not a missing row.
		return tenant.ErrInvalidTransition
	}
	if r.joined[teamID] == nil {
		r.joined[teamID] = make(map[string]map[string]struct{})
	}
	if r.joined[teamID][projectID] == nil {
		r.joined[teamID][projectID] = make(map[string]struct{})
	}
	r.joined[teamID][projectID][tenantID] = struct{}{}
	return nil
}

// RemoveProjectMember removes a member from a project.
func (r *MemoryTeamRegistry) RemoveProjectMember(ctx context.Context, teamID, projectID, tenantID string) error {
	if _, err := r.teamRecord(ctx, teamID); err != nil {
		return err
	}
	r.tenants.mu.Lock()
	defer r.tenants.mu.Unlock()
	if _, ok := r.projects[teamID][projectID]; !ok {
		return tenant.ErrNotFound
	}
	if _, ok := r.joined[teamID][projectID][tenantID]; !ok {
		return tenant.ErrNotFound
	}
	delete(r.joined[teamID][projectID], tenantID)
	return nil
}

// teamRecord reads a tenant and refuses one that is not a team.
func (r *MemoryTeamRegistry) teamRecord(ctx context.Context, teamID string) (tenant.Record, error) {
	record, err := r.tenants.GetTenant(ctx, teamID)
	if err != nil {
		return tenant.Record{}, err
	}
	if !record.IsTeam() {
		return tenant.Record{}, tenant.ErrKindMismatch
	}
	return record, nil
}

// teamView fills a team's counts. The caller holds the tenant lock.
func (r *MemoryTeamRegistry) teamView(record tenant.Record) tenant.Team {
	return tenant.Team{
		Record:       record,
		MemberCount:  len(r.members[record.ID]),
		ProjectCount: len(r.projects[record.ID]),
	}
}

// displayName reads a tenant's display name. The caller holds the tenant lock.
func (r *MemoryTeamRegistry) displayName(tenantID string) string {
	if record, ok := r.tenants.tenants[tenantID]; ok {
		return record.DisplayName
	}
	return ""
}

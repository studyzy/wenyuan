package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The PostgreSQL implementation of TeamRegistry. Every statement runs against
// the same admin schema as the tenant registry, because a team is a tenant and
// the team tables reference the tenant table.

// teamColumns is the team view: the tenant record plus the two counts.
const teamColumns = "t.tenant_id, t.display_name, t.status, t.kind, t.config, t.created_at, t.updated_at, t.deleted_at, " +
	"(SELECT count(*) FROM %s tm WHERE tm.team_tenant_id = t.tenant_id), " +
	"(SELECT count(*) FROM %s tp WHERE tp.team_tenant_id = t.tenant_id)"

const teamMemberColumns = "m.team_tenant_id, m.tenant_id, m.role, m.joined_at, COALESCE(t.display_name, '')"

const teamProjectColumns = "p.team_tenant_id, p.project_id, p.name, p.bank_id, p.created_at, " +
	"(SELECT count(*) FROM %s pm WHERE pm.team_tenant_id = p.team_tenant_id AND pm.project_id = p.project_id)"

const projectMemberColumns = "pm.team_tenant_id, pm.project_id, pm.tenant_id, pm.added_at, COALESCE(t.display_name, '')"

// teamsWhere builds the team predicate: the row exists and is a team.
func (r *PostgresTenantRegistry) teamsWhere(alias string) string {
	return alias + ".kind = 'team'"
}

// ListTeams returns the team tenants matching filter, in id order.
func (r *PostgresTenantRegistry) ListTeams(ctx context.Context, filter TenantFilter) ([]tenant.Team, error) {
	var args []any
	where := []string{"t.kind = 'team'"}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
		where = append(where, fmt.Sprintf("t.status = $%d", len(args)))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, "%"+escapeLike(query)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(t.tenant_id ILIKE $%d ESCAPE '\\' OR t.display_name ILIKE $%d ESCAPE '\\')", n, n))
	}
	statement := "SELECT " + r.teamSelect() + " FROM " + r.qualified("tenant") + " t WHERE " +
		strings.Join(where, " AND ") + " ORDER BY t.tenant_id"
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		statement += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		statement += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	teams := make([]tenant.Team, 0)
	for rows.Next() {
		team, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

// requireTeam reads a tenant and refuses one that is missing or is not a team.
func (r *PostgresTenantRegistry) requireTeam(ctx context.Context, teamID string) (tenant.Team, error) {
	record, err := r.GetTenant(ctx, teamID)
	if err != nil {
		return tenant.Team{}, err
	}
	if !record.IsTeam() {
		return tenant.Team{}, tenant.ErrKindMismatch
	}
	return tenant.Team{Record: record}, nil
}

// teamSelect renders the team view's column list with its subqueries pointing
// at the team tables in this registry's schema.
func (r *PostgresTenantRegistry) teamSelect() string {
	return fmt.Sprintf(teamColumns, r.qualified("team_member"), r.qualified("team_project"))
}

// GetTeam returns one team by tenant id.
func (r *PostgresTenantRegistry) GetTeam(ctx context.Context, teamID string) (tenant.Team, error) {
	if _, err := r.requireTeam(ctx, teamID); err != nil {
		return tenant.Team{}, err
	}
	team, err := scanTeam(r.db.QueryRowContext(ctx,
		"SELECT "+r.teamSelect()+" FROM "+r.qualified("tenant")+" t WHERE t.tenant_id = $1 AND "+r.teamsWhere("t"),
		teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.Team{}, tenant.ErrNotFound
	}
	return team, err
}

// UpdateTeamName renames a team's display name.
func (r *PostgresTenantRegistry) UpdateTeamName(ctx context.Context, teamID, displayName string) (tenant.Team, error) {
	if _, err := r.requireTeam(ctx, teamID); err != nil {
		return tenant.Team{}, err
	}
	result, err := r.db.ExecContext(ctx,
		"UPDATE "+r.qualified("tenant")+" SET display_name = $2, updated_at = now() WHERE tenant_id = $1 AND kind = 'team'",
		teamID, displayName)
	if err != nil {
		return tenant.Team{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return tenant.Team{}, err
	} else if affected == 0 {
		return tenant.Team{}, tenant.ErrNotFound
	}
	return r.GetTeam(ctx, teamID)
}

// SoftDeleteTeam marks a team deleted and leaves its rows in place.
func (r *PostgresTenantRegistry) SoftDeleteTeam(ctx context.Context, teamID string) (tenant.Team, error) {
	if _, err := r.requireTeam(ctx, teamID); err != nil {
		return tenant.Team{}, err
	}
	result, err := r.db.ExecContext(ctx,
		"UPDATE "+r.qualified("tenant")+" SET status = $2, updated_at = now(), deleted_at = COALESCE(deleted_at, now())"+
			" WHERE tenant_id = $1 AND kind = 'team' AND status <> $2",
		teamID, string(tenant.StatusDeleted))
	if err != nil {
		return tenant.Team{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return tenant.Team{}, err
	} else if affected == 0 {
		// Either the team is already deleted (still a team) or it is missing.
		return r.GetTeam(ctx, teamID)
	}
	return r.GetTeam(ctx, teamID)
}

// ListTeamMembers returns a team's members.
func (r *PostgresTenantRegistry) ListTeamMembers(ctx context.Context, teamID string) ([]tenant.TeamMember, error) {
	if _, err := r.GetTeam(ctx, teamID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+teamMemberColumns+" FROM "+r.qualified("team_member")+" m"+
			" LEFT JOIN "+r.qualified("tenant")+" t ON t.tenant_id = m.tenant_id"+
			" WHERE m.team_tenant_id = $1 ORDER BY m.joined_at, m.tenant_id", teamID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := make([]tenant.TeamMember, 0)
	for rows.Next() {
		var member tenant.TeamMember
		var role string
		if err := rows.Scan(&member.TeamID, &member.TenantID, &role, &member.JoinedAt, &member.DisplayName); err != nil {
			return nil, err
		}
		member.Role = tenant.MemberRole(role)
		members = append(members, member)
	}
	return members, rows.Err()
}

// AddTeamMember adds a member, or updates the role of one already there.
func (r *PostgresTenantRegistry) AddTeamMember(ctx context.Context, teamID, tenantID string, role tenant.MemberRole) error {
	if _, err := r.requireTeam(ctx, teamID); err != nil {
		return err
	}
	if !role.Valid() {
		return tenant.ErrInvalidTransition
	}
	// One statement inserts or updates only when the member is an active tenant;
	// the RETURNING clause then reports whether anything was written, which is
	// how a missing or inactive member surfaces without a second round trip.
	var written string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO `+r.qualified("team_member")+` (team_tenant_id, tenant_id, role)
		 SELECT $1, $2, $3
		 WHERE EXISTS (SELECT 1 FROM `+r.qualified("tenant")+` WHERE tenant_id = $2 AND status = 'active')
		 ON CONFLICT (team_tenant_id, tenant_id) DO UPDATE SET role = EXCLUDED.role
		 RETURNING tenant_id`,
		teamID, tenantID, string(role)).Scan(&written)
	if errors.Is(err, sql.ErrNoRows) {
		return r.memberRefusal(ctx, teamID, tenantID)
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			// The team vanished between the check and the insert.
			return tenant.ErrNotFound
		}
		return err
	}
	return nil
}

// RemoveTeamMember removes a member and their project membership with them.
func (r *PostgresTenantRegistry) RemoveTeamMember(ctx context.Context, teamID, tenantID string) error {
	if _, err := r.GetTeam(ctx, teamID); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx,
		"DELETE FROM "+r.qualified("team_member")+" WHERE team_tenant_id = $1 AND tenant_id = $2", teamID, tenantID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tenant.ErrNotFound
	}
	return nil
}

// ListTeamProjects returns a team's projects in creation order.
func (r *PostgresTenantRegistry) ListTeamProjects(ctx context.Context, teamID string) ([]tenant.TeamProject, error) {
	if _, err := r.GetTeam(ctx, teamID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+r.projectSelect()+" FROM "+r.qualified("team_project")+" p"+
			" WHERE p.team_tenant_id = $1 ORDER BY p.created_at, p.project_id", teamID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	projects := make([]tenant.TeamProject, 0)
	for rows.Next() {
		project, err := scanTeamProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// projectSelect renders the project view's column list.
func (r *PostgresTenantRegistry) projectSelect() string {
	return fmt.Sprintf(teamProjectColumns, r.qualified("team_project_member"))
}

// GetTeamProject returns one project of a team.
func (r *PostgresTenantRegistry) GetTeamProject(ctx context.Context, teamID, projectID string) (tenant.TeamProject, error) {
	if _, err := r.GetTeam(ctx, teamID); err != nil {
		return tenant.TeamProject{}, err
	}
	project, err := scanTeamProject(r.db.QueryRowContext(ctx,
		"SELECT "+r.projectSelect()+" FROM "+r.qualified("team_project")+" p"+
			" WHERE p.team_tenant_id = $1 AND p.project_id = $2", teamID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.TeamProject{}, tenant.ErrNotFound
	}
	return project, err
}

// CreateTeamProject records a project.
func (r *PostgresTenantRegistry) CreateTeamProject(ctx context.Context, project tenant.TeamProject) error {
	if err := tenant.ValidateProjectID(project.ProjectID); err != nil {
		return err
	}
	if err := tenant.ValidateProjectName(project.Name); err != nil {
		return err
	}
	if project.BankID == "" {
		project.BankID = tenant.ProjectBankID(project.TeamID, project.ProjectID)
	}
	if _, err := r.GetTeam(ctx, project.TeamID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO "+r.qualified("team_project")+" (team_tenant_id, project_id, name, bank_id) VALUES ($1,$2,$3,$4)",
		project.TeamID, project.ProjectID, project.Name, project.BankID)
	if isUniqueViolation(err) {
		return tenant.ErrExists
	}
	return err
}

// DeleteTeamProject removes a project and its membership rows.
func (r *PostgresTenantRegistry) DeleteTeamProject(ctx context.Context, teamID, projectID string) error {
	if _, err := r.GetTeam(ctx, teamID); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx,
		"DELETE FROM "+r.qualified("team_project")+" WHERE team_tenant_id = $1 AND project_id = $2", teamID, projectID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tenant.ErrNotFound
	}
	return nil
}

// ListProjectMembers returns the members taking part in a project.
func (r *PostgresTenantRegistry) ListProjectMembers(ctx context.Context, teamID, projectID string) ([]tenant.ProjectMember, error) {
	if _, err := r.GetTeamProject(ctx, teamID, projectID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+projectMemberColumns+" FROM "+r.qualified("team_project_member")+" pm"+
			" LEFT JOIN "+r.qualified("tenant")+" t ON t.tenant_id = pm.tenant_id"+
			" WHERE pm.team_tenant_id = $1 AND pm.project_id = $2 ORDER BY pm.added_at, pm.tenant_id",
		teamID, projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := make([]tenant.ProjectMember, 0)
	for rows.Next() {
		var member tenant.ProjectMember
		if err := rows.Scan(&member.TeamID, &member.ProjectID, &member.TenantID, &member.AddedAt, &member.DisplayName); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// AddProjectMember adds a team member to a project.
func (r *PostgresTenantRegistry) AddProjectMember(ctx context.Context, teamID, projectID, tenantID string) error {
	if _, err := r.GetTeamProject(ctx, teamID, projectID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO "+r.qualified("team_project_member")+" (team_tenant_id, project_id, tenant_id) VALUES ($1,$2,$3)"+
			" ON CONFLICT DO NOTHING", teamID, projectID, tenantID)
	if err != nil {
		if isForeignKeyViolation(err) {
			// The member is not on the team: the foreign key onto team_member is
			// what refuses it, and the caller reports a conflict.
			return tenant.ErrInvalidTransition
		}
		return err
	}
	return nil
}

// RemoveProjectMember removes a member from a project.
func (r *PostgresTenantRegistry) RemoveProjectMember(ctx context.Context, teamID, projectID, tenantID string) error {
	if _, err := r.GetTeam(ctx, teamID); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx,
		"DELETE FROM "+r.qualified("team_project_member")+
			" WHERE team_tenant_id = $1 AND project_id = $2 AND tenant_id = $3", teamID, projectID, tenantID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tenant.ErrNotFound
	}
	return nil
}

// ResolveProjectForMember returns the active project a member takes part in.
func (r *PostgresTenantRegistry) ResolveProjectForMember(ctx context.Context, tenantID, projectID string) (tenant.TeamProject, error) {
	project, err := scanTeamProject(r.db.QueryRowContext(ctx,
		"SELECT "+r.projectSelect()+" FROM "+r.qualified("team_project")+" p"+
			" JOIN "+r.qualified("team_project_member")+" pm"+
			"   ON pm.team_tenant_id = p.team_tenant_id AND pm.project_id = p.project_id"+
			" JOIN "+r.qualified("tenant")+" t ON t.tenant_id = p.team_tenant_id"+
			" WHERE pm.tenant_id = $1 AND p.project_id = $2"+
			"   AND t.kind = 'team' AND t.status = 'active' AND t.deleted_at IS NULL"+
			" ORDER BY p.team_tenant_id, p.project_id LIMIT 1",
		tenantID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.TeamProject{}, tenant.ErrNotFound
	}
	return project, err
}

// memberRefusal reports why a member insert wrote nothing.
func (r *PostgresTenantRegistry) memberRefusal(ctx context.Context, teamID, tenantID string) error {
	record, err := r.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if record.Status != tenant.StatusActive {
		return tenant.ErrInvalidTransition
	}
	// The caller validated the team before inserting, so a missing row here means
	// the member check did not match: the tenant vanished between the two reads.
	return tenant.ErrNotFound
}

// scanTeam reads the team view's column list.
func scanTeam(row rowScanner) (tenant.Team, error) {
	var team tenant.Team
	var status, kind string
	var config []byte
	var deleted sql.NullTime
	if err := row.Scan(&team.ID, &team.DisplayName, &status, &kind, &config,
		&team.CreatedAt, &team.UpdatedAt, &deleted, &team.MemberCount, &team.ProjectCount); err != nil {
		return tenant.Team{}, err
	}
	team.Status = tenant.Status(status)
	team.Kind = tenant.Kind(kind)
	if len(config) > 0 {
		if err := json.Unmarshal(config, &team.Config); err != nil {
			return tenant.Team{}, fmt.Errorf("decode tenant config: %w", err)
		}
	}
	if team.Config == nil {
		team.Config = map[string]any{}
	}
	team.DeletedAt = timePointer(deleted)
	return team, nil
}

// scanTeamProject reads the project view's column list.
func scanTeamProject(row rowScanner) (tenant.TeamProject, error) {
	var project tenant.TeamProject
	if err := row.Scan(&project.TeamID, &project.ProjectID, &project.Name, &project.BankID,
		&project.CreatedAt, &project.MemberCount); err != nil {
		return tenant.TeamProject{}, err
	}
	return project, nil
}

package tenant

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MemberRole is a member's standing inside a team.
type MemberRole string

const (
	// RoleOwner is a team owner. Ownership is advisory today: it is stored and
	// displayed, and no route grants it extra privileges.
	RoleOwner MemberRole = "owner"
	// RoleMember is an ordinary team member.
	RoleMember MemberRole = "member"
)

// Valid reports whether the role is one the registry stores.
func (r MemberRole) Valid() bool { return r == RoleOwner || r == RoleMember }

// projectIDPattern is the slug shape of a project identifier. It is the same
// shape a tenant id has, so it can name things without quoting.
var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// projectNamePattern is the shape of a project name, which is the suffix of
// every bank id the project owns.
//
// The colon is excluded on purpose: bank ids are split on the first "::" into
// tenant and project name, so a name carrying one would make its own banks
// unresolvable.
var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateProjectID reports whether id can name a project.
func ValidateProjectID(id string) error {
	if !projectIDPattern.MatchString(id) {
		return fmt.Errorf("%w: project_id %q must match %s", ErrInvalidProject, id, projectIDPattern)
	}
	return nil
}

// ValidateProjectName reports whether name can be a project's name. Names are
// case-sensitive and immutable: renaming is creating a new project.
func ValidateProjectName(name string) error {
	if strings.Contains(name, ":") {
		return fmt.Errorf("%w: project name %q must not contain ':'", ErrInvalidProject, name)
	}
	if !projectNamePattern.MatchString(name) {
		return fmt.Errorf("%w: project name %q must match %s", ErrInvalidProject, name, projectNamePattern)
	}
	return nil
}

// ProjectBankID is the id of the bank a project owns inside its team's schema:
// "<team>::<name>". It is computed once, when the project is created, and never
// recomputed, because both halves are immutable.
func ProjectBankID(teamID, name string) string {
	return teamID + "::" + name
}

// TeamMember is one row of team_member.
type TeamMember struct {
	TeamID   string
	TenantID string
	Role     MemberRole
	// DisplayName is the member tenant's display name, filled by list calls so
	// the console does not have to join the tenant list itself.
	DisplayName string
	JoinedAt    time.Time
}

// ProjectMember is one row of team_project_member.
type ProjectMember struct {
	TeamID      string
	ProjectID   string
	TenantID    string
	DisplayName string
	AddedAt     time.Time
}

// Team is a team tenant with the counts the console shows.
type Team struct {
	Record
	// MemberCount and ProjectCount are computed by the list and get calls.
	MemberCount  int
	ProjectCount int
}

// TeamProject is one row of team_project.
type TeamProject struct {
	TeamID    string
	ProjectID string
	Name      string
	// BankID is "<team>::<name>", fixed at creation.
	BankID string
	// MemberCount is filled by list and get calls.
	MemberCount int
	CreatedAt   time.Time
}

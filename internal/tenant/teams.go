package tenant

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
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

// projectNameMaxRunes bounds a project's display name. Unlike the id, the name is
// free-form: it is what a person reads, so it may be written in any script.
const projectNameMaxRunes = 128

// ValidateProjectID reports whether id can name a project.
func ValidateProjectID(id string) error {
	if !projectIDPattern.MatchString(id) {
		return fmt.Errorf("%w: project_id %q must match %s", ErrInvalidProject, id, projectIDPattern)
	}
	return nil
}

// ValidateProjectName reports whether name can be a project's display name.
//
// The name is presentation only — nothing addresses anything by it, and every
// lookup goes through the project id — so it accepts any text a person might type,
// Chinese included. Only two things are refused: an empty name (the console would
// show a blank row) and one that is too long to render.
func ValidateProjectName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: project name must not be empty", ErrInvalidProject)
	}
	if utf8.RuneCountInString(trimmed) > projectNameMaxRunes {
		return fmt.Errorf("%w: project name must be at most %d characters", ErrInvalidProject, projectNameMaxRunes)
	}
	return nil
}

// ProjectBankID is the id of the bank a project owns inside a tenant's schema:
// "<tenant>::<project id>".
//
// The suffix is the project *id*, not its display name: the id is the project's
// only identity, so it is what every bank id is built from. The name is a label a
// person reads and may change meaning freely without moving any data.
//
// It is computed once, when the project is created, and never recomputed, because
// both halves are immutable.
func ProjectBankID(tenantID, projectID string) string {
	return tenantID + "::" + projectID
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

// DroppedMergeKey marks a source the merge model judged redundant.
//
// Its disappearance cannot invalidate the team content, because nothing in that
// content was made from it, so delete detection skips these rows.
const DroppedMergeKey = "__dropped__"

// MergeState is one row of merge_state: where one member's contribution to one
// project stands.
type MergeState struct {
	TeamID    string
	ProjectID string
	TenantID  string
	// CursorUpdatedAt and CursorUnitID are the last (updated_at, id) pair
	// consumed from this member's bank. The zero row reads from the beginning.
	CursorUpdatedAt time.Time
	CursorUnitID    string
	// LastMergedAt is nil until the pair's first successful round.
	LastMergedAt *time.Time
	// LastError is the failure of the most recent round, empty after a success.
	LastError string
	// FailCount is consecutive failed rounds; the circuit breaker reads it.
	FailCount int
	// MergingSince is stamped while a round's model call is in flight. A stamp
	// older than the round deadline is a crashed round and may be taken over.
	MergingSince *time.Time
	// RebuildRequested asks the next round to ignore the cursor.
	RebuildRequested bool
}

// MergeSourceUnit is one row of merge_source_unit: one member fact, and the team
// fact it supports.
type MergeSourceUnit struct {
	TeamID       string
	ProjectID    string
	TenantID     string
	SourceUnitID string
	// MergeKey is the supported team fact's key, or DroppedMergeKey.
	MergeKey   string
	ConsumedAt time.Time
}

// MergeLockName is the advisory-lock name of one project's merge round.
//
// The two halves are passed as separate hashtext arguments so no single string
// can collide with another project's by concatenation.
func MergeLockName(teamID, projectID string) (string, string) {
	return "wenyuan:project-merge:" + teamID, projectID
}

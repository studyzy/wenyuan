package adminapi

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TeamConfig is what the management plane's team routes need beside the tenant
// registry.
//
// Teams is optional: a deployment that does not configure it answers every
// /admin/v1/teams route with 501, which is what a single-tenant process wants.
// Banks is optional too, but two routes need it — creating a project creates the
// bank it owns, and deleting one deletes every bank under its name — so a
// registry without a bank handle still serves the other team routes.
type TeamConfig struct {
	// Teams stores teams, their members and their projects.
	Teams storage.TeamRegistry
	// Banks is the management plane's handle on a team's banks: creating the
	// bank a project owns, listing the banks under a project's name and
	// deleting them.
	Banks BankAdmin
	// Merges, when non-nil, lets the plane keep a project's merge ledger in step
	// with its membership: leaving a project clears that member's ledger rows,
	// and a purge also asks for a rebuild. A nil value keeps membership changes
	// registry-only, which is what a deployment that cannot merge gets.
	Merges MergeAdmin
	// MembershipChanged, when non-nil, is told that one tenant's project
	// membership changed, so the read path drops the answer it caches for that
	// tenant instead of serving it until it expires.
	//
	// It is separate from Merges on purpose: whether a read is served from a
	// project's bank is a read-path question, and it must be answerable correctly
	// even where merging is switched off — otherwise a removal keeps a former
	// member reading the project for as long as the cache holds.
	MembershipChanged func(tenantID string)
}

// MergeAdmin is the management plane's handle on a project's merge state.
//
// It is narrow like BankAdmin: the plane never merges anything itself, it only
// tells the merge job what changed.
type MergeAdmin interface {
	// ClearMemberProject removes one member's ledger rows and state for one
	// project, so a member taken out of a project stops being checked as a
	// source.
	ClearMemberProject(ctx context.Context, teamID, projectID, tenantID string) error
	// RequestRebuild asks the next merge round to ignore the cursors.
	RequestRebuild(ctx context.Context, teamID, projectID string) error
	// TriggerMerge asks for one project's merge round to run now rather than at
	// the next scheduled tick.
	//
	// It returns immediately: the round is run in the background, because it
	// spends a model call and the caller is a management request that has
	// nothing to do while it runs. A round already in flight for the project
	// needs no second one — its inputs are re-read each round — so this is not
	// an error there.
	TriggerMerge(ctx context.Context, teamID, projectID string)
	// MergeStatus reports a project's merge state.
	MergeStatus(ctx context.Context, teamID, projectID string) (storage.MergeStatus, error)
	// ResetMergeState clears a halted project's circuit breaker.
	ResetMergeState(ctx context.Context, teamID, projectID string) error
}

// BankAdmin is the management plane's handle on a team's memory banks.
//
// It is deliberately narrow: a project owns the bank "<team>::<project name>"
// and the banks whose ids start with that prefix, so the plane needs to create
// the first, enumerate them by prefix, and delete each one.
type BankAdmin interface {
	// CreateBank creates bankID with name as its display name, reporting
	// ErrBankExists when the bank is already there.
	CreateBank(ctx context.Context, schema, bankID, name string) error
	// ListBanks returns the ids of the banks in schema whose id starts with
	// prefix, in id order.
	ListBanks(ctx context.Context, schema, prefix string) ([]string, error)
	// DeleteBank removes one bank and its content, reporting what it removed.
	DeleteBank(ctx context.Context, schema, bankID string) (int, error)
}

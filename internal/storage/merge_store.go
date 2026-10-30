package storage

import (
	"context"
	"errors"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The project merge store: the cross-schema access one merge round needs.
//
// It is a separate interface rather than a group of Store methods because one
// round touches three schemas — it reads member schemas, writes the team schema,
// and records the ledger in the admin schema — while a Store is bound to one.
// Which *tenant* schema a statement uses therefore always comes from the
// context, exactly as it does for Store; only the admin schema is fixed, because
// there is only one.
//
// There is deliberately no in-memory implementation. The risks this store
// carries — cross-schema transactions, advisory locks, foreign-key cascades —
// have no in-memory analogue, so a double would only let the merge job's tests
// pass against a semantics nobody runs. The merge engine's own rules are pure
// functions in internal/projectmerge and are tested there; everything behind this
// interface is verified against PostgreSQL.

// ErrMergeClaimLost reports a round whose claim was taken over, so its result is
// stale. It is not a failure of the project: another process is doing the work,
// and that one's result is the one that counts.
var ErrMergeClaimLost = errors.New("merge claim was taken over")

// MergeProjectMember is one member of a project, with the state of that member's
// contribution.
type MergeProjectMember struct {
	TenantID string
	// ProjectID is the project's identity, which is the second half of the bank a
	// member's contribution lives in ("<tenant>::<project id>"). It is the id,
	// not the display name: the name is a label and addresses nothing.
	ProjectID string
	// Schema is the member's tenant schema, which is where their bank lives.
	Schema string
	// State is the member's ledger row. It is absent when the pair has never
	// merged, which reads the member's bank from the beginning.
	State tenant.MergeState
	// HasLedger reports whether a merge_state row exists.
	HasLedger bool
}

// MergeProject is one project a round would consider.
type MergeProject struct {
	TeamID    string
	TeamName  string
	ProjectID string
	Name      string
	// BankID is the team's bank, "<team>::<name>".
	BankID string
	// TeamSchema is the team tenant's schema.
	TeamSchema string
	// Members are the project's members in a stable order, ordered so the pair
	// merged longest ago comes first: an uneven writer otherwise keeps the rest
	// from ever being reached.
	Members []MergeProjectMember
}

// MergeFact is one fact read from a member's bank.
type MergeFact struct {
	TenantID string
	UnitID   string
	Text     string
	// UpdatedAt is the fact's stored timestamp, which is the cursor's first part.
	UpdatedAt time.Time
}

// MergeCursor is the incremental position of one (project, member) pair.
type MergeCursor struct {
	UpdatedAt time.Time
	UnitID    string
}

// Before reports whether this cursor reads from the beginning of the bank.
func (c MergeCursor) Before() bool { return c.UpdatedAt.IsZero() }

// CursorOf is the cursor a member's state stands at.
func CursorOf(state tenant.MergeState) MergeCursor {
	if state.CursorUpdatedAt.IsZero() {
		return MergeCursor{}
	}
	return MergeCursor{UpdatedAt: state.CursorUpdatedAt, UnitID: state.CursorUnitID}
}

// TeamFactRow is one existing team fact.
type TeamFactRow struct {
	ID       string
	Text     string
	MergeKey string
}

// MergeStanzaInput is what a round reads while it holds the lock.
type MergeStanzaInput struct {
	Project MergeProject
	// Previous is the team bank's current managed facts, in a stable order.
	Previous []TeamFactRow
	// Members carries each member's job inputs, in the project's member order.
	Members []MergeMemberInput
}

// MergeMemberInput is one member's state for this round.
type MergeMemberInput struct {
	Member MergeProjectMember
	// BankExists reports whether the member's personal bank row exists. A member
	// with no bank yet contributes nothing and is skipped rather than failing the
	// round.
	BankExists bool
	// Fresh is the member's new world facts after the cursor, bounded by the
	// round's batch.
	Fresh []MergeFact
	// Deleted is true when a source this member contributed and that still
	// supports team content no longer exists in their bank.
	Deleted bool
}

// MergeStanza lets the job read what a round needs while the lock is held.
//
// It exposes reads only. Writes have one entry point, FinishMergeStanza, so the
// model call that sits between them cannot accidentally run inside a
// transaction.
type MergeStanza interface {
	Input() MergeStanzaInput
}

// FinishMergeRequest is one round's output, written in a single transaction.
type FinishMergeRequest struct {
	TeamID    string
	ProjectID string
	// TeamSchema is the schema written; it must match the one BeginMergeStanza
	// read from.
	TeamSchema string
	// BankID is the team bank the facts land in.
	BankID string
	// Members are the tenants the round consumed, whose cursors it advances.
	Members []string
	// Stamp is the value BeginMergeStanza wrote to merging_since. The write only
	// lands while it still matches, which is what keeps a round that another
	// process took over from writing its stale result.
	Stamp time.Time
	// Replace carries the facts to keep, remove and insert, plus the ledger rows
	// to record. When Rebuild is set the ledger for the round's members is
	// replaced wholesale rather than appended.
	Replace MergeReplaceResult
	// Cursors are the per-member positions to advance to, keyed by tenant id.
	// A member absent from the map keeps its cursor, which is what a member whose
	// input this round did not reach must do.
	Cursors map[string]MergeCursor
	// Rebuild clears rebuild_requested when the round was one.
	Rebuild bool
}

// MergeReplaceResult is the fact-level change a round produces.
type MergeReplaceResult struct {
	Keep   []TeamFactRow
	Remove []TeamFactRow
	Insert []TeamFactRow
	// Sources records, per member tenant id, each consumed source unit and the
	// team fact it supports — or tenant.DroppedMergeKey when it supports none.
	Sources map[string][]MergeSourceRow
	// RebuildMembers are the tenants whose ledger rows this round replaces
	// wholesale. Empty on an incremental round.
	RebuildMembers []string
}

// MergeSourceRow is one ledger row to record.
type MergeSourceRow struct {
	UnitID   string
	MergeKey string
}

// MergeStateFailure records why a round produced nothing.
type MergeStateFailure struct {
	// Error is the message a console shows.
	Error string
	// Counting tells whether this failure advances the circuit breaker. A round
	// skipped because another process holds the project, or because there was
	// nothing to do, is not a failure and must not trip it.
	Counting bool
}

// MergeStatus reports one project's merge state.
type MergeStatus struct {
	ProjectID string
	BankID    string
	// Merged reports whether any round has ever completed.
	Merged       bool
	LastMergedAt *time.Time
	LastError    string
	// Halted reports the circuit breaker being open.
	Halted    bool
	FailCount int
	// PendingMembers counts members whose state is not yet merged this round's
	// worth.
	PendingMembers int
	// TeamFacts counts the team bank's managed facts.
	TeamFacts int
	// UnmanagedFacts counts world facts in the team bank that no merge round
	// wrote, which the next round will replace.
	UnmanagedFacts int
}

// MergeStore is the cross-schema store the project merge job needs.
type MergeStore interface {
	// ListMergeProjects returns every active team project that has members, with
	// each member's state.
	ListMergeProjects(ctx context.Context) ([]MergeProject, error)

	// BeginMergeStanza acquires the project's round claim and returns what a
	// round must read.
	//
	// It reports ok=false when another process holds the project: either the
	// advisory lock is taken or merging_since is a live stamp. A claim older than
	// staleAfter is treated as a crashed round and taken over. The reads run in
	// one transaction with the claim, so no separate round can start between them.
	BeginMergeStanza(ctx context.Context, teamID, projectID string, batch int, staleAfter time.Duration,
		fn func(stanza MergeStanza) error) (ok bool, err error)

	// FinishMergeStanza writes one round's output and releases the claim.
	FinishMergeStanza(ctx context.Context, req FinishMergeRequest) error

	// AbandonMergeStanza records a round that produced no output.
	AbandonMergeStanza(ctx context.Context, teamID, projectID string, state MergeStateFailure) error

	// ClearMemberProject removes one member's ledger rows and state for one
	// project.
	//
	// It is what makes an un-share operation converge: the ledger's foreign keys
	// hang off team_member and team_project, not team_project_member, so a member
	// taken out of one project leaves their rows behind — and a row whose member
	// no longer contributes can never match, which would rebuild the project on
	// every round forever.
	ClearMemberProject(ctx context.Context, teamID, projectID, tenantID string) error

	// RequestRebuild asks the next round to ignore the cursors.
	RequestRebuild(ctx context.Context, teamID, projectID string) error

	// ResetMergeState clears the circuit breaker, so an operator can resume a
	// halted project.
	ResetMergeState(ctx context.Context, teamID, projectID string) error

	// MergeStatus reports the admin-side state a console shows.
	MergeStatus(ctx context.Context, teamID, projectID string) (MergeStatus, error)
}

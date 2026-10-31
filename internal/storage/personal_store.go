package storage

import (
	"context"
	"errors"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The personal aggregate store: the bank access one rollup round needs.
//
// It is a separate interface rather than a group of Store methods for the same
// reason MergeStore is: one round reads every bank of a tenant, writes one bank
// of that tenant, and records a ledger in the admin schema, while a Store is
// bound to one schema. Which schema a statement uses therefore always comes from
// the context, exactly as it does for Store.
//
// There is deliberately no in-memory implementation. The risks behind this
// interface — cross-schema transactions, advisory locks, foreign-key cascades —
// have no in-memory analogue, so a double would only let the job's tests pass
// against a semantics nobody runs. The rollup's own rules are pure functions in
// internal/personalrollup and are tested there; everything behind this interface
// is verified against PostgreSQL.

// ErrPersonalAggregateIsTeam reports a team tenant asked for an aggregate of its
// own. A team is not a person: its banks are merged into a project's team bank,
// and its members' aggregates live in the members' own schemas.
var ErrPersonalAggregateIsTeam = errors.New("a team tenant has no aggregate of its own")

// ErrPersonalClaimLost reports a round whose claim was taken over, so its result
// is stale. It is not a failure of the tenant: another process is doing the work,
// and that one's result is the one that counts.
var ErrPersonalClaimLost = errors.New("personal aggregate claim was taken over")

// DefaultPersonalFailThreshold is how many consecutive failed rounds halt one
// tenant's rollup before an operator resets it.
const DefaultPersonalFailThreshold = 5

// PersonalSourceBank is one bank a round reads facts from.
type PersonalSourceBank struct {
	// BankID is the source bank inside the tenant's schema.
	BankID string
	// Cursor is the (updated_at, id) watermark the next facts must be after.
	Cursor MergeCursor
	// HasLedger reports whether a personal_bank_state row exists, which a bank
	// with no history does not.
	HasLedger bool
	// LastMergedAt is when this bank last contributed to a completed round,
	// nil until it has.
	LastMergedAt *time.Time
}

// PersonalSourceFact is one fact a source bank contributed to a round.
type PersonalSourceFact struct {
	BankID    string
	UnitID    string
	Text      string
	UpdatedAt time.Time
}

// PersonalPreviousFact is one aggregate fact a round starts from.
type PersonalPreviousFact struct {
	ID       string
	Text     string
	MergeKey string
}

// PersonalRoundInput is what a round reads while it holds the claim.
type PersonalRoundInput struct {
	TenantID string
	Schema   string
	// BankID is the aggregate bank "<tenant>::myself".
	BankID string
	// Previous is the aggregate bank's current world facts, in a stable order.
	// It is every fact in that bank: the aggregate is the rollup's to write.
	Previous []PersonalPreviousFact
	// Sources are the banks that changed, ordered so the bank that contributed
	// longest ago comes first.
	Sources []PersonalSourceBank
	// Facts carries each changing bank's new facts, bounded by the round's batch.
	Facts []PersonalSourceFact
	// SourceDeleted reports that a fact a ledger row still names is gone, so
	// the aggregate must be rebuilt rather than extended.
	SourceDeleted bool
	// RebuildRequested is an operator asking for a full recomputation.
	RebuildRequested bool
	// Halted reports the circuit breaker being open. A round must not run.
	Halted bool
	// Topics is the tenant's vocabulary as the previous round left it, which the
	// prompt offers so a fact's wording keeps reusing the same topic.
	Topics []PersonalTopic
	// UnmanagedFacts counts the aggregate bank's rows no round wrote. A round
	// replaces them, and reports the count so an operator can see them go.
	UnmanagedFacts int
}

// BankCursorOf is the cursor a source bank's state stands at.
func BankCursorOf(bank PersonalSourceBank) MergeCursor {
	if bank.Cursor.Before() {
		return MergeCursor{}
	}
	return bank.Cursor
}

// PersonalSourceRow is one ledger row a round records.
type PersonalSourceRow struct {
	UnitID   string
	MergeKey string
	BankID   string
}

// FinishPersonalRequest is one round's output, written in a single transaction.
type FinishPersonalRequest struct {
	TenantID string
	Schema   string
	BankID   string
	// Stamp is the value BeginPersonalRound wrote to merging_since. The write
	// only lands while it still matches, which is what keeps a round that another
	// process took over from writing its stale result.
	Stamp time.Time
	// Remove and Insert are the facts to delete and add. A fact whose merge_key
	// survives keeps its row: it is present in neither list.
	Remove []PersonalPreviousFact
	Insert []PersonalPreviousFact
	// Sources are the ledger rows to record. When Rebuild is set they replace
	// the tenant's ledger wholesale.
	Sources []PersonalSourceRow
	// Cursors are the per-bank watermarks to advance to, keyed by bank id.
	Cursors map[string]MergeCursor
	// Rebuild reports the round recomputed everything and replaced the ledger.
	Rebuild bool
	// Topics are the aggregate facts' topics, recorded for the directory. They
	// are keyed by aggregate merge key.
	Topics []PersonalTopic
	// FactBanks maps an aggregate merge key to the source banks it spans, which
	// is what the directory aggregates into personal_link rows.
	FactBanks map[string][]string
	// UnmanagedFacts counts the rows in the aggregate bank a round did not
	// write, which it is about to replace. Recorded so an operator sees them.
	UnmanagedFacts int
}

// PersonalFailure records why a round produced nothing.
type PersonalFailure struct {
	// Error is the message a console shows.
	Error string
	// Counting tells whether this failure advances the circuit breaker. A round
	// skipped because another process holds the tenant, or because there was
	// nothing to do, is not a failure and must not trip it.
	Counting bool
}

// PersonalStatus reports one tenant's aggregate state.
type PersonalStatus struct {
	BankID string
	// Kind is the tenant's kind; a team reports "team" and no aggregate.
	Kind string
	// Aggregate reports whether the tenant takes an aggregate at all.
	Aggregate bool
	// SharedSchema reports that the deployment resolves every credential to one
	// schema, so "this person's aggregate" is really the deployment's.
	SharedSchema bool
	// LastRoundAt, LastError, FailCount and Halted describe the last round.
	LastRoundAt *time.Time
	LastError   string
	FailCount   int
	Halted      bool
	// RebuildRequested reports an operator asking for a full recomputation.
	RebuildRequested bool
	// AdoptedPreexisting reports the aggregate bank held content before the
	// rollup first wrote it.
	AdoptedPreexisting bool
	// SourceBanks counts the banks a round would read.
	SourceBanks int
	// PendingBanks counts the source banks whose watermark a round has not yet
	// reached.
	PendingBanks int
	// AggregateFacts counts the rows the rollup wrote; UserFacts counts the rows
	// in the aggregate bank it did not write (a direct retain into it).
	AggregateFacts int
	UserFacts      int
	// Links and Topics count the directory's edges and vocabulary entries.
	Links  int
	Topics int
}

// PersonalTopic is one (topic, bank) vocabulary entry.
type PersonalTopic struct {
	// Name is the normalized topic.
	Name   string
	BankID string
	// MergeKey is the aggregate fact the entry came from, which typing a round
	// writes into that fact's metadata.
	MergeKey string
}

// PersonalLink is one directory edge.
type PersonalLink struct {
	BankID string
	Weight int
}

// PersonalDirectory is what a tenant's navigation index holds.
type PersonalDirectory struct {
	Links  []PersonalLink
	Topics []PersonalTopic
}

// PersonalStore is the cross-schema store the personal rollup job needs.
type PersonalStore interface {
	// ListRollupTenants returns the active personal tenants an aggregate should
	// cover, in id order.
	ListRollupTenants(ctx context.Context) ([]tenant.Tenant, error)

	// BeginPersonalRound acquires the tenant's round claim and returns what a
	// round must read.
	//
	// It reports ok=false when another process holds the tenant (the advisory
	// lock is taken or merging_since is a live stamp) or when the tenant's
	// circuit breaker is open. A claim older than staleAfter is treated as a
	// crashed round and taken over.
	BeginPersonalRound(ctx context.Context, tenantID string, batch int, staleAfter time.Duration,
		fn func(input PersonalRoundInput, claim time.Time) error) (ok bool, err error)

	// FinishPersonalRound writes one round's output and releases the claim.
	FinishPersonalRound(ctx context.Context, req FinishPersonalRequest) error

	// AbandonPersonalRound records a round that produced no output.
	AbandonPersonalRound(ctx context.Context, tenantID string, failure PersonalFailure) error

	// RequestRebuild asks the next round to ignore the watermarks.
	RequestRebuild(ctx context.Context, tenantID string) error

	// ResetPersonalState clears the circuit breaker.
	ResetPersonalState(ctx context.Context, tenantID string) error

	// PersonalStatus reports the admin-side state a console shows.
	PersonalStatus(ctx context.Context, tenantID string, sharedSchema bool) (PersonalStatus, error)

	// PersonalDirectory returns the navigation index a round projected.
	PersonalDirectory(ctx context.Context, tenantID string) (PersonalDirectory, error)
}

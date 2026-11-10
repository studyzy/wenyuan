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
	// RebuildRequested is State.RebuildRequested copied before the round claims
	// the project: claiming rewrites the state row, and the read that follows
	// must still know a rebuild was asked for.
	RebuildRequested bool
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
	// FactType is the member fact's own type, so the round writes the team fact
	// under the same kind.
	FactType MemoryUnitType
	// Tags are the member fact's tags, carried so the team bank keeps the same
	// vocabulary: tags drive recall's tag filter and the fuzzy tag resolution, so
	// a fact whose tags are dropped is a fact that no longer answers a tag query.
	Tags []string
	// Context is the passage the fact was extracted from, carried verbatim. A
	// team fact is a copy of what a member wrote, so dropping its context would
	// change what the fact reads like.
	Context string
	// EventDate, OccurredStart, OccurredEnd and MentionedAt are the fact's
	// temporal bounds, carried verbatim. The temporal recall arm matches on
	// them, the graph derives its time edges from EventDate, and the listing's
	// time filters read them, so a team fact without them is a fact that can
	// only be reached by keyword and similarity.
	EventDate     *time.Time
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	// ObservationScopes is the fact's raw observation_scopes request. It is part
	// of a fact's content in the transfer format, so a copy carries it.
	ObservationScopes []byte
	// TextSignals is the fact's text signals, which the keyword index is built
	// from alongside the text and the context.
	TextSignals *string
	// CreatedAt, UpdatedAt and EditedAt are the fact's own stamps, carried so a
	// team bank's ordering and timeline describe when the member's facts were
	// written rather than when a round happened to copy them.
	CreatedAt *time.Time
	UpdatedAt time.Time
	EditedAt  *time.Time
	// SourceMemoryIDs is the fact's evidence, carried when it has any.
	SourceMemoryIDs []string
	// Embedding is the member fact's own vector, copied rather than recomputed.
	// Retain embeds text augmented with the fact's date and its resolved entity
	// names, and a team fact carries no entity postings, so a vector computed
	// here could never match the member's. One deployment is one embedding
	// scheme, which is what makes the copy comparable.
	Embedding []float32
	// EntityNames are the member fact's entity names, carried so the team bank
	// rebuilds its own entities from them. Ids are not carried: an entity is an
	// identity within one bank, and the round resolves each name to the team
	// bank's own row. A team fact without postings answers no entity-mediated
	// graph expansion, which is half of recall's graph arm.
	EntityNames []string
}

// MergeLink is one link between two facts a round carries.
//
// Its endpoints are identified by merge key rather than by unit id: the team
// fact is a different row, so the write resolves both keys to the team bank's
// own units. A link whose other endpoint has not reached the team bank yet is
// carried when that endpoint does, which is what keeps links across batch
// boundaries from being lost.
type MergeLink struct {
	FromMergeKey string
	ToMergeKey   string
	LinkType     MemoryLinkType
	// Weight is the link's strength, copied unchanged.
	Weight float64
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

// TeamFactRow is one team fact: an existing one being read back, or one a round
// writes.
type TeamFactRow struct {
	ID       string
	Text     string
	MergeKey string
	// SourceUnitID and SourceTenant are the member fact this row mirrors. The
	// source unit is the row's identity: an edited fact keeps its unit id, so a
	// round replaces its row rather than adding a second one for the new text.
	SourceUnitID string
	SourceTenant string
	// FactType is the row's stored fact type. A team fact keeps the kind of the
	// member fact it was made from.
	FactType MemoryUnitType
	// Tags are the row's tags. A team fact keeps the member fact's tags.
	Tags []string
	// Context, the four temporal bounds and ObservationScopes are the member
	// fact's own values, carried verbatim; the round's diff compares them, which
	// is what decides whether an existing row still matches its source.
	Context           string
	EventDate         *time.Time
	OccurredStart     *time.Time
	OccurredEnd       *time.Time
	MentionedAt       *time.Time
	ObservationScopes []byte
	// TextSignals is the member fact's text signals, written with the row.
	TextSignals *string
	// CreatedAt, UpdatedAt and EditedAt are the member fact's stamps.
	CreatedAt *time.Time
	UpdatedAt time.Time
	EditedAt  *time.Time
	// SourceMemoryIDs is the member fact's evidence, carried when it has any.
	SourceMemoryIDs []string
	// Embedding is the member fact's vector, written unchanged.
	Embedding []float32
	// EntityNames are the member fact's entity names, which the write resolves
	// to the team bank's own entity rows and posts the new unit to. They are the
	// member's spellings, not team identities: folding them is a merge rule and
	// belongs to the round, not to the write.
	EntityNames []string
}

// MergeStanzaInput is what a round reads while it holds the lock.
type MergeStanzaInput struct {
	Project MergeProject
	// Previous is the team bank's current managed facts, in a stable order.
	// It is bounded, so it describes the bank rather than enumerating it.
	Previous []TeamFactRow
	// TeamRows are the team rows this round's own member facts already
	// produced, keyed by source unit id. This is the read the round's diff
	// decides on: keyed by the source unit rather than by the text, so an
	// edited member fact replaces its row instead of adding a second one.
	TeamRows map[string]TeamFactRow
	// ProjectTotal counts the facts the project's members hold, which is the
	// input a rebuild replays across its rounds. It is the bound a rebuild is
	// checked against, because the replay reads the whole of it eventually.
	ProjectTotal int
	// More reports that at least one member's bank holds facts beyond what this
	// round read, so the replay is not finished and the next round continues.
	More bool
	// UnmanagedFacts counts the team bank's facts no round wrote, which the
	// round's write replaces.
	UnmanagedFacts int
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
	// More reports that the member's bank holds facts beyond the prefix this
	// round could read, so the replay has not reached its end.
	More bool
	// Links are the member's links that touch a fact in Fresh. Both endpoints
	// are named by merge key; a link whose other endpoint is not in the team
	// bank yet is written when that endpoint arrives in a later round.
	Links []MergeLink
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
	// Replace carries the facts to insert and the provenance rows to record.
	Replace MergeReplaceResult
	// Cursors are the per-member positions to advance to, keyed by tenant id.
	// A member absent from the map keeps its cursor, which is what a member whose
	// input this round did not reach must do.
	Cursors map[string]MergeCursor
	// Rebuild reports the round replays the members' facts from the beginning
	// rather than reading past a cursor. A segmented rebuild reconciles only
	// when it completes; reconciling a partial replay would delete the content
	// of the batches it has not reached.
	Rebuild bool
	// RebuildDone clears rebuild_requested and makes the write reconcile the
	// team bank against the ledger. A rebuild that spans several rounds must
	// keep the flag: clearing it on the first round turns the rest into
	// incremental rounds, which resume at the cursor and never replay the
	// remainder of the members' banks.
	RebuildDone bool
	// Reconcile removes every managed team fact whose sources have all left
	// the members' banks. It is set only when a rebuild completes, after the
	// round's own provenance rows are written, so a fact carried by an earlier
	// segment still has a live source. An incremental round removes nothing.
	Reconcile bool
	// UnmanagedFacts counts the team bank's facts no round wrote. A positive
	// count makes the write sweep them, of every merged kind, before inserting —
	// which is what keeps a hand-written fact from surviving because the sweep
	// only knew about `world`.
	UnmanagedFacts int
}

// MergeReplaceResult is the fact-level change a round produces.
type MergeReplaceResult struct {
	// Keep is the team content the round read back unchanged.
	Keep []TeamFactRow
	// Insert is the facts to add: new sources, and the new text of an edited
	// one.
	Insert []TeamFactRow
	// Remove is the rows an edited source no longer produces. They are deleted
	// before the inserts, with the observations resting on them retired.
	Remove []TeamFactRow
	// Links are the member links to recreate between team facts. The write
	// resolves each endpoint's merge key to the team bank's own row, so a link
	// that names a fact another round wrote still lands.
	Links []MergeLink
	// Sources records, per member tenant id, each consumed source unit and the
	// team fact it supports — or tenant.DroppedMergeKey when it supports none.
	Sources map[string][]MergeSourceRow
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
	// UnmanagedFacts counts facts in the team bank that no merge round wrote,
	// which the next round will replace. A fact is the round's when its metadata
	// carries `origin=merge`; anything else — a hand-written fact, or a row an
	// older round keyed on its text — is not.
	UnmanagedFacts int
}

// MergeRoundOptions are the round's own settings: what a round reads and how
// much of it. The store does not hold them, because they are the job's
// configuration and the store is shared by every project.
type MergeRoundOptions struct {
	// Batch bounds the new member facts this round consumes, shared across the
	// project's members.
	Batch int
	// StaleAfter is how old a claim must be before it is treated as a crashed
	// round and taken over.
	StaleAfter time.Duration
	// IncludeExperience decides whether a member's `experience` facts are input.
	IncludeExperience bool
	// FailThreshold is how many consecutive failed rounds open the circuit
	// breaker. Zero means the breaker never opens, which is what a caller that
	// does not want the safeguard asks for.
	FailThreshold int
	// RecoveryInterval is how long an open breaker rests before one round is let
	// through to test it. Zero means a halted project stays halted until an
	// operator resets it.
	RecoveryInterval time.Duration
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
	// advisory lock is taken or merging_since is a live stamp. A claim older
	// than the round's StaleAfter is treated as a crashed round and taken over.
	// The reads run in one transaction with the claim, so no separate round can
	// start between them.
	BeginMergeStanza(ctx context.Context, teamID, projectID string, opts MergeRoundOptions,
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

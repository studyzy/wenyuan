// Package retainfold holds the rules that coalesce queued append writes for
// one document into a single execution.
//
// A document's writes are serialized by the claim's serialization key, which
// is what makes them correct — and also what makes a buffered burst of turns
// slow: each queued write re-reads and reprocesses the document in turn.
// Folding collapses that burst into one execution over the concatenated
// items, decided at claim time from rows the claim already holds.
//
// Only appends fold: an append is cumulative, so running several of them as
// one execution over the joined text produces the same document as running
// them in sequence. A replace means "this body supersedes what is stored", so
// a submission that is not append-only runs alone. Everything an execution
// applies once, from the primary's payload, has to match across the fold —
// tenant, API key, document tags, strategy — and a peer carrying file
// metadata keeps its own execution, because the attachment steps behind it
// assume a single-item write.
//
// The plan keeps the peers' submission order: it folds a contiguous prefix
// and leaves everything from the first peer that cannot join pending, so the
// document's text lands in the order the callers sent it. Folding is a pure
// optimization: a plan that folds nothing is always available, and the peers
// it defers stay pending for a later claim.
package retainfold

import (
	"encoding/json"
	"sort"

	"github.com/studyzy/wenyuan/internal/storage"
)

// DefaultMaxFoldPeers is how many peers fold into one execution on a first
// attempt. Bounded so a document that has accumulated a long backlog still
// commits in reasonable steps rather than one enormous transaction; the
// remainder is claimed on a later cycle.
const DefaultMaxFoldPeers = 16

// PayloadKey names where the fold's member list rides in a task payload, so
// the execution can slice its results back apart per operation. The list
// survives the payload's JSON round trip; RefsFromPayload reads it back.
const PayloadKey = "fold_members"

// Member is one queued retain operation considered for a fold. Items are the
// write's items as the task payload decodes them; the remaining fields carry
// what one execution applies once from the primary's payload, plus the
// credentials usage is attributed to.
type Member struct {
	OperationID  string
	Items        []storage.RetainItem
	TenantID     string
	APIKeyID     string
	DocumentTags []string
	Strategy     string
	// HasFileMetadata reports that the submission came from a converted
	// upload. Such an operation never folds: its attachment steps assume a
	// single-item write.
	HasFileMetadata bool
}

// AppendOnly reports whether every item this operation submitted appends to
// its document. Only appends are foldable, because only appends are
// cumulative; an operation with no items folds nothing.
func (m Member) AppendOnly() bool {
	if len(m.Items) == 0 {
		return false
	}
	for _, item := range m.Items {
		if item.UpdateMode != "append" {
			return false
		}
	}
	return true
}

// JoinRejection reports why peer may not join primary's execution, or the
// empty string when it may. Every rejection is one of the values an execution
// applies once, from the primary's payload: folding across it would silently
// apply the primary's value to the peer's content.
func JoinRejection(primary, peer Member) string {
	if !peer.AppendOnly() {
		return "peer is not append-only"
	}
	if peer.HasFileMetadata {
		return "peer carries file metadata"
	}
	if peer.TenantID != primary.TenantID || peer.APIKeyID != primary.APIKeyID {
		// Usage is attributed to the members of a fold, so mixing credentials
		// inside one execution would bill one caller for another's extraction.
		return "peer has a different tenant or api key"
	}
	if !sameTagSet(peer.DocumentTags, primary.DocumentTags) {
		// The execution applies the primary's document tags to the whole
		// document; folding a differently-tagged peer would drop its tags.
		return "peer has different document tags"
	}
	if peer.Strategy != primary.Strategy {
		return "peer has a different strategy"
	}
	return ""
}

// Plan is the outcome of choosing the peers that fold into one execution.
type Plan struct {
	// Members is the primary operation first, then each folded peer, in
	// submission order.
	Members []Member
	// Deferred lists the operation ids left pending for a later claim.
	Deferred []string
}

// PeerIDs returns the ids of the folded peers — every member but the primary.
func (p Plan) PeerIDs() []string {
	if len(p.Members) < 2 {
		return nil
	}
	ids := make([]string, 0, len(p.Members)-1)
	for _, member := range p.Members[1:] {
		ids = append(ids, member.OperationID)
	}
	return ids
}

// MaxPeersForRetry is the fold width for an operation on its retryCount-th
// attempt. A folded execution is all-or-nothing, so one poisonous item would
// otherwise drag every item folded with it down on every retry, and the same
// group would be folded together again next attempt — a burst of good content
// held hostage by one bad item. Halving the width per retry converges on the
// poison: by the time an operation has failed a few times it runs alone,
// fails alone, and is dead-lettered alone while its neighbours proceed.
func MaxPeersForRetry(retryCount int) int {
	if retryCount <= 0 {
		return DefaultMaxFoldPeers
	}
	width := DefaultMaxFoldPeers >> retryCount
	if width < 0 {
		return 0
	}
	return width
}

// PlanFold chooses the contiguous run of peers that folds into primary. Peers
// must already be in claim order — oldest first, ties broken by operation id —
// which is the order the document accumulates in.
//
// The fold stops at the first peer that cannot join and takes nothing after
// it: appends are cumulative, so skipping one and taking the next would commit
// turns out of order. Peers left behind stay pending and are claimed on a
// later cycle, still in order.
//
// The primary always runs, however large: only peers are held to the token
// budget, and a peer that would push the execution past it ends the fold. The
// budget is the same per-execution token setting the queued-submission split
// applies; storage.DefaultRetainSubBatchTokens is its built-in value.
func PlanFold(primary Member, peers []Member, maxPeers, tokenBudget int) Plan {
	plan := Plan{Members: []Member{primary}}
	if maxPeers <= 0 || !primary.AppendOnly() || primary.HasFileMetadata {
		// A primary that is not an append-only plain write is not a fold base
		// at all: whatever queued behind it must wait for it to finish and
		// then be re-evaluated against the document it leaves behind.
		plan.Deferred = operationIDs(peers)
		return plan
	}

	used := itemsTokens(primary.Items)
	for index, peer := range peers {
		if len(plan.Members) > maxPeers || JoinRejection(primary, peer) != "" {
			plan.Deferred = operationIDs(peers[index:])
			break
		}
		peerTokens := itemsTokens(peer.Items)
		if used+peerTokens > tokenBudget {
			plan.Deferred = operationIDs(peers[index:])
			break
		}
		used += peerTokens
		plan.Members = append(plan.Members, peer)
	}
	return plan
}

// MergeItems flattens a fold's submissions into the item list of one
// execution. Order is the members' order, which is submission order — the
// document ends up carrying the turns exactly as the callers sent them.
func MergeItems(members []Member) []storage.RetainItem {
	total := 0
	for _, member := range members {
		total += len(member.Items)
	}
	items := make([]storage.RetainItem, 0, total)
	for _, member := range members {
		items = append(items, member.Items...)
	}
	return items
}

// MemberRef is one fold member as it survives the trip through the task
// payload. The fold is decided at claim time and consumed by the execution,
// with the payload in between, so only what the execution needs downstream
// rides along: which operation it covers, and how many items it contributed —
// which is what slices the execution's results back apart for the
// per-operation completion bookkeeping.
type MemberRef struct {
	OperationID string `json:"operation_id"`
	ItemsCount  int    `json:"items_count"`
}

// RefsFor renders the members' payload form, primary first.
func RefsFor(members []Member) []MemberRef {
	if len(members) == 0 {
		return nil
	}
	refs := make([]MemberRef, 0, len(members))
	for _, member := range members {
		refs = append(refs, MemberRef{OperationID: member.OperationID, ItemsCount: len(member.Items)})
	}
	return refs
}

// RefsFromPayload reads the fold members back from a task payload value. The
// payload decodes as JSON, so the value arrives as a list of objects; anything
// else reads as absent, and a payload that names no fold reads as absent too.
func RefsFromPayload(value any) ([]MemberRef, bool) {
	if value == nil {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var refs []MemberRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, false
	}
	return refs, true
}

// operationIDs lists the members' operation ids.
func operationIDs(members []Member) []string {
	if len(members) == 0 {
		return nil
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.OperationID)
	}
	return ids
}

// itemsTokens measures a member's items with the same token count the
// queued-submission split budgets with.
func itemsTokens(items []storage.RetainItem) int {
	total := 0
	for _, item := range items {
		total += storage.CountTokens(item.Content)
	}
	return total
}

// sameTagSet reports whether two tag lists carry the same multiset of tags,
// so a fold does not attach one submission's tags to another's text.
func sameTagSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

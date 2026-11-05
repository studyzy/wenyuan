package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/studyzy/wenyuan/internal/retainfold"
	"github.com/studyzy/wenyuan/internal/storage"
)

// foldClaimedRetain coalesces the queued appends for the claimed operation's
// document into its execution. Appends are cumulative, so running several of
// them as one write over the joined items produces the same document as
// running them in turn — and a buffered burst of turns stops re-reading and
// reprocessing the document once per item.
//
// The fold is decided from the queued rows and applied before any work runs:
// the peers that join are claimed here, so an operation is either folded and
// executed, or left pending for a later claim. Anything that goes wrong leaves
// the claimed operation running on its own and every peer pending — the fold
// is an optimization, never a dependency.
func (c *Consumer) foldClaimedRetain(ctx context.Context, op *storage.Operation) []storage.Operation {
	// Only an append-only write for one document folds; an operation without a
	// serialization key spans several documents (or none) and has nothing to
	// coalesce on.
	if op.OperationType != storage.OperationTypeRetain || op.SerializationKey == "" || op.Payload == nil {
		return nil
	}
	maxPeers := retainfold.MaxPeersForRetry(op.RetryCount)
	if maxPeers <= 0 {
		return nil
	}
	peers, err := c.foldRetainPeers(ctx, op, maxPeers)
	if err != nil {
		c.cfg.Logger.Warn("retain fold skipped; the claimed operation runs on its own",
			"operation_id", op.OperationID, "bank_id", op.BankID, "error", err)
		return nil
	}
	if len(peers) > 0 {
		slog.Default().Info("folded queued retains into one execution",
			"operation_id", op.OperationID, "bank_id", op.BankID,
			"peers", len(peers))
	}
	return peers
}

// foldRetainPeers plans the fold, claims the peers that join it, and rewrites
// the claimed payload in place: the items become the members' merged list and
// the member list rides alongside, so the execution covers every member and
// the settlement can account for them. The claimed payload is never persisted
// — the row keeps its own submission, so a reclaim always runs it unfolded.
func (c *Consumer) foldRetainPeers(ctx context.Context, op *storage.Operation, maxPeers int) ([]storage.Operation, error) {
	primary, err := retainFoldMember(op.OperationID, op.Payload)
	if err != nil {
		return nil, err
	}
	candidates, err := c.store.PendingRetainPeers(ctx, op.BankID, op.SerializationKey, maxPeers)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	peers := make([]retainfold.Member, 0, len(candidates))
	for _, candidate := range candidates {
		member, err := retainFoldMember(candidate.OperationID, candidate.Payload)
		if err != nil {
			// One unreadable payload bars the fold rather than quietly running
			// a plan that skips it: the members around it stay in order.
			return nil, err
		}
		peers = append(peers, member)
	}
	plan := retainfold.PlanFold(primary, peers, maxPeers, c.cfg.RetainFoldTokens)
	if len(plan.Members) < 2 {
		return nil, nil
	}
	ids := plan.PeerIDs()
	claimed, err := c.store.ClaimPendingOperations(ctx, c.workerID(), ids)
	if err != nil {
		return nil, err
	}
	// The claim holds each peer it returned. The fold takes the contiguous run
	// they cover — the peers the claim missed stay pending, still in order, and
	// are claimed on a later cycle.
	held := make(map[string]struct{}, len(claimed))
	for _, id := range claimed {
		held[id] = struct{}{}
	}
	covered := 0
	for _, id := range ids {
		if _, ok := held[id]; !ok {
			break
		}
		covered++
	}
	if covered == 0 {
		return nil, nil
	}
	// A peer the claim took beyond the covered run cannot join the execution —
	// its predecessor broke the fold — and must not sit in processing with no
	// execution behind it, so it goes straight back to pending.
	for _, id := range claimed[covered:] {
		if err := c.store.ReleaseOperationClaim(ctx, id, c.workerID()); err != nil {
			c.cfg.Logger.Warn("could not return an unfolded peer to the queue",
				"operation_id", id, "error", err)
		}
	}
	members := plan.Members[:covered+1]
	// The rewrite lands on a copy: a backend that hands out its stored map
	// (the memory store does) must not find its row's payload rewritten under
	// it — the row keeps its own submission, in both backends.
	payload := make(map[string]any, len(op.Payload)+1)
	for key, value := range op.Payload {
		payload[key] = value
	}
	payload["items"] = storage.AsyncRetainItemsPayload(retainfold.MergeItems(members))
	payload[retainfold.PayloadKey] = retainfold.RefsFor(members)
	op.Payload = payload
	return candidates[:covered], nil
}

// retainFoldMember builds one fold member from a queued payload. The fields
// are what one execution applies once, from the primary's payload, or what
// bars a fold outright.
func retainFoldMember(operationID string, payload map[string]any) (retainfold.Member, error) {
	req, err := storage.DecodeAsyncRetainPayload(payload)
	if err != nil {
		return retainfold.Member{}, err
	}
	return retainfold.Member{
		OperationID:  operationID,
		Items:        req.Items,
		DocumentTags: req.DocumentTags,
		// The extraction settings a queued write resolved at submission are
		// what the execution applies to the whole merged item list, so a peer
		// resolved under different settings cannot join one plan with them.
		Strategy: retainFoldSettingsFingerprint(req),
		// A converted upload always gives its items a metadata dict, so a
		// payload carrying one marks an upload; its steps assume a single-item
		// write and it keeps its own execution.
		HasFileMetadata: retainItemsCarryMetadata(req.Items),
	}, nil
}

// retainFoldSettingsFingerprint renders the write settings a folded execution
// applies once as one comparable string. A peer whose fingerprint differs is
// rejected before the plan, so a fold never extracts one submission's content
// under another's settings.
func retainFoldSettingsFingerprint(req storage.RetainRequest) string {
	vocabulary := "absent"
	if req.Vocabulary != nil {
		if raw, err := json.Marshal(req.Vocabulary); err == nil {
			vocabulary = string(raw)
		} else {
			vocabulary = "unreadable"
		}
	}
	return fmt.Sprintf("%q|%q|%q|%t|%d|%d|%d|%t|%s",
		req.ExtractionMode, req.ExtractionMission, req.ExtractionInstructions,
		req.OptionalFactDimensions, req.ChunkSize, req.StructuredChunkSize,
		req.MaxAttachmentsPerChunk, req.DiscardDocumentText, vocabulary)
}

// retainItemsCarryMetadata reports whether any of a payload's items carried
// the metadata member. A converted upload always writes one, even empty.
func retainItemsCarryMetadata(items []storage.RetainItem) bool {
	for _, item := range items {
		if item.Metadata != nil {
			return true
		}
	}
	return false
}

// -- Fold-aware settlement -------------------------------------------------
//
// A folded execution is responsible for every operation the claim handed it,
// not just the row it was keyed on, so every terminal transition fans out over
// the members: they either all committed or none did. Retry and deferral put
// the whole group back to pending, so it is re-claimed — and re-folded, more
// narrowly each retry — together.

// completeFolded completes every member the claim folded into the primary.
// The execution's reported metadata describes the whole run, so only the
// primary carries it.
func (c *Consumer) completeFolded(ctx context.Context, folded []storage.Operation) {
	for _, peer := range folded {
		if _, err := c.settle(ctx, peer.OperationID, storage.StatusCompleted, "", nil); err != nil {
			c.reportSettleLapse(peer, "mark-completed", err)
			continue
		}
		c.cfg.Logger.Info("operation completed", "operation_id", peer.OperationID, "bank_id", peer.BankID)
		c.promoteParent(ctx, peer)
	}
}

// failFolded fails every member the claim folded into the primary with the
// execution's cause.
func (c *Consumer) failFolded(ctx context.Context, folded []storage.Operation, detail string) {
	truncated := truncateErrorMessage(detail)
	for _, peer := range folded {
		if _, err := c.settle(ctx, peer.OperationID, storage.StatusFailed, truncated, nil); err != nil {
			c.reportSettleLapse(peer, "mark-failed", err)
			continue
		}
		c.cfg.Logger.Warn("operation failed permanently", "operation_id", peer.OperationID, "detail", truncated)
		c.promoteParent(ctx, peer)
	}
}

// retryFolded schedules every member the claim folded into the primary for a
// retry at the primary's moment, each with its own attempt count advanced.
func (c *Consumer) retryFolded(ctx context.Context, folded []storage.Operation, next time.Time, detail string) {
	for _, peer := range folded {
		if err := c.store.ScheduleOperationRetry(ctx, peer.OperationID, peer.RetryCount+1, next, detail); err != nil {
			c.reportSettleLapse(peer, "schedule-retry", err)
		}
	}
}

// deferFolded holds every member the claim folded into the primary until the
// primary's deferral moment, without spending any member's attempt budget.
func (c *Consumer) deferFolded(ctx context.Context, folded []storage.Operation, next time.Time) {
	if next.IsZero() {
		return
	}
	deferrer, hasDeferrer := c.store.(storage.OperationDeferrer)
	for _, peer := range folded {
		var err error
		if hasDeferrer {
			err = deferrer.DeferOperation(ctx, peer.OperationID, next, nil)
		} else {
			err = c.store.ScheduleOperationRetry(ctx, peer.OperationID, peer.RetryCount, next, "")
		}
		if err != nil {
			c.reportSettleLapse(peer, "defer", err)
		}
	}
}

package storage

import (
	"context"
	"time"
)

// SubmitAsyncRetain queues a batch retain on the in-process store: a parent
// operation that aggregates the submission and one child per token-bounded
// sub-batch. The postgres backend documents the semantics; this is the same
// rule applied to the in-memory operation table.
//
// The whole submission is made under the store lock, so either every row of it
// is visible to a claim or none is — the atomicity the postgres backend gets
// from one transaction.
func (s *MemoryStore) SubmitAsyncRetain(ctx context.Context, req RetainRequest, tokensPerBatch int) (AsyncRetainSubmission, error) {
	if err := ctx.Err(); err != nil {
		return AsyncRetainSubmission{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return AsyncRetainSubmission{}, s.writeErr
	}
	if _, ok := s.banks[req.BankID]; !ok {
		if err := s.createBankLocked(req.BankID); err != nil {
			return AsyncRetainSubmission{}, err
		}
	}
	parentID := req.OperationID
	if parentID == "" {
		parentID = newUUID()
	} else if existing, ok := s.operations[parentID]; ok {
		// A caller-supplied identifier that already names this bank's batch is
		// a retried submission: it rejoins the original instead of queueing the
		// work a second time. An identifier in use by anything else is a
		// conflict rather than a silent overwrite.
		if existing.BankID != req.BankID || existing.OperationType != AsyncRetainType {
			return AsyncRetainSubmission{}, ErrOperationConflict
		}
		return AsyncRetainSubmission{
			OperationID:  parentID,
			ItemsCount:   intFromMetadata(existing.ResultMetadata, "items_count"),
			Deduplicated: true,
		}, nil
	}
	parent, children := asyncRetainPlan(parentID, req, tokensPerBatch)
	now := time.Now().UTC()
	parent.Run = 1
	parent.CreatedAt = now
	parent.UpdatedAt = now
	s.operations[parentID] = parent
	for _, child := range children {
		s.operations[child.OperationID] = Operation{
			OperationID:      child.OperationID,
			BankID:           req.BankID,
			OperationType:    OperationTypeRetain,
			Status:           StatusPending,
			Run:              1,
			CreatedAt:        now,
			UpdatedAt:        now,
			Payload:          child.Payload,
			ResultMetadata:   child.Metadata,
			SerializationKey: child.SerializationKey,
		}
	}
	return AsyncRetainSubmission{OperationID: parentID, ItemsCount: len(req.Items)}, nil
}

package storage

import (
	"context"
	"sort"
	"time"
)

// PutOperation records an asynchronous operation in its initial state.
func (s *MemoryStore) PutOperation(ctx context.Context, op Operation) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op.OperationID == "" {
		op.OperationID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}
	now := time.Now().UTC()
	op.CreatedAt = now
	op.UpdatedAt = now
	if op.Run == 0 {
		op.Run = 1
	}
	op.Payload = executablePayload(op.Payload)
	s.operations[op.OperationID] = op
	return op, nil
}

// RestoreOperation records an operation keeping the timestamps it carries, so an
// import replays the exported row's history instead of stamping the restore
// time. See the interface's note on why it is separate from PutOperation.
func (s *MemoryStore) RestoreOperation(ctx context.Context, op Operation) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op.OperationID == "" {
		op.OperationID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}
	if op.UpdatedAt.IsZero() {
		op.UpdatedAt = op.CreatedAt
	}
	if op.Run == 0 {
		op.Run = 1
	}
	op.Payload = executablePayload(op.Payload)
	s.operations[op.OperationID] = op
	return op, nil
}

// GetOperation returns one operation by identifier, scoped to the bank; an
// identifier belonging to another bank reads as ErrOperationNotFound.
func (s *MemoryStore) GetOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	op, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if bankID != "" && op.BankID != bankID {
		return Operation{}, ErrOperationNotFound
	}
	return op, nil
}

// getOperation returns one operation by identifier alone, for internal callers
// that already hold a bank-scoped reference.
func (s *MemoryStore) getOperation(ctx context.Context, opID string) (Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.operations[opID]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	op.IsParent = operationIsParent(op.ResultMetadata)
	op.Progress = operationProgress(op.ResultMetadata)
	return op, nil
}

// OperationAlive reports whether the identified operation exists, scoped to the
// bank, and is not cancelled. A missing row reads as not alive rather than
// faulting, which is what the consumer's execution-start guard needs: a bank
// deleted mid-flight takes its queued operations with it.
func (s *MemoryStore) OperationAlive(ctx context.Context, bankID, opID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.operationAliveLocked(bankID, opID), nil
}

// operationAliveLocked is OperationAlive's body for callers that already hold
// the lock, notably the retain write's item-boundary checkpoint.
func (s *MemoryStore) operationAliveLocked(bankID, opID string) bool {
	op, ok := s.operations[opID]
	if !ok || (bankID != "" && op.BankID != bankID) {
		return false
	}
	return op.Status != StatusCancelled
}

// GetOperationChildren returns the child operations of a parent batch, ordered
// by sub-batch index.
func (s *MemoryStore) GetOperationChildren(ctx context.Context, bankID, opID string) ([]ChildOperation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	children := make([]ChildOperation, 0)
	for _, op := range s.operations {
		if op.BankID != bankID || stringFromMetadata(op.ResultMetadata, "parent_operation_id") != opID {
			continue
		}
		children = append(children, ChildOperation{
			OperationID:   op.OperationID,
			Status:        op.Status,
			SubBatchIndex: optionalIntFromMetadata(op.ResultMetadata, "sub_batch_index"),
			ItemsCount:    optionalIntFromMetadata(op.ResultMetadata, "items_count"),
			ErrorMessage:  optionalStringFromMetadata(op.Error),
		})
	}
	sort.SliceStable(children, func(i, j int) bool {
		left, right := children[i].SubBatchIndex, children[j].SubBatchIndex
		switch {
		case left == nil && right == nil:
		case left == nil:
			return false
		case right == nil:
			return true
		case *left != *right:
			return *left < *right
		}
		return children[i].OperationID < children[j].OperationID
	})
	return children, nil
}

// optionalIntFromMetadata reads an integer-valued metadata key, distinguishing
// an absent key from a stored zero so the read can emit an explicit null.
func optionalIntFromMetadata(metadata map[string]any, key string) *int {
	if _, present := metadata[key]; !present {
		return nil
	}
	value := intFromMetadata(metadata, key)
	return &value
}

// optionalStringFromMetadata returns a pointer to a non-empty value, and nil for
// an unset one so the field is emitted as null.
func optionalStringFromMetadata(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// AdvanceOperation moves an operation forward in its lifecycle, rejecting
// illegal transitions.
func (s *MemoryStore) AdvanceOperation(ctx context.Context, opID string, next OperationStatus, errMsg string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	if !validTransition(op.Status, next) {
		return op, ErrOperationConflict
	}
	now := time.Now().UTC()
	op.Status = next
	op.Error = errMsg
	op.UpdatedAt = now
	if next.Terminal() {
		// A terminal operation is finished with, so its claim is cleared and
		// its completion time recorded in the same move.
		op.CompletedAt = &now
		op.WorkerID = ""
		op.ClaimedAt = nil
	}
	s.operations[opID] = op
	return op, nil
}

// AdvanceOperationWithResult advances an operation and merges result metadata
// onto it, implementing OperationResultRecorder so a processor's report reaches
// the operation record on the in-memory backend too.
func (s *MemoryStore) AdvanceOperationWithResult(ctx context.Context, opID string, next OperationStatus, errMsg string, result map[string]any) (Operation, error) {
	advanced, err := s.AdvanceOperation(ctx, opID, next, errMsg)
	if err != nil {
		return advanced, err
	}
	if len(result) == 0 {
		return advanced, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-read under the lock: AdvanceOperation released it, and a concurrent
	// settle must not be lost by writing back a stale copy.
	op := s.operations[opID]
	mergeOperationMetadata(&op, result)
	s.operations[opID] = op
	return op, nil
}

// mergeOperationMetadata folds result into an operation's metadata, key by key,
// so a field written earlier — when the operation was enqueued, or by an earlier
// attempt — survives. The caller must hold the store lock.
func mergeOperationMetadata(op *Operation, result map[string]any) {
	if op.ResultMetadata == nil {
		op.ResultMetadata = map[string]any{}
	}
	for key, value := range result {
		op.ResultMetadata[key] = value
	}
}

// RetryOperation starts a new run of a failed operation, scoped to the bank.
//
// A failed batch parent is retried by re-running the work that failed beneath
// it: the parent holds no task payload of its own, so resetting the parent
// alone would queue a row nothing executes. Its failed and cancelled children
// go back to pending with their stored payloads intact, and the parent is
// revived so it re-aggregates once they finish. The reference's own rule, and
// the reason it does not simply create a new submission: re-ingesting from
// scratch is what duplicates memories.
func (s *MemoryStore) RetryOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok || (bankID != "" && op.BankID != bankID) {
		return Operation{}, ErrOperationNotFound
	}
	// The payload-less batch parent is decided first, as the Postgres twin does
	// and as the reference branches (engine/memory_engine.py:21510-21567): a
	// completed batch parent with no children left answers the batch-parent 409,
	// not the generic status one.
	if op.OperationType == AsyncRetainType && operationIsParent(op.ResultMetadata) {
		return s.retryBatchParentLocked(op)
	}
	if !retryableStatus(op) {
		return op, ErrOperationConflict
	}
	op.Status = StatusPending
	op.Error = ""
	op.Run++
	op.RetryCount = 0
	op.CompletedAt = nil
	op.NextRetryAt = nil
	op.WorkerID = ""
	op.ClaimedAt = nil
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return op, nil
}

// retryableStatus reports whether an operation may be retried: a failed or
// cancelled one, or a deferred one — pending with a next_retry_at still in the
// future, which retrying runs now rather than waiting out. A pending operation
// that is not deferred (already due) is refused: it is queued and will run on
// its own, so there is nothing to bring forward.
func retryableStatus(op Operation) bool {
	switch op.Status {
	case StatusFailed, StatusCancelled:
		return true
	case StatusPending:
		return op.NextRetryAt != nil && op.NextRetryAt.After(time.Now().UTC())
	default:
		return false
	}
}

// retryBatchParentLocked requeues a failed batch's children and revives the
// parent over them. The caller must hold the store lock.
func (s *MemoryStore) retryBatchParentLocked(parent Operation) (Operation, error) {
	now := time.Now().UTC()
	outstanding := false
	for id, op := range s.operations {
		if op.BankID != parent.BankID {
			continue
		}
		if childParent, _ := op.ResultMetadata["parent_operation_id"].(string); childParent != parent.OperationID {
			continue
		}
		if retryableStatus(op) {
			op.Status = StatusPending
			op.Error = ""
			op.NextRetryAt = nil
			op.WorkerID = ""
			op.ClaimedAt = nil
			op.CompletedAt = nil
			op.RetryCount = 0
			op.UpdatedAt = now
			s.operations[id] = op
			outstanding = true
			continue
		}
		if !op.Status.Terminal() {
			// A live child still owns its run; resetting one already
			// processing would let a second worker race it on the document.
			outstanding = true
		}
	}
	if !outstanding {
		// Nothing beneath it can be re-run, so reviving the parent would strand
		// it pending with no child left to promote it.
		return parent, ErrBatchParentNoWork
	}
	parent.Status = StatusPending
	parent.Error = ""
	parent.CompletedAt = nil
	parent.NextRetryAt = nil
	parent.RetryCount = 0
	parent.UpdatedAt = now
	s.operations[parent.OperationID] = parent
	return parent, nil
}

// ListOperations returns one page of operations matching the query, most
// recently created first, together with the pre-paging total.
func (s *MemoryStore) ListOperations(ctx context.Context, query OperationQuery) (OperationPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]Operation, 0, len(s.operations))
	for _, op := range s.operations {
		if query.BankID != "" && op.BankID != query.BankID {
			continue
		}
		if query.Status != "" && string(op.Status) != query.Status {
			continue
		}
		if query.Type != "" && op.OperationType != query.Type {
			continue
		}
		if query.WorkerID != "" && op.WorkerID != query.WorkerID {
			continue
		}
		if query.ExcludeParents && operationIsParent(op.ResultMetadata) {
			continue
		}
		op.IsParent = operationIsParent(op.ResultMetadata)
		op.Progress = operationProgress(op.ResultMetadata)
		matched = append(matched, op)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].OperationID < matched[j].OperationID
	})
	total := len(matched)
	// An omitted/non-positive limit is the internal scan default; the API
	// layer always passes a concrete page size (upstream declares ge=1).
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	start, end := pageBounds(total, query.Offset, limit)
	return OperationPage{Operations: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// CancelOperation cancels a non-terminal operation, returning
// ErrOperationConflict when the operation is already terminal, scoped to the
// bank.
func (s *MemoryStore) CancelOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok || (bankID != "" && op.BankID != bankID) {
		return Operation{}, ErrOperationNotFound
	}
	if op.Status.Terminal() {
		return op, ErrOperationConflict
	}
	// Only the status and updated_at move: the reference leaves completed_at
	// null for a cancelled operation.
	op.Status = StatusCancelled
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return op, nil
}

// DeleteOperation permanently removes a terminal operation, returning
// ErrOperationNotFound when the identifier is unknown or belongs to another
// bank, and ErrOperationNotTerminal while the operation is still live.
func (s *MemoryStore) DeleteOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[opID]
	if !ok || op.BankID != bankID {
		return Operation{}, ErrOperationNotFound
	}
	if !op.Status.Terminal() {
		return op, ErrOperationNotTerminal
	}
	delete(s.operations, opID)
	// An export operation owns a stored archive keyed in its result_metadata;
	// it must go with the row or the blob outlives its only handle. The
	// in-memory backend holds the payloads in the same store, so the removal is
	// local. Best-effort, matching the reference: the row is already gone.
	if key, _ := op.ResultMetadata[OperationPayloadKeyStoredFile].(string); key != "" {
		delete(s.storedFiles, key)
	}
	return op, nil
}

// RepairPendingBatchParent records the corrected terminal status of a parent
// batch operation a read found out of sync. The postgres backend documents the
// semantics; this is the same guarded rule applied to the in-memory operation
// table.
func (s *MemoryStore) RepairPendingBatchParent(ctx context.Context, bankID, parentID string, status OperationStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	op, ok := s.operations[parentID]
	if !ok || (bankID != "" && op.BankID != bankID) || op.Status != StatusPending {
		return nil
	}
	now := time.Now().UTC()
	op.Status = status
	op.UpdatedAt = now
	op.CompletedAt = &now
	s.operations[parentID] = op
	return nil
}

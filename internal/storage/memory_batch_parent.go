package storage

import (
	"context"
	"time"
)

// FinalizeBatchParent promotes a parent batch operation once every child has
// reached a terminal status, aggregating the children's outcome counters onto
// it. The postgres backend documents the semantics; this is the same rule
// applied to the in-memory operation table.
func (s *MemoryStore) FinalizeBatchParent(ctx context.Context, bankID, parentID string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.operations[parentID]
	if !ok || (bankID != "" && parent.BankID != bankID) {
		return Operation{}, false, ErrOperationNotFound
	}
	if parent.Status.Terminal() {
		return parent, false, nil
	}
	children := make([]batchChild, 0)
	for _, op := range s.operations {
		if op.BankID != parent.BankID || op.ResultMetadata == nil {
			continue
		}
		if childParent, _ := op.ResultMetadata["parent_operation_id"].(string); childParent == parentID {
			children = append(children, batchChild{
				OperationID:  op.OperationID,
				Status:       op.Status,
				ErrorMessage: op.Error,
				Metadata:     op.ResultMetadata,
			})
		}
	}
	if len(children) == 0 {
		return parent, false, nil
	}
	status, errorMessage, aggregate := aggregateChildren(children)
	if status == "" {
		return parent, false, nil
	}
	if parent.ResultMetadata == nil {
		parent.ResultMetadata = map[string]any{}
	}
	for key, value := range aggregate {
		parent.ResultMetadata[key] = value
	}
	parent.Status = status
	parent.Error = errorMessage
	now := time.Now().UTC()
	parent.UpdatedAt = now
	if status.Terminal() {
		parent.CompletedAt = &now
	}
	s.operations[parentID] = parent
	return parent, true, nil
}

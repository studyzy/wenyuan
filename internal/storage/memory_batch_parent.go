package storage

import (
	"context"
	"time"
)

// FinalizeBatchParent promotes a parent batch operation once every child has
// reached a terminal status. The promotion branches per outcome, exactly as the
// reference writes them (engine/memory_engine.py:6021-6068): a completed parent
// takes the children's aggregated outcome counters and a completion time, a
// failed one takes only its status, error message and updated_at, and a
// cancelled one only its status and updated_at.
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
	now := time.Now().UTC()
	switch status {
	case StatusFailed:
		// The batch did not finish: no completion time and no outcome roll-up,
		// only the cause an operator still needs to see.
		parent.Status = status
		parent.Error = errorMessage
		parent.UpdatedAt = now
	case StatusCancelled:
		// The batch was stopped, not finished: no cause and no completion time.
		parent.Status = status
		parent.UpdatedAt = now
	default:
		if parent.ResultMetadata == nil {
			parent.ResultMetadata = map[string]any{}
		}
		for key, value := range aggregate {
			parent.ResultMetadata[key] = value
		}
		parent.Status = status
		parent.Error = errorMessage
		parent.UpdatedAt = now
		parent.CompletedAt = &now
	}
	s.operations[parentID] = parent
	return parent, true, nil
}

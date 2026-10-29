package storage

import (
	"context"
	"database/sql"
	"errors"
)

// FinalizeBatchParent promotes a parent batch operation to a terminal status
// once every child has reached one.
//
// The parent is a status aggregator: it carries no task payload of its own, so
// no worker ever runs it, and it reaches a terminal state only when its last
// child does. The promotion is a guarded write — `status IN ('pending',
// 'processing')` — so when two children finish at once exactly one promotes the
// parent and the loser leaves it alone rather than re-terminalising it. The
// children's outcome counters are summed onto the parent, so a caller polling
// the parent reads the same totals the individual children report.
func (s *PostgresStore) FinalizeBatchParent(ctx context.Context, bankID, parentID string) (Operation, bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Operation
		var zero1 bool
		return zero0, zero1, schemaErr
	}
	parent, err := s.getOperation(ctx, parentID)
	if err != nil {
		return Operation{}, false, err
	}
	if bankID != "" && parent.BankID != bankID {
		return Operation{}, false, ErrOperationNotFound
	}
	if parent.Status.Terminal() {
		return parent, false, nil
	}
	children, err := s.batchChildren(ctx, parent.BankID, parentID)
	if err != nil {
		return Operation{}, false, err
	}
	if len(children) == 0 {
		return parent, false, nil
	}
	status, errorMessage, aggregate := aggregateChildren(children)
	if status == "" {
		// A child is still pending or processing: the parent waits.
		return parent, false, nil
	}
	metadata, err := marshalMetadata(aggregate)
	if err != nil {
		return Operation{}, false, err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET status=$2, error_message=$3, updated_at=now(), completed_at=now(),
	              result_metadata=(COALESCE(result_metadata,'{}'::jsonb) || $4::jsonb)
	          WHERE operation_id=$1 AND status IN ('pending','processing')
	          RETURNING ` + operationColumns
	promoted, err := scanOperation(s.db.QueryRowContext(ctx, query, parentID, string(status), errorMessage, metadata))
	if errors.Is(err, sql.ErrNoRows) {
		// Another child promoted it first, or it was cancelled concurrently.
		current, getErr := s.getOperation(ctx, parentID)
		if getErr != nil {
			return Operation{}, false, getErr
		}
		return current, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	return promoted, true, nil
}

// batchChildren reads a parent's children with the outcome fields aggregation
// needs.
func (s *PostgresStore) batchChildren(ctx context.Context, bankID, parentID string) ([]batchChild, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []batchChild
		return zero0, schemaErr
	}
	query := `SELECT operation_id::text, status, COALESCE(error_message,''),
	                 COALESCE(result_metadata,'{}'::jsonb)::text
	          FROM ` + quoteIdent(schema) + `.async_operations
	          WHERE bank_id = $1 AND result_metadata->>'parent_operation_id' = $2`
	rows, err := s.reader().QueryContext(ctx, query, bankID, parentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	children := make([]batchChild, 0)
	for rows.Next() {
		var child batchChild
		var status, metadataRaw string
		if err := rows.Scan(&child.OperationID, &status, &child.ErrorMessage, &metadataRaw); err != nil {
			return nil, err
		}
		child.Status = OperationStatus(status)
		child.Metadata = decodeJSONMap([]byte(metadataRaw))
		children = append(children, child)
	}
	return children, rows.Err()
}

// batchChild is one child operation as the parent roll-up reads it.
type batchChild struct {
	OperationID  string
	Status       OperationStatus
	ErrorMessage string
	Metadata     map[string]any
}

// aggregateChildren decides a parent's terminal status from its children and
// the outcome metadata to merge onto it. An empty status means a child is still
// in flight, so the parent is not ready to be promoted. A real failure outranks
// a cancellation, because it carries a cause an operator still needs to see.
//
// The roll-up folds exactly the outcome counters a child produces — the units
// it stored and the extraction errors it swallowed. The parent's own item count
// is the submission's and is not a sum: it was written when the batch was
// queued, and summing the children's item counts over it would double it.
func aggregateChildren(children []batchChild) (OperationStatus, string, map[string]any) {
	allDone := true
	anyFailed := false
	anyCancelled := false
	agg := map[string]any{}
	for _, child := range children {
		switch child.Status {
		case StatusCompleted:
		case StatusFailed:
			anyFailed = true
		case StatusCancelled:
			anyCancelled = true
		default:
			allDone = false
		}
		for _, key := range []string{"unit_ids_count", "extraction_errors_count"} {
			agg[key] = intFromMetadata(agg, key) + intFromMetadata(child.Metadata, key)
		}
	}
	if !allDone {
		return "", "", nil
	}
	if anyFailed {
		failures := make([]string, 0, len(children))
		for _, child := range children {
			failures = append(failures, child.ErrorMessage)
		}
		return StatusFailed, summarizeChildErrors(failures), agg
	}
	if anyCancelled {
		return StatusCancelled, "", agg
	}
	return StatusCompleted, "", agg
}

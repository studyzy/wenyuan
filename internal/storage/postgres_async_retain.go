package storage

import (
	"context"
	"database/sql"
	"errors"
)

// SubmitAsyncRetain queues a batch retain on PostgreSQL: one parent batch
// operation that aggregates the submission and carries no work a worker runs,
// plus one child operation per token-bounded sub-batch.
//
// The parent and its children are inserted in a single transaction, so a
// failure between them cannot leave a parent with no children: such a row would
// never be promoted by anything (nothing executes it) and would sit pending
// forever. The parent's primary key is the caller's identifier when they
// supplied one, which is what makes a retried submission rejoin the original
// rather than duplicate the work.
func (s *PostgresStore) SubmitAsyncRetain(ctx context.Context, req RetainRequest, tokensPerBatch int) (AsyncRetainSubmission, error) {
	if err := ctx.Err(); err != nil {
		return AsyncRetainSubmission{}, err
	}
	// A write to an unknown bank creates it, exactly as the synchronous path
	// does: callers address a bank by name and declaring it first is not part
	// of the contract.
	if _, err := s.ensureBankExists(ctx, req.BankID); err != nil {
		return AsyncRetainSubmission{}, err
	}
	parentID := req.OperationID
	if parentID == "" {
		parentID = newUUID()
	} else if replay, err := s.resolveRetainReplay(ctx, req.BankID, parentID); err == nil {
		return replay, nil
	} else if !errors.Is(err, ErrOperationNotFound) {
		return AsyncRetainSubmission{}, err
	}
	parent, children := asyncRetainPlan(parentID, req, tokensPerBatch)
	if err := s.insertAsyncRetain(ctx, parent, children); err != nil {
		// The read above is a short-circuit, not the concurrency authority: two
		// simultaneous first submissions of one identifier both find nothing and
		// then collide on the primary key. The loser resolves to the winner's
		// operation rather than failing the caller.
		if req.OperationID != "" {
			if replay, replayErr := s.resolveRetainReplay(ctx, req.BankID, req.OperationID); replayErr == nil {
				return replay, nil
			}
		}
		return AsyncRetainSubmission{}, err
	}
	return AsyncRetainSubmission{OperationID: parentID, ItemsCount: len(req.Items)}, nil
}

// resolveRetainReplay reports the submission a caller-supplied identifier
// already names. It reports ErrOperationNotFound when the identifier is free to
// be created and ErrOperationConflict when it is in use by anything other than
// this bank's batch.
func (s *PostgresStore) resolveRetainReplay(ctx context.Context, bankID, operationID string) (AsyncRetainSubmission, error) {
	existing, err := s.getOperation(ctx, operationID)
	if errors.Is(err, ErrOperationNotFound) {
		return AsyncRetainSubmission{}, ErrOperationNotFound
	}
	if err != nil {
		return AsyncRetainSubmission{}, err
	}
	if existing.BankID != bankID || existing.OperationType != AsyncRetainType {
		return AsyncRetainSubmission{}, ErrOperationConflict
	}
	return AsyncRetainSubmission{
		OperationID:  operationID,
		ItemsCount:   intFromMetadata(existing.ResultMetadata, "items_count"),
		Deduplicated: true,
	}, nil
}

// insertAsyncRetain writes the parent and its children in one transaction.
func (s *PostgresStore) insertAsyncRetain(ctx context.Context, parent Operation, children []asyncRetainChild) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	parentMetadata, err := marshalMetadata(parent.ResultMetadata)
	if err != nil {
		return err
	}
	// The parent's payload stays NULL: it is not executable work, and a row
	// with no payload is what tells a claim there is nothing to run.
	insert := `INSERT INTO ` + quoteIdent(schema) + `.async_operations
	           (operation_id, bank_id, operation_type, status, result_metadata, task_payload, serialization_key)
	           VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7)`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, insert, parent.OperationID, parent.BankID,
		parent.OperationType, string(parent.Status), parentMetadata, nil, nil); err != nil {
		return err
	}
	for _, child := range children {
		childMetadata, err := marshalMetadata(child.Metadata)
		if err != nil {
			return err
		}
		payload, err := marshalMetadata(child.Payload)
		if err != nil {
			return err
		}
		serializationKey := sql.NullString{String: child.SerializationKey, Valid: child.SerializationKey != ""}
		if _, err := tx.ExecContext(ctx, insert, child.OperationID, parent.BankID,
			OperationTypeRetain, string(StatusPending), childMetadata, payload, serializationKey); err != nil {
			return err
		}
	}
	return tx.Commit()
}

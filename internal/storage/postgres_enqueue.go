package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// enqueueColumns is the shared column list for reading an operation back.
// %[1]s qualifies each column with the alias in scope, so the same list works
// in each place the operation row is read.
const enqueueColumns = `%[1]sid, %[1]sbank_id, COALESCE(%[1]soperation_type,''), %[1]sstatus,
	COALESCE(%[1]serror_message,''), %[1]srun, %[1]screated_at, %[1]supdated_at,
	%[1]scompleted_at, COALESCE(%[1]sretry_count,0), %[1]snext_retry_at,
	%[1]sresult_metadata, COALESCE(%[1]sworker_id,''), %[1]sclaimed_at,
	COALESCE(%[1]stask_payload, 'null'::jsonb), COALESCE(%[1]sserialization_key,'')`

// qualifiedColumns renders the column list against an alias.
func qualifiedColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return fmt.Sprintf(enqueueColumns, prefix)
}

// EnqueueOperation inserts an operation unless work for the same subject is
// already queued or running.
//
// The existence check is part of the INSERT rather than a read in front of it.
// Every process of a deployment runs the loop that calls this and each saw the
// same "nothing in flight" snapshot, so a check performed separately would let
// each one insert its own copy — which is the defect the check exists to
// prevent. Here the statement itself is the arbiter.
func (s *PostgresStore) EnqueueOperation(ctx context.Context, req EnqueueRequest) (EnqueueResult, error) {
	op := req.Operation
	if op.ID == "" {
		op.ID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}
	metadata, err := marshalMetadata(op.ResultMetadata)
	if err != nil {
		return EnqueueResult{}, err
	}
	payload, err := marshalMetadata(op.Payload)
	if err != nil {
		return EnqueueResult{}, err
	}

	// When no subject key is named there is nothing to deduplicate against, so
	// the guard is skipped entirely rather than left as a predicate that can
	// never be false.
	subjectKey := req.DedupeByPayloadKey
	if subjectKey == "" {
		inserted, err := s.insertOperation(ctx, op, metadata, payload)
		return EnqueueResult{Operation: inserted}, err
	}

	blockingStatuses := []string{"pending"}
	if req.DedupeIncludesProcessing {
		blockingStatuses = append(blockingStatuses, "processing")
	}

	table := quoteIdent(s.schema) + ".async_operations"
	// The check and the insert must be atomic, and a bare NOT EXISTS is not: a
	// data-modifying CTE still evaluates its predicate against a snapshot taken
	// when the statement began, so two callers both see no queued row and both
	// insert. Locking the bank row first serializes submits for that bank, and
	// the lock releases when this transaction commits.
	//
	// FOR NO KEY UPDATE rather than FOR UPDATE, because asynchronous operations
	// reference the bank row: every insert for this bank takes a FOR KEY SHARE
	// lock on it, and FOR UPDATE conflicts with that and would block them all
	// for the duration. FOR NO KEY UPDATE conflicts with itself, which is what
	// serializes the submits, but not with FOR KEY SHARE.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var bankPresent int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(s.schema)+`.banks WHERE id = $1 FOR NO KEY UPDATE`,
		op.BankID).Scan(&bankPresent); err != nil {
		// A bank that is not present holds no queued work, and the insert would
		// fail its foreign key anyway, so there is nothing to deduplicate
		// against and no reason to hold the transaction open.
		return EnqueueResult{}, err
	}

	// A running operation is preferred over a merely queued one. Both suppress
	// the insert, but the running one is the work actually under way, so it is
	// the honest thing to report back — and preferring it makes the answer
	// deterministic rather than dependent on row order.
	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM `+table+`
		 WHERE bank_id = $1 AND operation_type = $2
		   AND status = ANY($3::text[])
		   AND task_payload ->> $4 = $5
		 ORDER BY (status = 'processing') DESC, created_at, id
		 LIMIT 1`,
		op.BankID, op.OperationType, blockingStatuses, subjectKey, subjectValue(op.Payload, subjectKey)).Scan(&existingID)
	switch {
	case err == nil:
		existing, readErr := getOperationTx(ctx, tx, s.schema, existingID)
		if readErr != nil {
			return EnqueueResult{}, readErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return EnqueueResult{}, commitErr
		}
		return EnqueueResult{Operation: existing, Deduplicated: true}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return EnqueueResult{}, err
	}

	query := "INSERT INTO " + quoteIdent(s.schema) + ".async_operations" +
		"          (id, bank_id, operation_type, status, error_message, run, retry_count," +
		"           result_metadata, task_payload, serialization_key)" +
		"          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)" +
		"          RETURNING " + qualifiedColumns("")
	var stored Operation
	stored, err = scanOperation(tx.QueryRowContext(ctx, query,
		op.ID, op.BankID, op.OperationType, string(op.Status), op.Error,
		maxInt(op.Run, 1), op.RetryCount, metadata, payload, op.SerializationKey))
	if err != nil {
		return EnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnqueueResult{}, err
	}
	return EnqueueResult{Operation: stored}, nil
}

// getOperationTx reads one operation inside a transaction.
func getOperationTx(ctx context.Context, tx Tx, schema, opID string) (Operation, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+qualifiedColumns("")+`
		 FROM `+quoteIdent(schema)+`.async_operations WHERE id = $1`, opID)
	return scanOperation(row)
}

// insertOperation performs the unguarded insert.
func (s *PostgresStore) insertOperation(ctx context.Context, op Operation, metadata, payload []byte) (Operation, error) {
	query := "INSERT INTO " + quoteIdent(s.schema) + ".async_operations" +
		"          (id, bank_id, operation_type, status, error_message, run, retry_count," +
		"           result_metadata, task_payload, serialization_key)" +
		"          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)" +
		"          RETURNING " + qualifiedColumns("")
	row := s.db.QueryRowContext(ctx, query,
		op.ID, op.BankID, op.OperationType, string(op.Status), op.Error,
		maxInt(op.Run, 1), op.RetryCount, metadata, payload, op.SerializationKey)
	return scanOperation(row)
}

// isSerializationFailure reports whether an error is the database refusing to
// let two transactions interleave.
//
// It is matched structurally rather than through the driver's error type,
// because the storage area must not import a driver: the PostgreSQL driver is
// compiled only under the pgx build tag, so a type assertion against it would
// not compile in the default build. Every PostgreSQL driver reports the SQL
// state on the error, which is the stable part of the contract.
func isSerializationFailure(err error) bool {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		// 40001 serialization_failure, 40P01 deadlock_detected. Both mean the
		// transaction was aborted to preserve correctness and may be retried.
		switch state.SQLState() {
		case "40001", "40P01":
			return true
		}
	}
	return false
}

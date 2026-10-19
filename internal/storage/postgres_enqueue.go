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
const enqueueColumns = `%[1]soperation_id, %[1]sbank_id, COALESCE(%[1]soperation_type,''), %[1]sstatus,
	COALESCE(%[1]serror_message,''), %[1]screated_at, %[1]supdated_at,
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
	if op.OperationID == "" {
		op.OperationID = newID()
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

	return s.enqueueLockedBankTx(ctx, op.BankID, true, func(tx Tx) (EnqueueResult, error) {
		table := quoteIdent(s.schema) + ".async_operations"
		// A running operation is preferred over a merely queued one. Both
		// suppress the insert, but the running one is the work actually under
		// way, so it is the honest thing to report back — and preferring it
		// makes the answer deterministic rather than dependent on row order.
		var existingID string
		err := tx.QueryRowContext(ctx,
			`SELECT operation_id FROM `+table+`
			 WHERE bank_id = $1 AND operation_type = $2
			   AND status = ANY($3::text[])
			   AND task_payload ->> $4 = $5
			 ORDER BY (status = 'processing') DESC, created_at, operation_id
			 LIMIT 1`,
			op.BankID, op.OperationType, blockingStatuses, subjectKey, subjectValue(op.Payload, subjectKey)).Scan(&existingID)
		switch {
		case err == nil:
			existing, readErr := getOperationTx(ctx, tx, s.schema, existingID)
			if readErr != nil {
				return EnqueueResult{}, readErr
			}
			return EnqueueResult{Operation: existing, Deduplicated: true}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return EnqueueResult{}, err
		}

		query := "INSERT INTO " + quoteIdent(s.schema) + ".async_operations" +
			"          (bank_id, operation_type, status, error_message, retry_count," +
			"           result_metadata, task_payload, serialization_key)" +
			"          VALUES ($1,$2,$3,$4,$5,$6,$7,$8)" +
			"          RETURNING " + qualifiedColumns("")
		stored, err := scanOperation(tx.QueryRowContext(ctx, query,
			op.BankID, op.OperationType, string(op.Status), op.Error,
			op.RetryCount, metadata, payload, op.SerializationKey))
		if err != nil {
			return EnqueueResult{}, err
		}
		return EnqueueResult{Operation: stored}, nil
	})
}

// enqueueLockedBankTx runs one conditional enqueue inside a transaction that
// first takes the bank row's FOR NO KEY UPDATE lock, so concurrent submits that
// are conditional on what is already queued for the bank serialize. The check
// and the insert are both performed on that transaction by submit, and the
// transaction commits when submit returns without an error — releasing the lock,
// and making the winner's row visible to the next submitter.
//
// The check-then-insert has to be atomic: a bare NOT EXISTS is not, because a
// data-modifying CTE still evaluates its predicate against the snapshot taken
// when the statement began, so two callers both see no queued row and both
// insert — the defect the guard exists to prevent.
//
// FOR NO KEY UPDATE rather than FOR UPDATE, because asynchronous operations
// reference the bank row: every insert for this bank takes a FOR KEY SHARE lock
// on it, and FOR UPDATE conflicts with that and would block them all for the
// duration. FOR NO KEY UPDATE conflicts with itself, which is what serializes
// the submits, but not with FOR KEY SHARE.
//
// requireBank distinguishes the two bank-existence policies. A caller that
// deduplicates by payload key fails on a missing bank (a FK violation would be
// the only other answer, and the reference reports it as a clean error); a
// caller that deduplicates by bank tolerates it here and lets the insert fail
// its foreign key, which is what an unknown bank used to answer.
func (s *PostgresStore) enqueueLockedBankTx(ctx context.Context, bankID string, requireBank bool, submit func(tx Tx) (EnqueueResult, error)) (EnqueueResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var bankPresent int
	lockErr := tx.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(s.schema)+`.banks WHERE bank_id = $1 FOR NO KEY UPDATE`,
		bankID).Scan(&bankPresent)
	// A bank that is not present holds no queued work, so there is nothing to
	// deduplicate against; callers that require it stop, and callers that do not
	// carry on to the insert, which fails its foreign key.
	if lockErr != nil && (requireBank || !errors.Is(lockErr, sql.ErrNoRows)) {
		return EnqueueResult{}, lockErr
	}

	result, err := submit(tx)
	if err != nil {
		return EnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnqueueResult{}, err
	}
	return result, nil
}

// enqueueDedupeByBank inserts op unless the bank already has a non-terminal
// operation of the same type, in which case it reports the existing one. It is
// the reference's dedupe_by_bank submit — the guard used by bank-wide work
// where any outstanding copy of the pass already covers the intent.
//
// The check-and-insert runs under the bank row lock (see enqueueLockedBankTx),
// so two submits reaching it at the same instant cannot both see no queued row
// and both insert. The reference states the same reasoning for its shared
// submit path.
//
// unscopedOnly deduplicates against a bank-wide pass alone. A pending scoped
// consolidation covers only its own tag subset, so it must not swallow a
// full-bank sweep (the reference's "Dedup only against an existing unscoped
// (full-bank) pending op", engine/memory_engine.py:22207-22211). It applies to
// consolidation; graph maintenance has no scoped sibling.
func (s *PostgresStore) enqueueDedupeByBank(ctx context.Context, op Operation, blockingStatuses []string, unscopedOnly bool) (EnqueueResult, error) {
	if op.OperationID == "" {
		op.OperationID = newID()
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
	// An empty serialization key is NULL rather than an empty string, matching
	// PutOperation: the claim's serialization check matches keys by equality and
	// "" would make every keyless operation contend with every other one.
	serializationKey := sql.NullString{String: op.SerializationKey, Valid: op.SerializationKey != ""}
	return s.enqueueLockedBankTx(ctx, op.BankID, false, func(tx Tx) (EnqueueResult, error) {
		var existingID string
		// A scoped consolidation carries observation_scopes in its payload; the
		// JSON predicate is spelled out here rather than left to a portable
		// helper because the reference keeps the same check in Python for the
		// same reason (Oracle's JSON_VALUE returns NULL for the array form).
		unscoped := ""
		if unscopedOnly {
			unscoped = ` AND task_payload->'observation_scopes' IS NULL`
		}
		err := tx.QueryRowContext(ctx,
			`SELECT operation_id FROM `+quoteIdent(s.schema)+`.async_operations
			 WHERE bank_id = $1 AND operation_type = $2 AND status = ANY($3::text[])`+unscoped+`
			 ORDER BY created_at, operation_id
			 LIMIT 1`,
			op.BankID, op.OperationType, blockingStatuses).Scan(&existingID)
		switch {
		case err == nil:
			existing, readErr := getOperationTx(ctx, tx, s.schema, existingID)
			if readErr != nil {
				return EnqueueResult{}, readErr
			}
			return EnqueueResult{Operation: existing, Deduplicated: true}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return EnqueueResult{}, err
		}
		query := "INSERT INTO " + quoteIdent(s.schema) + ".async_operations" +
			"          (operation_id, bank_id, operation_type, status, error_message, retry_count," +
			"           result_metadata, task_payload, serialization_key)" +
			"          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)" +
			"          RETURNING " + qualifiedColumns("")
		stored, err := scanOperation(tx.QueryRowContext(ctx, query,
			op.OperationID, op.BankID, op.OperationType, string(op.Status), op.Error,
			op.RetryCount, metadata, payload, serializationKey))
		if err != nil {
			return EnqueueResult{}, err
		}
		return EnqueueResult{Operation: stored}, nil
	})
}

// getOperationTx reads one operation inside a transaction.
func getOperationTx(ctx context.Context, tx Tx, schema, opID string) (Operation, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+qualifiedColumns("")+`
		 FROM `+quoteIdent(schema)+`.async_operations WHERE operation_id = $1`, opID)
	return scanOperation(row)
}

// insertOperation performs the unguarded insert.
func (s *PostgresStore) insertOperation(ctx context.Context, op Operation, metadata, payload []byte) (Operation, error) {
	query := "INSERT INTO " + quoteIdent(s.schema) + ".async_operations" +
		"          (bank_id, operation_type, status, error_message, retry_count," +
		"           result_metadata, task_payload, serialization_key)" +
		"          VALUES ($1,$2,$3,$4,$5,$6,$7,$8)" +
		"          RETURNING " + qualifiedColumns("")
	row := s.db.QueryRowContext(ctx, query,
		op.BankID, op.OperationType, string(op.Status), op.Error,
		op.RetryCount, metadata, payload, op.SerializationKey)
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

// isUniqueViolation reports whether a statement lost a race on a unique
// constraint. A writer that reads first and inserts second can always be beaten
// to the insert by a concurrent one, so the loser retries against what the
// winner wrote rather than failing the caller's work.
func isUniqueViolation(err error) bool {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return false
	}
	return state.SQLState() == "23505"
}

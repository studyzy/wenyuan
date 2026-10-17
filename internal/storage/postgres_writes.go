package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// RecordWrite persists a memory write honouring operation-id idempotency. The
// operation identifier column is uniquely constrained, so a reused identifier
// surfaces as a conflict rather than a duplicate row.
func (s *PostgresStore) RecordWrite(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if _, err := s.GetBank(ctx, req.BankID); err != nil {
		return WriteResult{}, err
	}
	if req.OperationID != "" {
		var existing string
		query := `SELECT operation_id FROM ` + quoteIdent(s.schema) + `.async_operations WHERE operation_id = $1`
		err := s.db.QueryRowContext(ctx, query, req.OperationID).Scan(&existing)
		if err == nil {
			return WriteResult{OperationID: existing}, ErrOperationConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return WriteResult{}, err
		}
	}
	memoryID := req.OperationID
	if memoryID == "" {
		memoryID = newID()
	}
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.memory_units
	           (id, bank_id, text, search_vector, fact_type, metadata, document_id, tags)
	           VALUES ($1,$2,$3,` + searchVectorExpr("$3", "''", "''") + `,$4,$5,$6,$7)`
	if _, err := s.db.ExecContext(ctx, insert, memoryID, req.BankID, req.Content,
		string(TypeWorld), "{}", nullString(req.DocumentID), req.Tags); err != nil {
		return WriteResult{}, err
	}
	result := WriteResult{OperationID: resultOpID(req, memoryID), MemoryID: memoryID, Status: StatusCompleted}

	opID := req.OperationID
	if opID == "" {
		opID = memoryID
	}
	op := Operation{OperationID: opID, BankID: req.BankID, Status: StatusCompleted, Run: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if req.Async {
		op.Status = StatusPending
		result.Status = StatusPending
		result.Accepted = true
	}
	if _, err := s.PutOperation(ctx, op); err != nil {
		return WriteResult{}, err
	}
	return result, nil
}

func resultOpID(req WriteRequest, fallback string) string {
	if req.OperationID != "" {
		return req.OperationID
	}
	return fallback
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *PostgresStore) Search(ctx context.Context, req RecallRequest) (RecallResult, error) {
	if _, err := s.GetBank(ctx, req.BankID); err != nil {
		return RecallResult{}, err
	}
	query := `SELECT id, text, fact_type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND ($2 = '' OR text ILIKE '%' || $2 || '%')
	          LIMIT $3`
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.reader().QueryContext(ctx, query, req.BankID, req.Query, limit)
	if err != nil {
		return RecallResult{}, err
	}
	defer func() { _ = rows.Close() }()
	matches := make([]RecallMatch, 0)
	for rows.Next() {
		var m RecallMatch
		var occurredStart, occurredEnd, mentionedAt sql.NullTime
		var chunkID, documentID sql.NullString
		if err := rows.Scan(&m.ID, &m.Text, &m.Type, &occurredStart, &occurredEnd, &mentionedAt, &chunkID, &documentID); err != nil {
			return RecallResult{}, err
		}
		m.OccurredStart = nullTimePtr(occurredStart)
		m.OccurredEnd = nullTimePtr(occurredEnd)
		m.MentionedAt = nullTimePtr(mentionedAt)
		m.ChunkID = chunkID.String
		m.DocumentID = documentID.String
		matches = append(matches, m)
	}
	return RecallResult{Matches: matches}, rows.Err()
}

func (s *PostgresStore) Synthesis(ctx context.Context, req ReflectRequest) (ReflectContext, error) {
	result, err := s.Search(ctx, RecallRequest{BankID: req.BankID, Query: req.Query, Limit: req.Limit})
	if err != nil {
		return ReflectContext{}, err
	}
	return ReflectContext{Matches: result.Matches, Trace: map[string]any{"retrieved": len(result.Matches)}}, nil
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func (s *PostgresStore) PutOperation(ctx context.Context, op Operation) (Operation, error) {
	if op.OperationID == "" {
		op.OperationID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}
	metadata, err := marshalMetadata(op.ResultMetadata)
	if err != nil {
		return Operation{}, err
	}
	payload, err := marshalMetadata(op.Payload)
	if err != nil {
		return Operation{}, err
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.async_operations
	          (operation_id, bank_id, operation_type, status, error_message, retry_count, result_metadata, task_payload, serialization_key)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	          ON CONFLICT (operation_id) DO UPDATE SET status=$4, error_message=$5, updated_at=now(),
	              result_metadata=$7, task_payload=$8, serialization_key=$9
	          RETURNING ` + operationColumns
	// An empty key is NULL rather than an empty string: the claim's serialization
	// check matches keys by equality, and "" would make every keyless operation
	// contend with every other one.
	serializationKey := sql.NullString{String: op.SerializationKey, Valid: op.SerializationKey != ""}
	row := s.db.QueryRowContext(ctx, query, op.OperationID, op.BankID, op.OperationType, string(op.Status), op.Error, op.RetryCount, metadata, payload, serializationKey)
	return scanOperation(row)
}

// operationColumns is the column list every statement that returns an
// operation must use. It is written once because scanOperation reads exactly
// these 15 columns in this order: a statement that returns fewer produces a
// scan error at run time rather than a compile error, which is how the cancel
// and retry paths were broken.
const operationColumns = `operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''),
	          created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at,
	          result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb),
	          COALESCE(serialization_key,'')`

// GetOperation returns one operation by identifier, scoped to the bank; an
// identifier belonging to another bank reads as ErrOperationNotFound.
func (s *PostgresStore) GetOperation(ctx context.Context, bankID, opID string) (Operation, error) {
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
//
// It reads on the primary: every caller is a state transition that reads the
// current status and then writes the next one, and a replica that lags by even
// one commit would let a terminal operation be advanced a second time.
func (s *PostgresStore) getOperation(ctx context.Context, opID string) (Operation, error) {
	query := `SELECT ` + operationColumns + `
	          FROM ` + quoteIdent(s.schema) + `.async_operations WHERE operation_id = $1`
	row := s.db.QueryRowContext(ctx, query, opID)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	return op, err
}

// OperationAlive reports whether the identified operation exists, scoped to the
// bank, and is not cancelled. A missing row reads as not alive rather than
// faulting — a bank delete removes the row by foreign-key cascade, and the
// running task is what notices.
//
// It reads on the primary for the same reason the state transitions do: a
// replica lagging behind a cancel or a bank delete would report the work alive
// when it is not.
func (s *PostgresStore) OperationAlive(ctx context.Context, bankID, opID string) (bool, error) {
	query := `SELECT bank_id, status FROM ` + quoteIdent(s.schema) + `.async_operations
	          WHERE operation_id = $1`
	var scannedBank, status string
	err := s.db.QueryRowContext(ctx, query, opID).Scan(&scannedBank, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if bankID != "" && scannedBank != bankID {
		return false, nil
	}
	return status != string(StatusCancelled), nil
}

// GetOperationChildren returns the child operations of a parent batch, ordered
// by sub-batch index.
func (s *PostgresStore) GetOperationChildren(ctx context.Context, bankID, opID string) ([]ChildOperation, error) {
	query := `SELECT operation_id::text, status, error_message,
	                 (result_metadata->>'sub_batch_index')::int,
	                 (result_metadata->>'items_count')::int
	          FROM ` + quoteIdent(s.schema) + `.async_operations
	          WHERE bank_id = $1 AND result_metadata->>'parent_operation_id' = $2
	          ORDER BY (result_metadata->>'sub_batch_index')::int ASC, operation_id ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, opID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	children := make([]ChildOperation, 0)
	for rows.Next() {
		var child ChildOperation
		var status string
		var subBatchIndex, itemsCount sql.NullInt64
		var errorMessage sql.NullString
		if err := rows.Scan(&child.OperationID, &status, &errorMessage, &subBatchIndex, &itemsCount); err != nil {
			return nil, err
		}
		child.Status = OperationStatus(status)
		if subBatchIndex.Valid {
			value := int(subBatchIndex.Int64)
			child.SubBatchIndex = &value
		}
		if itemsCount.Valid {
			value := int(itemsCount.Int64)
			child.ItemsCount = &value
		}
		if errorMessage.Valid && errorMessage.String != "" {
			value := errorMessage.String
			child.ErrorMessage = &value
		}
		children = append(children, child)
	}
	return children, rows.Err()
}

// ListOperations returns one page of operations matching the query, most
// recently created first, with the pre-paging total.
func (s *PostgresStore) ListOperations(ctx context.Context, query OperationQuery) (OperationPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	// The two statements number their parameters differently: the count has no
	// LIMIT/OFFSET, so its parent flag is $5 where the page's is $7. A parameter
	// the server cannot find a use for has no inferable type, and PostgreSQL
	// rejects the statement outright, so each statement is built with the
	// numbering it actually uses rather than sharing one filter verbatim.
	const parentPredicate = `(NOT $%[1]d::boolean OR NOT COALESCE(result_metadata->>'is_parent', 'false') = 'true')`
	filter := `($1='' OR bank_id=$1) AND ($2='' OR status=$2) AND ($3='' OR operation_type=$3)
	           AND ($4='' OR worker_id=$4)`
	countFilter := filter + ` AND ` + fmt.Sprintf(parentPredicate, 5)
	countQuery := `SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.async_operations WHERE ` + countFilter
	var total int
	if err := s.reader().QueryRowContext(ctx, countQuery, query.BankID, query.Status, query.Type, query.WorkerID,
		query.ExcludeParents).Scan(&total); err != nil {
		return OperationPage{}, err
	}
	listFilter := filter + ` AND ` + fmt.Sprintf(parentPredicate, 7)
	listQuery := `SELECT ` + operationColumns + `
	              FROM ` + quoteIdent(s.schema) + `.async_operations
	              WHERE ` + listFilter + `
	              ORDER BY created_at DESC, operation_id ASC LIMIT $5 OFFSET $6`
	rows, err := s.reader().QueryContext(ctx, listQuery, query.BankID, query.Status, query.Type, query.WorkerID, limit, query.Offset, query.ExcludeParents)
	if err != nil {
		return OperationPage{}, err
	}
	defer func() { _ = rows.Close() }()
	ops := make([]Operation, 0)
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return OperationPage{}, err
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return OperationPage{}, err
	}
	return OperationPage{Operations: ops, Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// CancelOperation cancels a non-terminal operation, returning
// ErrOperationConflict when the operation is already terminal.
func (s *PostgresStore) CancelOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	current, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if current.BankID != bankID {
		return Operation{}, ErrOperationNotFound
	}
	if current.Status.Terminal() {
		return current, ErrOperationConflict
	}
	// The reference's cancel stamps the status and updated_at only: completed_at
	// stays null for a cancelled operation, so the read model reports the status
	// change without inventing a completion time.
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, updated_at=now()
	          WHERE operation_id=$1 AND status IN ('pending','processing')
	          RETURNING ` + operationColumns
	cancelled, err := scanOperation(s.db.QueryRowContext(ctx, query, opID, string(StatusCancelled)))
	if errors.Is(err, sql.ErrNoRows) {
		// The row reached a terminal state between the read above and this
		// write: the concurrent terminal write wins the race, and the caller is
		// told the operation could not be cancelled rather than being handed a
		// cancellation that never happened.
		current, getErr := s.getOperation(ctx, opID)
		if getErr != nil {
			return Operation{}, getErr
		}
		return current, ErrOperationConflict
	}
	return cancelled, err
}

func (s *PostgresStore) AdvanceOperation(ctx context.Context, opID string, next OperationStatus, errMsg string) (Operation, error) {
	current, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if !validTransition(current.Status, next) {
		return current, ErrOperationConflict
	}
	assign := ""
	if next.Terminal() {
		// A terminal operation is finished with, so its claim is cleared at
		// the same moment: a completed row that still looks held would mislead
		// a shutdown scan into trying to hand it back.
		assign = ", completed_at=now(), worker_id=NULL, claimed_at=NULL"
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, error_message=$3, updated_at=now()` + assign + ` WHERE operation_id=$1
	          RETURNING ` + operationColumns
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(next), errMsg))
}

// AdvanceOperationWithResult advances an operation and merges result metadata
// onto it in the same statement, implementing OperationResultRecorder. The JSON
// merge (||) leaves any metadata written when the operation was enqueued in
// place, so a caller joining on those keys keeps working, and the success-path
// metadata is written before the row is marked completed.
func (s *PostgresStore) AdvanceOperationWithResult(ctx context.Context, opID string, next OperationStatus, errMsg string, result map[string]any) (Operation, error) {
	current, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if !validTransition(current.Status, next) {
		return current, ErrOperationConflict
	}
	metadata, err := marshalMetadata(result)
	if err != nil {
		return Operation{}, err
	}
	assign := ""
	if next.Terminal() {
		// A terminal operation is finished with, so its claim is cleared at
		// the same moment: a completed row that still looks held would mislead
		// a shutdown scan into trying to hand it back.
		assign = ", completed_at=now(), worker_id=NULL, claimed_at=NULL"
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, error_message=$3, result_metadata=(COALESCE(result_metadata, '{}'::jsonb) || $4::jsonb), updated_at=now()` + assign + ` WHERE operation_id=$1
	          RETURNING ` + operationColumns
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(next), errMsg, metadata))
}

// RetryOperation starts a new run of a failed operation.
//
// A failed batch parent is retried by re-running the work that failed beneath
// it, the same rule the in-memory backend applies: the parent holds no task
// payload of its own, so reviving it alone would queue a row nothing executes.
// Its failed and cancelled children go back to pending with their stored
// payloads intact, and the parent is revived only when at least one child
// remains to drive the roll-up — a parent left pending with nothing beneath it
// would never reach a terminal status again.
func (s *PostgresStore) RetryOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	current, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if current.BankID != bankID {
		return Operation{}, ErrOperationNotFound
	}
	if current.Status != StatusFailed && current.Status != StatusCancelled {
		return current, ErrOperationConflict
	}
	if current.OperationType == AsyncRetainType {
		return s.retryBatchParent(ctx, current)
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, error_message=NULL, completed_at=NULL, next_retry_at=NULL,
	              worker_id=NULL, claimed_at=NULL, retry_count=0, updated_at=now() WHERE operation_id=$1
	          RETURNING ` + operationColumns
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(StatusPending)))
}

// retryBatchParent requeues a failed batch's children and revives the parent
// over them.
func (s *PostgresStore) retryBatchParent(ctx context.Context, parent Operation) (Operation, error) {
	qualified := quoteIdent(s.schema)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The children are locked before the parent, so two retries of one batch
	// cannot both decide they are the one reviving it. The filter is built as
	// JSON rather than interpolated into a literal, because a JSON string needs
	// double quotes and the statement's parameter is a jsonb.
	childFilter, err := marshalMetadata(map[string]any{"parent_operation_id": parent.OperationID})
	if err != nil {
		return Operation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+qualified+`.async_operations
	    SET status='pending', error_message='', completed_at=NULL, next_retry_at=NULL,
	        worker_id=NULL, claimed_at=NULL, retry_count=0, updated_at=now()
	    WHERE bank_id=$1 AND result_metadata @> $2::jsonb AND status IN ('failed','cancelled')`,
		parent.BankID, childFilter); err != nil {
		return Operation{}, err
	}
	var outstanding bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+qualified+`.async_operations child
	    WHERE child.bank_id=$1 AND child.result_metadata @> $2::jsonb AND child.status <> 'completed'
	      AND child.status <> 'failed' AND child.status <> 'cancelled')`,
		parent.BankID, childFilter).Scan(&outstanding); err != nil {
		return Operation{}, err
	}
	if !outstanding {
		return parent, ErrOperationConflict
	}
	revived, err := scanOperation(tx.QueryRowContext(ctx, `UPDATE `+qualified+`.async_operations
	    SET status='pending', error_message='', completed_at=NULL, next_retry_at=NULL,
	        worker_id=NULL, claimed_at=NULL, retry_count=0, updated_at=now()
	    WHERE operation_id=$1
	    RETURNING `+operationColumns, parent.OperationID))
	if err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, err
	}
	return revived, nil
}

// DeleteOperation permanently removes a terminal operation row. The delete is
// status-guarded inside the statement so it cannot race a concurrent retry
// between a read and a write. An operation that is still pending or running is
// reported as ErrOperationNotTerminal rather than silently removed.
func (s *PostgresStore) DeleteOperation(ctx context.Context, bankID, opID string) (Operation, error) {
	qualified := quoteIdent(s.schema)
	// The reference scopes the delete to the bank as well as the identifier, so
	// an operation belonging to another bank reads as not found here.
	query := `DELETE FROM ` + qualified + `.async_operations
	          WHERE operation_id = $1 AND bank_id = $2
	            AND status IN ('failed', 'cancelled', 'completed')
	          RETURNING ` + operationColumns
	deleted, err := scanOperation(s.db.QueryRowContext(ctx, query, opID, bankID))
	if err == nil {
		s.deleteOperationExportArchive(ctx, deleted.ResultMetadata)
		return deleted, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	// Nothing was removed. Distinguish a missing row from a live one, so the
	// caller can answer 404 and 409 respectively.
	current, getErr := s.getOperation(ctx, opID)
	if getErr != nil || current.BankID != bankID {
		return Operation{}, ErrOperationNotFound
	}
	return current, ErrOperationNotTerminal
}

// RepairPendingBatchParent persists the corrected terminal status a read
// computed for a parent batch operation left "pending" though every child has
// finished. The correction is the reference's lazy read-path repair: it writes
// status, updated_at and completed_at and nothing else — no outcome counters, no
// error message. The write is guarded on the stored status still being
// "pending", so a concurrent child promotion wins the race and this becomes a
// no-op rather than re-terminalising the row.
func (s *PostgresStore) RepairPendingBatchParent(ctx context.Context, bankID, parentID string, status OperationStatus) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$3, updated_at=now(), completed_at=now()
	          WHERE operation_id=$1 AND bank_id=$2 AND status='pending'`
	_, err := s.db.ExecContext(ctx, query, parentID, bankID, string(status))
	return err
}

// deleteOperationExportArchive removes the stored archive an export operation
// owns. The archive's only handle is the row's result_metadata.storage_key, so
// it must be removed with the row or it outlives its only reference. A
// non-export operation carries no key and nothing happens. Best-effort: a
// failure to delete the blob is logged, not returned, so cleanup never turns a
// standing delete into an error, and the row is already gone either way.
//
// The metadata is taken directly rather than the whole operation because the
// retention prune reads only result_metadata from the rows it is about to
// remove, and both callers share this one delete.
func (s *PostgresStore) deleteOperationExportArchive(ctx context.Context, metadata map[string]any) {
	key, _ := metadata[OperationPayloadKeyStoredFile].(string)
	if key == "" {
		return
	}
	if err := s.DeleteStoredFile(ctx, key); err != nil {
		slog.Default().Warn("failed to delete export archive", "storage_key", key, "error", err)
	}
}

func scanOperation(row rowScanner) (Operation, error) {
	var op Operation
	var status string
	var completedAt, nextRetryAt, claimedAt sql.NullTime
	var metadata, payload []byte
	if err := row.Scan(&op.OperationID, &op.BankID, &op.OperationType, &status, &op.Error, &op.CreatedAt, &op.UpdatedAt, &completedAt, &op.RetryCount, &nextRetryAt, &metadata, &op.WorkerID, &claimedAt, &payload, &op.SerializationKey); err != nil {
		return Operation{}, err
	}
	op.Status = OperationStatus(status)
	op.CompletedAt = nullTimePtr(completedAt)
	op.NextRetryAt = nullTimePtr(nextRetryAt)
	op.ClaimedAt = nullTimePtr(claimedAt)
	rest, err := unmarshalResultMetadata(metadata)
	if err != nil {
		return Operation{}, err
	}
	op.ResultMetadata = rest
	op.Payload = decodeJSONObject(payload)
	op.IsParent = operationIsParent(rest)
	return op, nil
}

// scanOperationWithClaim reads the row shape returned by the claim update,
// which appends the claim columns and the payload.
func scanOperationWithClaim(row rowScanner) (Operation, error) {
	return scanOperation(row)
}

// decodeJSONObject turns a jsonb column into a map, tolerating null.
func decodeJSONObject(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

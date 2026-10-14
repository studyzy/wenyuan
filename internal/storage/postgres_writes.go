package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	           VALUES ($1,$2,$3,` + searchVectorSQL + `,$4,$5,$6,$7)`
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
	rows, err := s.db.QueryContext(ctx, query, req.BankID, req.Query, limit)
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
	          (operation_id, bank_id, operation_type, status, error_message, retry_count, result_metadata, task_payload)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	          ON CONFLICT (operation_id) DO UPDATE SET status=$4, error_message=$5, updated_at=now(),
	              result_metadata=$7, task_payload=$8
	          RETURNING operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb), COALESCE(serialization_key,'')`
	row := s.db.QueryRowContext(ctx, query, op.OperationID, op.BankID, op.OperationType, string(op.Status), op.Error, op.RetryCount, metadata, payload)
	return scanOperation(row)
}

func (s *PostgresStore) GetOperation(ctx context.Context, opID string) (Operation, error) {
	query := `SELECT operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb), COALESCE(serialization_key,'')
	          FROM ` + quoteIdent(s.schema) + `.async_operations WHERE operation_id = $1`
	row := s.db.QueryRowContext(ctx, query, opID)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	return op, err
}

// ListOperations returns one page of operations matching the query, most
// recently created first, with the pre-paging total.
func (s *PostgresStore) ListOperations(ctx context.Context, query OperationQuery) (OperationPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	filter := `($1='' OR bank_id=$1) AND ($2='' OR status=$2) AND ($3='' OR operation_type=$3)
	           AND ($4='' OR worker_id=$4)`
	countQuery := `SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.async_operations WHERE ` + filter
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, query.BankID, query.Status, query.Type, query.WorkerID).Scan(&total); err != nil {
		return OperationPage{}, err
	}
	listQuery := `SELECT operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb), COALESCE(serialization_key,'')
	              FROM ` + quoteIdent(s.schema) + `.async_operations
	              WHERE ` + filter + `
	              ORDER BY created_at DESC, operation_id ASC LIMIT $5 OFFSET $6`
	rows, err := s.db.QueryContext(ctx, listQuery, query.BankID, query.Status, query.Type, query.WorkerID, limit, query.Offset)
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
func (s *PostgresStore) CancelOperation(ctx context.Context, opID string) (Operation, error) {
	current, err := s.GetOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if current.Status.Terminal() {
		return current, ErrOperationConflict
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, completed_at=now(), updated_at=now() WHERE operation_id=$1
	          RETURNING operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at`
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(StatusCancelled)))
}

func (s *PostgresStore) AdvanceOperation(ctx context.Context, opID string, next OperationStatus, errMsg string) (Operation, error) {
	current, err := s.GetOperation(ctx, opID)
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
	          RETURNING operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb), COALESCE(serialization_key,'')`
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(next), errMsg))
}

func (s *PostgresStore) RetryOperation(ctx context.Context, opID string) (Operation, error) {
	current, err := s.GetOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	if current.Status != StatusFailed {
		return current, ErrOperationConflict
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, error_message='', retry_count=COALESCE(retry_count,0)+1, updated_at=now() WHERE operation_id=$1
	          RETURNING operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at`
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(StatusPending)))
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
	          RETURNING operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb), COALESCE(serialization_key,'')`
	deleted, err := scanOperation(s.db.QueryRowContext(ctx, query, opID, bankID))
	if err == nil {
		return deleted, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	// Nothing was removed. Distinguish a missing row from a live one, so the
	// caller can answer 404 and 409 respectively.
	current, getErr := s.GetOperation(ctx, opID)
	if getErr != nil || current.BankID != bankID {
		return Operation{}, ErrOperationNotFound
	}
	return current, ErrOperationNotTerminal
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

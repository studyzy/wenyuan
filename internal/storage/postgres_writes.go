package storage

import (
	"context"
	"database/sql"
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
		query := `SELECT id FROM ` + quoteIdent(s.schema) + `.async_operations WHERE id = $1`
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
	           (id, bank_id, text, type, metadata, document_id, tags)
	           VALUES ($1,$2,$3,$4,$5,$6,$7)`
	if _, err := s.db.ExecContext(ctx, insert, memoryID, req.BankID, req.Content,
		string(TypeWorld), "{}", nullString(req.DocumentID), req.Tags); err != nil {
		return WriteResult{}, err
	}
	result := WriteResult{OperationID: resultOpID(req, memoryID), MemoryID: memoryID, Status: StatusCompleted}

	opID := req.OperationID
	if opID == "" {
		opID = memoryID
	}
	op := Operation{ID: opID, BankID: req.BankID, Status: StatusCompleted, Run: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
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
	query := `SELECT id, text, type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id
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
	if op.ID == "" {
		op.ID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}
	// The operation payload rides in the existing result_metadata column so the
	// queued work can be described without a schema change. A non-nil payload
	// is stored under a single wrapper key, leaving room for result metadata
	// written by a future consumer.
	metadata := op.ResultMetadata
	if len(op.Payload) > 0 {
		metadata = map[string]any{}
		for k, v := range op.ResultMetadata {
			metadata[k] = v
		}
		metadata[payloadMetadataKey] = op.Payload
	}
	encoded, err := marshalMetadata(metadata)
	if err != nil {
		return Operation{}, err
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.async_operations
	          (id, bank_id, operation_type, status, error_message, run, retry_count, result_metadata)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	          ON CONFLICT (id) DO UPDATE SET status=$4, error_message=$5, updated_at=now(), result_metadata=$8
	          RETURNING id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run, created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata`
	row := s.db.QueryRowContext(ctx, query, op.ID, op.BankID, op.OperationType, string(op.Status), op.Error, maxInt(op.Run, 1), op.RetryCount, encoded)
	return scanOperation(row)
}

func (s *PostgresStore) GetOperation(ctx context.Context, opID string) (Operation, error) {
	query := `SELECT id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run, created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at, result_metadata
	          FROM ` + quoteIdent(s.schema) + `.async_operations WHERE id = $1`
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
	countQuery := `SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.async_operations
	               WHERE bank_id=$1 AND ($2='' OR status=$2) AND ($3='' OR operation_type=$3)`
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, query.BankID, query.Status, query.Type).Scan(&total); err != nil {
		return OperationPage{}, err
	}
	listQuery := `SELECT id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run, created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at
	              FROM ` + quoteIdent(s.schema) + `.async_operations
	              WHERE bank_id=$1 AND ($2='' OR status=$2) AND ($3='' OR operation_type=$3)
	              ORDER BY created_at DESC, id ASC LIMIT $4 OFFSET $5`
	rows, err := s.db.QueryContext(ctx, listQuery, query.BankID, query.Status, query.Type, limit, query.Offset)
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
	          SET status=$2, completed_at=now(), updated_at=now() WHERE id=$1
	          RETURNING id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run, created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at`
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
		assign = ", completed_at=now()"
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.async_operations
	          SET status=$2, error_message=$3, updated_at=now()` + assign + ` WHERE id=$1
	          RETURNING id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run, created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at`
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
	          SET status=$2, error_message='', run=run+1, retry_count=COALESCE(retry_count,0)+1, updated_at=now() WHERE id=$1
	          RETURNING id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run, created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at`
	return scanOperation(s.db.QueryRowContext(ctx, query, opID, string(StatusPending)))
}

func scanOperation(row rowScanner) (Operation, error) {
	var op Operation
	var status string
	var completedAt, nextRetryAt sql.NullTime
	var metadata []byte
	if err := row.Scan(&op.ID, &op.BankID, &op.OperationType, &status, &op.Error, &op.Run, &op.CreatedAt, &op.UpdatedAt, &completedAt, &op.RetryCount, &nextRetryAt, &metadata); err != nil {
		return Operation{}, err
	}
	op.Status = OperationStatus(status)
	op.CompletedAt = nullTimePtr(completedAt)
	op.NextRetryAt = nullTimePtr(nextRetryAt)
	payload, rest, err := unmarshalMetadata(metadata)
	if err != nil {
		return Operation{}, err
	}
	op.Payload = payload
	op.ResultMetadata = rest
	return op, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package storage

import (
	"context"
	"encoding/json"
)

// AppendDocumentEvents appends document change-history events to an operation's
// result_metadata.document_events.
//
// The reference merges in Python under a row lock rather than with an in-place
// jsonb append (`document_history.py:198-224`), because its Oracle rewriter
// cannot express the append and an operation row is written rarely enough that
// the lock costs nothing. The same read-merge-write is done here, in one
// statement: the list is replaced by itself with the new events appended, so a
// concurrent writer's events survive instead of being overwritten by this
// read's copy.
func (s *PostgresStore) AppendDocumentEvents(ctx context.Context, opID string, events []map[string]any) error {
	if opID == "" || len(events) == 0 {
		return nil
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	appended, err := json.Marshal(events)
	if err != nil {
		return err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET result_metadata = jsonb_set(
	                  COALESCE(result_metadata, '{}'::jsonb),
	                  '{` + DocumentEventKey + `}',
	                  COALESCE(result_metadata->'` + DocumentEventKey + `', '[]'::jsonb) || $2::jsonb),
	              updated_at = now()
	          WHERE operation_id = $1`
	_, err = s.db.ExecContext(ctx, query, opID, appended)
	return err
}

// ListDocumentEvents reads a document's change history out of the bank's
// operations, newest first.
//
// The match is the reference's containment test on the event list
// (`result_metadata->'document_events' @> '[{"document_id": …}]'`), which no
// index serves: the scan is bounded by operation retention, exactly as upstream
// bounds it. The page bound is on operations, ordered by the operation's
// created_at, and the events of the paged operations are then flattened and
// ordered by their own created_at.
func (s *PostgresStore) ListDocumentEvents(ctx context.Context, bankID, documentID string, limit, offset int) (DocumentEventPage, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return DocumentEventPage{}, schemaErr
	}
	needle, err := json.Marshal([]map[string]any{{"document_id": documentID}})
	if err != nil {
		return DocumentEventPage{}, err
	}
	match := `result_metadata->'` + DocumentEventKey + `' @> $2::jsonb`
	qualified := quoteIdent(schema)
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.async_operations WHERE bank_id = $1 AND `+match,
		bankID, needle).Scan(&total); err != nil {
		return DocumentEventPage{}, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT operation_id, result_metadata FROM `+qualified+`.async_operations
		 WHERE bank_id = $1 AND `+match+`
		 ORDER BY created_at DESC
		 LIMIT $3 OFFSET $4`,
		bankID, needle, limit, offset)
	if err != nil {
		return DocumentEventPage{}, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]StoredDocumentEvent, 0)
	for rows.Next() {
		var (
			operationID string
			metadata    []byte
		)
		if err := rows.Scan(&operationID, &metadata); err != nil {
			return DocumentEventPage{}, err
		}
		events = append(events, documentEventsFor(operationID, decodeJSONObject(metadata), documentID)...)
	}
	if err := rows.Err(); err != nil {
		return DocumentEventPage{}, err
	}
	SortStoredDocumentEvents(events)
	return DocumentEventPage{
		DocumentID: documentID, Total: total, Limit: limit, Offset: offset, Events: events,
	}, nil
}

// DocumentFactSources returns fact id -> document id for the given facts, read
// from the live and the invalidated views: an invalidated fact has left the live
// view but not the bank, and a consolidation that named it still attributes to
// its document. Ids the driver cannot cast to a uuid name no row, so they are
// dropped before the query rather than failing it — the caller is recording
// history, which must never fail the change it describes.
func (s *PostgresStore) DocumentFactSources(ctx context.Context, bankID string, factIDs []string) (map[string]string, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	ids := make([]string, 0, len(factIDs))
	for _, id := range factIDs {
		if isUUIDValue(id) {
			ids = append(ids, id)
		}
	}
	documents := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return documents, nil
	}
	qualified := quoteIdent(schema)
	for _, table := range []string{"memory_units", "invalidated_memory_units"} {
		rows, err := s.db.QueryContext(ctx,
			`SELECT id::text, COALESCE(document_id, '') FROM `+qualified+`.`+table+`
			 WHERE bank_id = $1 AND id = ANY($2::uuid[])`, bankID, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var factID, documentID string
			if err := rows.Scan(&factID, &documentID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if documentID != "" {
				documents[factID] = documentID
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return documents, nil
}

// isUUIDValue reports whether the value can be read as a uuid, which is the
// shape a memory-unit or operation identifier is stored in.
func isUUIDValue(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !isHexDigit(byte(char)) {
				return false
			}
		}
	}
	return true
}

// isHexDigit reports whether the byte is a hexadecimal digit.
func isHexDigit(char byte) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')
}

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// ListArchivedMemories returns a bank's curation archive as rows, oldest first.
//
// The rows are the table's own columns, read through row_to_json: the archive
// holds a unit's entity ids, its causal links and its invalidation metadata,
// none of which the live unit projection carries, so projecting the rows here
// would drop them before a transfer ever saw them.
func (s *PostgresStore) ListArchivedMemories(ctx context.Context, bankID string) ([]map[string]any, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []map[string]any
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	query := `SELECT row_to_json(mu) FROM (
	            SELECT * FROM ` + qualified + `.invalidated_memory_units
	            WHERE bank_id = $1
	            ORDER BY invalidated_at, id
	          ) mu`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row map[string]any
		if err := decodeArchiveRow(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PutArchivedMemory inserts one archived row. The row is poured into the table
// through its own composite type, so a column the archive did not carry takes
// its default rather than shifting the others, and an id already archived is
// left alone.
func (s *PostgresStore) PutArchivedMemory(ctx context.Context, row map[string]any) (bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 bool
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	data, err := json.Marshal(row)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.invalidated_memory_units
		 SELECT * FROM json_populate_record(NULL::`+qualified+`.invalidated_memory_units, $1::json)
		 ON CONFLICT (id) DO NOTHING`, string(data))
	if err != nil {
		return false, fmt.Errorf("insert archived memory: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return inserted > 0, nil
}

// decodeArchiveRow decodes one JSON object, keeping numbers as their literal
// text so an integer column is not rounded through a float.
func decodeArchiveRow(raw []byte, target *map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(target)
}

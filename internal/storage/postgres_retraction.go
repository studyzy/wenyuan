package storage

import "context"

// LiveMemoryIDs reports which of the given identifiers still exist as memory
// units of the bank. One query answers the whole set, because a model may cite
// a hundred facts and the check runs on every staleness test.
func (s *PostgresStore) LiveMemoryIDs(ctx context.Context, bankID string, ids []string) (map[string]bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string]bool
		return zero0, schemaErr
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	query := `SELECT id::text FROM ` + quoteIdent(schema) + `.memory_units
	          WHERE bank_id = $1 AND id::text = ANY($2)`
	rows, err := s.reader().QueryContext(ctx, query, bankID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	live := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}

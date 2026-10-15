package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// BankConfig returns a bank's configuration overrides straight from the row.
//
// It deliberately does not go through the process-local bank cache: a
// deployment runs several servers against one database, and a configuration
// change written by one of them has to take effect on the next request the
// others serve. Reading the column is a single indexed lookup, which is the
// price of that consistency, and it is the reference's own behaviour.
func (s *PostgresStore) BankConfig(ctx context.Context, bankID string) (map[string]any, error) {
	query := `SELECT COALESCE(config, '{}'::jsonb) FROM ` + quoteIdent(s.schema) + `.banks WHERE bank_id = $1`
	var raw []byte
	err := s.db.QueryRowContext(ctx, query, bankID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBankNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	config := map[string]any{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	return config, nil
}

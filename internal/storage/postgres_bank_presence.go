package storage

import "context"

// BankHasUnits reports whether a bank holds any unit at all.
//
// It reads the primary rather than the replica when one is configured, because
// the answer decides which bank a read is served from: a replica lagging behind
// a bank's first write would tell a member their team has no content and send
// them to their own bank, then flip back a moment later.
func (s *PostgresStore) BankHasUnits(ctx context.Context, bankID string) (bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return false, schemaErr
	}
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM `+quoteIdent(schema)+`.memory_units WHERE bank_id = $1)`,
		bankID).Scan(&exists)
	return exists, err
}

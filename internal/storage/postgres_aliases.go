package storage

import (
	"context"
	"database/sql"
	"errors"
)

// ResolveBankID maps an alias to its canonical bank identifier. A name that is
// itself a bank wins over an alias of the same text.
func (s *PostgresStore) ResolveBankID(ctx context.Context, name string) (string, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 string
		return zero0, schemaErr
	}
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(schema)+`.banks WHERE bank_id = $1`, name).Scan(&exists)
	if err == nil {
		return name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return name, err
	}
	cache := aliasCacheRef()
	if cached, ok := cache.get(schema, name); ok {
		return cached, nil
	}
	var owner string
	err = s.db.QueryRowContext(ctx,
		`SELECT bank_id FROM `+quoteIdent(schema)+`.bank_aliases WHERE alias = $1`, name).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		// A miss is worth caching too: nearly every request names a real bank,
		// and without a negative entry each one would pay a read that can only
		// answer "not an alias".
		cache.put(schema, name, name)
		return name, nil
	}
	if err != nil {
		return name, err
	}
	cache.put(schema, name, owner)
	return owner, nil
}

// ListAliases returns a bank's aliases displayed first then oldest first.
func (s *PostgresStore) ListAliases(ctx context.Context, bankID string) ([]BankAlias, error) {
	if _, err := s.GetBank(ctx, bankID); err != nil {
		return nil, err
	}
	return s.listAliases(ctx, bankID)
}

// AddAlias attaches an alias to a bank, optionally displaying the bank under
// it, and returns the whole alias list.
func (s *PostgresStore) AddAlias(ctx context.Context, bankID, alias string, primary bool) ([]BankAlias, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []BankAlias
		return zero0, schemaErr
	}
	if !ValidBankID(alias) {
		return nil, ErrInvalidBankID
	}
	if alias == bankID {
		return nil, ErrInvalidBankID
	}
	if _, err := s.GetBank(ctx, bankID); err != nil {
		return nil, err
	}
	owner, err := s.ResolveBankID(ctx, alias)
	if err != nil {
		return nil, err
	}
	if owner == alias {
		if _, isBank := s.bankExists(ctx, schema, alias); isBank {
			return nil, ErrAliasConflict
		}
	} else {
		return nil, ErrAliasConflict
	}
	if primary {
		if err := s.clearPrimary(ctx, bankID); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(schema)+`.bank_aliases (alias, bank_id, is_primary) VALUES ($1,$2,$3)`,
		alias, bankID, primary); err != nil {
		// A concurrent create of the same alias loses the unique constraint the
		// read-then-write check above cannot close; report the reference's
		// conflict rather than the raw write error.
		if isUniqueViolation(err) {
			return nil, ErrAliasConflict
		}
		return nil, err
	}
	// The negative the conflict check above cached for this name would
	// otherwise keep the new alias invisible on this process for the whole TTL.
	aliasCacheRef().invalidate(schema, alias)
	return s.listAliases(ctx, bankID)
}

// SetAliasPrimary displays the bank under the named alias when primary is true,
// demoting the incumbent in the same transaction. When primary is false it
// clears only that alias's display flag, leaving the bank's other aliases as
// they are.
func (s *PostgresStore) SetAliasPrimary(ctx context.Context, bankID, alias string, primary bool) ([]BankAlias, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []BankAlias
		return zero0, schemaErr
	}
	if _, err := s.GetBank(ctx, bankID); err != nil {
		return nil, err
	}
	if !s.ownsAlias(ctx, schema, bankID, alias) {
		return nil, ErrAliasNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if primary {
		// Demote first: the primary-uniqueness index forbids the overlap that
		// setting the new one before clearing the old would create.
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+quoteIdent(schema)+`.bank_aliases SET is_primary = FALSE
			 WHERE bank_id = $1 AND is_primary = TRUE AND alias <> $2`,
			bankID, alias); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+quoteIdent(schema)+`.bank_aliases SET is_primary = $1 WHERE bank_id = $2 AND alias = $3`,
		primary, bankID, alias); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.listAliases(ctx, bankID)
}

// RemoveAlias detaches the named alias from the bank.
func (s *PostgresStore) RemoveAlias(ctx context.Context, bankID, alias string) ([]BankAlias, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []BankAlias
		return zero0, schemaErr
	}
	if _, err := s.GetBank(ctx, bankID); err != nil {
		return nil, err
	}
	if !s.ownsAlias(ctx, schema, bankID, alias) {
		return nil, ErrAliasNotFound
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+quoteIdent(schema)+`.bank_aliases WHERE bank_id = $1 AND alias = $2`,
		bankID, alias); err != nil {
		return nil, err
	}
	// A cached resolution for the detached name would otherwise keep routing to
	// the bank on this process for the whole TTL.
	aliasCacheRef().invalidate(schema, alias)
	return s.listAliases(ctx, bankID)
}

func (s *PostgresStore) listAliases(ctx context.Context, bankID string) ([]BankAlias, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []BankAlias
		return zero0, schemaErr
	}
	query := `SELECT alias, bank_id, is_primary, created_at
	          FROM ` + quoteIdent(schema) + `.bank_aliases
	          WHERE bank_id = $1 ORDER BY is_primary DESC, created_at ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	aliases := make([]BankAlias, 0)
	for rows.Next() {
		var entry BankAlias
		if err := rows.Scan(&entry.Alias, &entry.BankID, &entry.Primary, &entry.CreatedAt); err != nil {
			return nil, err
		}
		aliases = append(aliases, entry)
	}
	return aliases, rows.Err()
}

func (s *PostgresStore) ownsAlias(ctx context.Context, schema, bankID, alias string) bool {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(schema)+`.bank_aliases WHERE bank_id = $1 AND alias = $2`,
		bankID, alias).Scan(&exists)
	return err == nil
}

func (s *PostgresStore) bankExists(ctx context.Context, schema, bankID string) (string, bool) {
	var seen string
	err := s.db.QueryRowContext(ctx,
		`SELECT bank_id FROM `+quoteIdent(schema)+`.banks WHERE bank_id = $1`, bankID).Scan(&seen)
	return seen, err == nil
}

func (s *PostgresStore) clearPrimary(ctx context.Context, bankID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE `+quoteIdent(schema)+`.bank_aliases SET is_primary = FALSE WHERE bank_id = $1`,
		bankID)
	return err
}

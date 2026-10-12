package storage

import (
	"context"
	"database/sql"
	"errors"
)

// ResolveBankID maps an alias to its canonical bank identifier. A name that is
// itself a bank wins over an alias of the same text.
func (s *PostgresStore) ResolveBankID(ctx context.Context, name string) (string, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(s.schema)+`.banks WHERE id = $1`, name).Scan(&exists)
	if err == nil {
		return name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return name, err
	}
	var owner string
	err = s.db.QueryRowContext(ctx,
		`SELECT bank_id FROM `+quoteIdent(s.schema)+`.bank_aliases WHERE alias = $1`, name).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return name, nil
	}
	return owner, err
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
		if _, isBank := s.bankExists(ctx, alias); isBank {
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
		`INSERT INTO `+quoteIdent(s.schema)+`.bank_aliases (alias, bank_id, is_primary) VALUES ($1,$2,$3)`,
		alias, bankID, primary); err != nil {
		return nil, err
	}
	return s.listAliases(ctx, bankID)
}

// PromoteAlias displays the bank under the named alias, demoting any other.
func (s *PostgresStore) PromoteAlias(ctx context.Context, bankID, alias string) ([]BankAlias, error) {
	if _, err := s.GetBank(ctx, bankID); err != nil {
		return nil, err
	}
	if !s.ownsAlias(ctx, bankID, alias) {
		return nil, ErrAliasNotFound
	}
	if err := s.clearPrimary(ctx, bankID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE `+quoteIdent(s.schema)+`.bank_aliases SET is_primary = TRUE WHERE bank_id = $1 AND alias = $2`,
		bankID, alias); err != nil {
		return nil, err
	}
	return s.listAliases(ctx, bankID)
}

// RemoveAlias detaches the named alias from the bank.
func (s *PostgresStore) RemoveAlias(ctx context.Context, bankID, alias string) ([]BankAlias, error) {
	if _, err := s.GetBank(ctx, bankID); err != nil {
		return nil, err
	}
	if !s.ownsAlias(ctx, bankID, alias) {
		return nil, ErrAliasNotFound
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+quoteIdent(s.schema)+`.bank_aliases WHERE bank_id = $1 AND alias = $2`,
		bankID, alias); err != nil {
		return nil, err
	}
	return s.listAliases(ctx, bankID)
}

func (s *PostgresStore) listAliases(ctx context.Context, bankID string) ([]BankAlias, error) {
	query := `SELECT alias, bank_id, is_primary, created_at
	          FROM ` + quoteIdent(s.schema) + `.bank_aliases
	          WHERE bank_id = $1 ORDER BY is_primary DESC, created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (s *PostgresStore) ownsAlias(ctx context.Context, bankID, alias string) bool {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(s.schema)+`.bank_aliases WHERE bank_id = $1 AND alias = $2`,
		bankID, alias).Scan(&exists)
	return err == nil
}

func (s *PostgresStore) bankExists(ctx context.Context, bankID string) (string, bool) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM `+quoteIdent(s.schema)+`.banks WHERE id = $1`, bankID).Scan(&id)
	return id, err == nil
}

func (s *PostgresStore) clearPrimary(ctx context.Context, bankID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE `+quoteIdent(s.schema)+`.bank_aliases SET is_primary = FALSE WHERE bank_id = $1`,
		bankID)
	return err
}

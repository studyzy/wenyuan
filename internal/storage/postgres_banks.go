package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ListBanks returns one page of banks ordered most-recently-written first together
// with the pre-paging match count. The search filter matches the identifier,
// the display name or any alias.
func (s *PostgresStore) ListBanks(ctx context.Context, query BankQuery) (BankPage, error) {
	where := ""
	args := []any{}
	if query.Search != "" {
		where = ` WHERE (b.id ILIKE $1 OR b.name ILIKE $1 OR EXISTS (
		    SELECT 1 FROM ` + quoteIdent(s.schema) + `.bank_aliases a
		    WHERE a.bank_id = b.id AND a.alias ILIKE $1))`
		args = append(args, "%"+query.Search+"%")
	}

	var total int
	countSQL := `SELECT count(*) FROM ` + quoteIdent(s.schema) + `.banks b` + where
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return BankPage{}, err
	}

	listSQL := `SELECT b.id, b.name, b.disposition_skepticism, b.disposition_literalism, b.disposition_empathy,
	                   b.reflect_mission, b.created_at, b.updated_at, b.last_write_at, b.last_document_at
	            FROM ` + quoteIdent(s.schema) + `.banks b` + where + `
	            ORDER BY COALESCE(b.last_write_at, b.created_at) DESC, b.id ASC`
	if query.Limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT %d OFFSET %d", query.Limit, maxInt(query.Offset, 0))
	} else if query.Offset > 0 {
		listSQL += fmt.Sprintf(" OFFSET %d", query.Offset)
	}

	rows, err := s.db.QueryContext(ctx, listSQL, args...)
	if err != nil {
		return BankPage{}, err
	}
	defer func() { _ = rows.Close() }()
	banks := make([]Bank, 0)
	for rows.Next() {
		bank, err := scanBank(rows)
		if err != nil {
			return BankPage{}, err
		}
		banks = append(banks, bank)
	}
	return BankPage{Banks: banks, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

func (s *PostgresStore) CreateBank(ctx context.Context, bank Bank) (Bank, error) {
	if !ValidBankID(bank.ID) {
		return Bank{}, ErrInvalidBankID
	}
	storeBankDefaults(&bank)
	applyDispositionDefaults(&bank)
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.banks
	          (id, name, disposition_skepticism, disposition_literalism, disposition_empathy, reflect_mission)
	          VALUES ($1,$2,$3,$4,$5,$6)
	          RETURNING id, name, disposition_skepticism, disposition_literalism, disposition_empathy,
	                    reflect_mission, created_at, updated_at, last_write_at, last_document_at`
	row := s.db.QueryRowContext(ctx, query, bank.ID, bank.Name,
		bank.DispositionSkepticism, bank.DispositionLiteralism, bank.DispositionEmpathy, bank.ReflectMission)
	return scanBank(row)
}

func (s *PostgresStore) GetBank(ctx context.Context, bankID string) (Bank, error) {
	query := `SELECT id, name, disposition_skepticism, disposition_literalism, disposition_empathy,
	                 reflect_mission, created_at, updated_at, last_write_at, last_document_at
	          FROM ` + quoteIdent(s.schema) + `.banks WHERE id = $1`
	row := s.db.QueryRowContext(ctx, query, bankID)
	bank, err := scanBank(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bank{}, ErrBankNotFound
	}
	return bank, err
}

func (s *PostgresStore) ReplaceBank(ctx context.Context, bank Bank) (Bank, error) {
	storeBankDefaults(&bank)
	applyDispositionDefaults(&bank)
	query := `UPDATE ` + quoteIdent(s.schema) + `.banks SET
	          name=$2, disposition_skepticism=$3, disposition_literalism=$4, disposition_empathy=$5,
	          reflect_mission=$6, updated_at=now()
	          WHERE id=$1
	          RETURNING id, name, disposition_skepticism, disposition_literalism, disposition_empathy,
	                    reflect_mission, created_at, updated_at, last_write_at, last_document_at`
	row := s.db.QueryRowContext(ctx, query, bank.ID, bank.Name,
		bank.DispositionSkepticism, bank.DispositionLiteralism, bank.DispositionEmpathy, bank.ReflectMission)
	stored, err := scanBank(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bank{}, ErrBankNotFound
	}
	return stored, err
}

func (s *PostgresStore) UpdateBank(ctx context.Context, bankID string, patch BankPatch) (Bank, error) {
	current, err := s.GetBank(ctx, bankID)
	if err != nil {
		return Bank{}, err
	}
	applyBankPatch(&current, patch)
	return s.ReplaceBank(ctx, current)
}

// DeleteBank removes a bank and its owned rows, returning how many bank rows
// were removed. Alias rows disappear through the cascade.
func (s *PostgresStore) DeleteBank(ctx context.Context, bankID string) (int, error) {
	query := `DELETE FROM ` + quoteIdent(s.schema) + `.banks WHERE id = $1`
	res, err := s.db.ExecContext(ctx, query, bankID)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanBank(row rowScanner) (Bank, error) {
	var bank Bank
	if err := row.Scan(&bank.ID, &bank.Name,
		&bank.DispositionSkepticism, &bank.DispositionLiteralism, &bank.DispositionEmpathy,
		&bank.ReflectMission, &bank.CreatedAt, &bank.UpdatedAt,
		&bank.LastWriteAt, &bank.LastDocumentAt); err != nil {
		return Bank{}, err
	}
	return bank, nil
}

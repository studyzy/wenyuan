package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ListBanks returns one page of banks ordered most-recently-written first together
// with the pre-paging match count. The search filter matches the identifier,
// the display name or any alias.
//
// The activity watermarks the collection shows are derived, not stored: the
// newest document creation is the ingestion time, and the newest write to a
// document or a memory unit is the last write. A bank with neither falls back
// to its creation time, which is the reference's ordering.
func (s *PostgresStore) ListBanks(ctx context.Context, query BankQuery) (BankPage, error) {
	qualified := quoteIdent(s.schema)
	where := ""
	args := []any{}
	if query.Search != "" {
		where = ` WHERE (b.bank_id ILIKE $1 OR b.name ILIKE $1 OR EXISTS (
		    SELECT 1 FROM ` + qualified + `.bank_aliases a
		    WHERE a.bank_id = b.bank_id AND a.alias ILIKE $1))`
		args = append(args, "%"+query.Search+"%")
	}

	var total int
	countSQL := `SELECT count(*) FROM ` + qualified + `.banks b` + where
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return BankPage{}, err
	}

	listSQL := `SELECT b.bank_id, COALESCE(b.name,''), COALESCE(b.disposition, '{}'::jsonb),
	                   COALESCE(b.mission,''), b.created_at, b.updated_at,
	                   d.last_document_at, GREATEST(d.last_document_write_at, f.last_fact_at)
	            FROM ` + qualified + `.banks b
	            LEFT JOIN (
	                SELECT bank_id, MAX(created_at) AS last_document_at,
	                       MAX(updated_at) AS last_document_write_at
	                FROM ` + qualified + `.documents GROUP BY bank_id
	            ) d ON d.bank_id = b.bank_id
	            LEFT JOIN (
	                SELECT bank_id, MAX(updated_at) AS last_fact_at
	                FROM ` + qualified + `.memory_units GROUP BY bank_id
	            ) f ON f.bank_id = b.bank_id` + where + `
	            ORDER BY COALESCE(GREATEST(d.last_document_write_at, f.last_fact_at), b.created_at) DESC,
	                     b.bank_id ASC`
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
		bank, err := scanBankListRow(rows)
		if err != nil {
			return BankPage{}, err
		}
		banks = append(banks, bank)
	}
	return BankPage{Banks: banks, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

func (s *PostgresStore) CreateBank(ctx context.Context, bank Bank) (Bank, error) {
	if !ValidBankID(bank.BankID) {
		return Bank{}, ErrInvalidBankID
	}
	storeBankDefaults(&bank)
	applyDispositionDefaults(&bank)
	disposition, err := marshalMetadata(dispositionAsAny(bank.Disposition))
	if err != nil {
		return Bank{}, err
	}
	// The reference names the bank key `bank_id` and carries the traits in one
	// jsonb column, so a row written here is one the reference service reads.
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.banks
	          (bank_id, name, disposition, mission)
	          VALUES ($1,$2,$3,$4)
	          RETURNING ` + bankSelectColumns
	row := s.db.QueryRowContext(ctx, query, bank.BankID, bank.Name, disposition, bank.Mission)
	return scanBank(row)
}

func (s *PostgresStore) GetBank(ctx context.Context, bankID string) (Bank, error) {
	query := `SELECT ` + bankSelectColumns + `
	          FROM ` + quoteIdent(s.schema) + `.banks WHERE bank_id = $1`
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
	disposition, err := marshalMetadata(dispositionAsAny(bank.Disposition))
	if err != nil {
		return Bank{}, err
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.banks SET
	          name=$2, disposition=$3, mission=$4, updated_at=now()
	          WHERE bank_id=$1
	          RETURNING ` + bankSelectColumns
	row := s.db.QueryRowContext(ctx, query, bank.BankID, bank.Name, disposition, bank.Mission)
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
	query := `DELETE FROM ` + quoteIdent(s.schema) + `.banks WHERE bank_id = $1`
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

// bankSelectColumns is the column list every single-bank read uses. Declared
// once so a read and a write cannot drift, and in the reference's column order.
// The activity watermarks a listing shows are not here: a single bank has no
// place to put them, and the reference's own profile read does not carry them.
const bankSelectColumns = `bank_id, COALESCE(name,''), COALESCE(disposition, '{}'::jsonb),
	COALESCE(mission,''), created_at, updated_at`

func scanBank(row rowScanner) (Bank, error) {
	var bank Bank
	var disposition []byte
	if err := row.Scan(&bank.BankID, &bank.Name, &disposition,
		&bank.Mission, &bank.CreatedAt, &bank.UpdatedAt); err != nil {
		return Bank{}, err
	}
	bank.Disposition = decodeDisposition(disposition)
	return bank, nil
}

// scanBankListRow reads a collection row, which adds the two derived activity
// watermarks. Neither is stored, and a bank with no documents or units has
// neither.
func scanBankListRow(row rowScanner) (Bank, error) {
	var bank Bank
	var disposition []byte
	var lastDocument, lastWrite sql.NullTime
	if err := row.Scan(&bank.BankID, &bank.Name, &disposition,
		&bank.Mission, &bank.CreatedAt, &bank.UpdatedAt,
		&lastDocument, &lastWrite); err != nil {
		return Bank{}, err
	}
	bank.Disposition = decodeDisposition(disposition)
	bank.LastDocumentAt = nullTimePtr(lastDocument)
	bank.LastWriteAt = nullTimePtr(lastWrite)
	return bank, nil
}

// dispositionAsAny renders the traits for the jsonb column.
func dispositionAsAny(disposition map[string]int) map[string]any {
	out := make(map[string]any, len(disposition))
	for key, value := range disposition {
		out[key] = value
	}
	return out
}

// decodeDisposition reads the jsonb trait object. A value that is not an object,
// or whose traits are not numbers, yields the neutral set rather than an error:
// the column is caller-influenced and a malformed value should not make a bank
// unreadable.
func decodeDisposition(raw []byte) map[string]int {
	out := map[string]int{}
	if len(raw) == 0 {
		return out
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return out
	}
	for key, value := range decoded {
		switch typed := value.(type) {
		case float64:
			out[key] = int(typed)
		case int:
			out[key] = typed
		}
	}
	return out
}

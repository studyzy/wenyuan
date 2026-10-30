package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 BankPage
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	where := ""
	args := []any{}
	if query.SearchGiven {
		where = ` WHERE (b.bank_id ILIKE $1 OR b.name ILIKE $1 OR EXISTS (
		    SELECT 1 FROM ` + qualified + `.bank_aliases a
		    WHERE a.bank_id = b.bank_id AND a.alias ILIKE $1))`
		args = append(args, "%"+query.Search+"%")
	}

	var total int
	countSQL := `SELECT count(*) FROM ` + qualified + `.banks b` + where
	if err := s.reader().QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return BankPage{}, err
	}

	listSQL := `SELECT b.bank_id, COALESCE(b.name,''), COALESCE(b.disposition, '{}'::jsonb),
	                   COALESCE(b.mission,''), COALESCE(b.config, '{}'::jsonb), b.created_at, b.updated_at,
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
	if query.Limit >= 0 {
		listSQL += fmt.Sprintf(" LIMIT %d OFFSET %d", query.Limit, maxInt(query.Offset, 0))
	} else if query.Offset > 0 {
		listSQL += fmt.Sprintf(" OFFSET %d", maxInt(query.Offset, 0))
	}

	rows, err := s.reader().QueryContext(ctx, listSQL, args...)
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
	if err := rows.Err(); err != nil {
		return BankPage{}, err
	}
	if err := s.fillFactCounts(ctx, banks); err != nil {
		return BankPage{}, err
	}
	matchedAliases, err := s.matchedAliases(ctx, banks, query.Search)
	if err != nil {
		return BankPage{}, err
	}
	return BankPage{Banks: banks, Total: total, Limit: query.Limit, Offset: query.Offset, MatchedAliases: matchedAliases}, nil
}

// matchedAliases reports, per returned bank, the aliases that explain the hit:
// only when the search text is not already in the bank's own id or name, and
// only for the page's banks. It is the reference's _aliases_explaining.
func (s *PostgresStore) matchedAliases(ctx context.Context, banks []Bank, search string) (map[string][]string, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string][]string
		return zero0, schemaErr
	}
	if search == "" || len(banks) == 0 {
		return nil, nil
	}
	ids := make([]string, len(banks))
	byID := make(map[string]Bank, len(banks))
	for i := range banks {
		ids[i] = banks[i].BankID
		byID[banks[i].BankID] = banks[i]
	}
	qualified := quoteIdent(schema)
	rows, err := s.reader().QueryContext(ctx, `SELECT bank_id, alias
	    FROM `+qualified+`.bank_aliases
	    WHERE bank_id = ANY($1) AND UPPER(alias) LIKE UPPER($2)
	    ORDER BY alias`, ids, "%"+search+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	needle := strings.ToUpper(search)
	matched := map[string][]string{}
	for rows.Next() {
		var bankID, alias string
		if err := rows.Scan(&bankID, &alias); err != nil {
			return nil, err
		}
		bank := byID[bankID]
		if strings.Contains(strings.ToUpper(bank.BankID), needle) || strings.Contains(strings.ToUpper(bank.Name), needle) {
			continue
		}
		matched[bankID] = append(matched[bankID], alias)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return matched, nil
}

// fillFactCounts sets each returned bank's FactCount from its memory_units rows.
//
// The count is scoped to the page rather than aggregated over the whole table,
// which is the reference's own fix for a listing that got slower the more banks
// a deployment had: the page is a bounded index range per bank. A bank with no
// units keeps its zero.
func (s *PostgresStore) fillFactCounts(ctx context.Context, banks []Bank) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if len(banks) == 0 {
		return nil
	}
	ids := make([]string, len(banks))
	for i := range banks {
		ids[i] = banks[i].BankID
	}
	qualified := quoteIdent(schema)
	rows, err := s.reader().QueryContext(ctx, `SELECT bank_id, COUNT(*)
	    FROM `+qualified+`.memory_units
	    WHERE bank_id = ANY($1)
	    GROUP BY bank_id`, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	counts := make(map[string]int, len(banks))
	for rows.Next() {
		var bankID string
		var count int
		if err := rows.Scan(&bankID, &count); err != nil {
			return err
		}
		counts[bankID] = count
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range banks {
		banks[i].FactCount = counts[banks[i].BankID]
	}
	return nil
}

func (s *PostgresStore) CreateBank(ctx context.Context, bank Bank) (Bank, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Bank
		return zero0, schemaErr
	}
	if !ValidBankID(bank.BankID) {
		return Bank{}, ErrInvalidBankID
	}
	storeBankDefaults(&bank)
	disposition, err := marshalMetadata(dispositionAsAny(bank.Disposition))
	if err != nil {
		return Bank{}, err
	}
	config, err := marshalMetadata(bank.Config)
	if err != nil {
		return Bank{}, err
	}
	// The reference names the bank key `bank_id` and carries the traits in one
	// jsonb column, so a row written here is one the reference service reads.
	query := `INSERT INTO ` + quoteIdent(schema) + `.banks
	          (bank_id, name, disposition, mission, config)
	          VALUES ($1,$2,$3,$4,$5::jsonb)
	          RETURNING ` + bankSelectColumns
	row := s.db.QueryRowContext(ctx, query, bank.BankID, bank.Name, disposition, bank.Mission, config)
	stored, err := scanBank(row)
	if err != nil {
		return Bank{}, err
	}
	// The insert has autocommitted, so a hook that opens its own statements
	// sees the row rather than racing a transaction it is not part of.
	runBankCreateHook(ctx, s.bankCreateHookRef(), stored.BankID)
	return stored, nil
}

// ensureBankExists lazily creates the bank row if it is absent, returning
// whether this call inserted it.
//
// A bank row is the foreign-key target of every bank-scoped write, and callers
// address a bank by name without having to declare it first, so a first write
// to an unknown bank creates it rather than failing on a raw foreign-key
// violation. The insert is idempotent: concurrent callers race benignly, one
// inserts and the rest observe the row the winner wrote.
//
// A deployment-configured default bank template is applied by the store's
// bank-create hook, which the composition root installs; it runs after this
// insert commits, so the freshly-created bank is the hook's to provision.
func (s *PostgresStore) ensureBankExists(ctx context.Context, bankID string) (bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 bool
		return zero0, schemaErr
	}
	if !ValidBankID(bankID) {
		return false, ErrInvalidBankID
	}
	bank := Bank{BankID: bankID}
	storeBankDefaults(&bank)
	disposition, err := marshalMetadata(dispositionAsAny(bank.Disposition))
	if err != nil {
		return false, err
	}
	config, err := marshalMetadata(bank.Config)
	if err != nil {
		return false, err
	}
	query := `INSERT INTO ` + quoteIdent(schema) + `.banks
	          (bank_id, name, disposition, mission, config)
	          VALUES ($1,$2,$3,$4,$5::jsonb)
	          ON CONFLICT (bank_id) DO NOTHING`
	result, err := s.db.ExecContext(ctx, query, bank.BankID, bank.Name, disposition, bank.Mission, config)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, nil
	}
	if affected > 0 {
		runBankCreateHook(ctx, s.bankCreateHookRef(), bankID)
	}
	return affected > 0, nil
}

// EnsureBank lazily creates the bank row when it is absent, so a bank-scoped
// write that addresses a bank by name — a webhook registration, an async
// operation — lands on the row its foreign key needs rather than failing on a
// constraint violation. Concurrent callers race benignly.
func (s *PostgresStore) EnsureBank(ctx context.Context, bankID string) error {
	_, err := s.ensureBankExists(ctx, bankID)
	return err
}

// ListBankIDs returns the ids of the banks in the current schema whose id
// starts with prefix, in id order.
//
// The match is on left(), not LIKE: a prefix built from a name is literal text,
// and a "_" or "%" inside it must not turn into a wildcard.
func (s *PostgresStore) ListBankIDs(ctx context.Context, prefix string) ([]string, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT bank_id FROM `+quoteIdent(schema)+`.banks
		 WHERE left(bank_id, length($1::text)) = $1 ORDER BY bank_id`, prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresStore) GetBank(ctx context.Context, bankID string) (Bank, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Bank
		return zero0, schemaErr
	}
	query := `SELECT ` + bankSelectColumns + `
	          FROM ` + quoteIdent(schema) + `.banks WHERE bank_id = $1`
	row := s.reader().QueryRowContext(ctx, query, bankID)
	bank, err := scanBank(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bank{}, ErrBankNotFound
	}
	return bank, err
}

func (s *PostgresStore) ReplaceBank(ctx context.Context, bank Bank) (Bank, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Bank
		return zero0, schemaErr
	}
	// A replace writes exactly what it is given: the name defaulting belongs to
	// the create paths, so a partial update that clears the name keeps it empty
	// rather than silently restoring the identifier.
	disposition, err := marshalMetadata(dispositionAsAny(bank.Disposition))
	if err != nil {
		return Bank{}, err
	}
	config, err := marshalMetadata(bank.Config)
	if err != nil {
		return Bank{}, err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.banks SET
	          name=$2, disposition=$3, mission=$4, config=$5::jsonb, updated_at=now()
	          WHERE bank_id=$1
	          RETURNING ` + bankSelectColumns
	row := s.db.QueryRowContext(ctx, query, bank.BankID, bank.Name, disposition, bank.Mission, config)
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
//
// It also drops the bank's per-(bank, fact_type) partial vector indexes, before
// the DELETE. Those indexes are named after the bank identifier (see
// BankVectorIndexName) and the sweep plans from the banks table, so once the row
// is gone no later reconcile can name them — the deletion path is the only one
// that owns this guarantee. Dropping first means a failed drop leaves the bank
// to be retried rather than stranding an unreachable index.
func (s *PostgresStore) DeleteBank(ctx context.Context, bankID string) (int, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 int
		return zero0, schemaErr
	}
	if err := s.dropBankVectorIndexes(ctx, bankID); err != nil {
		return 0, fmt.Errorf("drop vector indexes for bank %s: %w", bankID, err)
	}
	qualified := quoteIdent(schema)
	// The bank's content is purged explicitly, not left to cascade: the memory
	// domain hangs off `documents` and `entities`, neither of which references
	// `banks`, so deleting the bank row alone would orphan the whole domain.
	// The order is the reference's: documents first (they cascade to chunks,
	// memory_units, invalidated_memory_units, memory_links and unit_entities),
	// then the rows no cascade reaches, and the bank row last.
	counts := map[string]int{}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// A bank that never existed counts zero and deletes nothing; the reference
	// answers zero for it rather than faulting.
	for _, table := range []string{"memory_units", "entities", "documents"} {
		var value int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+qualified+`.`+table+` WHERE bank_id = $1`, bankID).Scan(&value); err != nil {
			return 0, err
		}
		counts[table] = value
	}
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.documents WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.attachments WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.observation_history WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.invalidated_memory_units WHERE bank_id = $1`,
		`DELETE FROM ` + qualified + `.entities WHERE bank_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, statement, bankID); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM `+qualified+`.banks WHERE bank_id = $1`, bankID)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// Reclaim the bank's external payloads. A bank that never existed has no
	// rows and no objects; a failed sweep is a warning rather than a failure,
	// because the rows are already gone and a stray object is only wasted
	// space.
	if int(affected) > 0 && s.objectStore != nil {
		if deleter, ok := s.objectStore.(prefixDeleter); ok {
			if err := deleter.DeletePrefix(ctx, bankStoragePrefix(bankID)); err != nil {
				// A sweep failure leaves orphaned objects the next teardown
				// can reclaim; it must not fail the delete.
				_ = err
			}
		}
	}
	// The reported count is the reference's sum: memory units, entities and
	// documents. It is zero for a bank that was never created.
	return counts["memory_units"] + counts["entities"] + counts["documents"], nil
}

// prefixDeleter is the optional object-store capability a bank teardown uses to
// reclaim a bank's payloads.
type prefixDeleter interface {
	DeletePrefix(ctx context.Context, prefix string) error
}

// bankStoragePrefix is the key prefix every payload of one bank is stored
// under.
func bankStoragePrefix(bankID string) string {
	return "banks/" + encodeSegment(bankID) + "/"
}

type rowScanner interface {
	Scan(dest ...any) error
}

// bankSelectColumns is the column list every single-bank read uses. Declared
// once so a read and a write cannot drift, and in the reference's column order.
// The activity watermarks a listing shows are not here: a single bank has no
// place to put them, and the reference's own profile read does not carry them.
const bankSelectColumns = `bank_id, COALESCE(name,''), COALESCE(disposition, '{}'::jsonb),
	COALESCE(mission,''), COALESCE(config, '{}'::jsonb), created_at, updated_at`

func scanBank(row rowScanner) (Bank, error) {
	var bank Bank
	var disposition, config []byte
	if err := row.Scan(&bank.BankID, &bank.Name, &disposition,
		&bank.Mission, &config, &bank.CreatedAt, &bank.UpdatedAt); err != nil {
		return Bank{}, err
	}
	bank.Disposition = decodeDisposition(disposition)
	bank.Config = decodeJSONMap(config)
	return bank, nil
}

// scanBankListRow reads a collection row, which adds the two derived activity
// watermarks. Neither is stored, and a bank with no documents or units has
// neither.
func scanBankListRow(row rowScanner) (Bank, error) {
	var bank Bank
	var disposition, config []byte
	var lastDocument, lastWrite sql.NullTime
	if err := row.Scan(&bank.BankID, &bank.Name, &disposition,
		&bank.Mission, &config, &bank.CreatedAt, &bank.UpdatedAt,
		&lastDocument, &lastWrite); err != nil {
		return Bank{}, err
	}
	bank.Disposition = decodeDisposition(disposition)
	// The listing overlays the bank's configuration on the legacy disposition
	// and mission columns, so the config column has to travel with the row.
	bank.Config = decodeJSONMap(config)
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

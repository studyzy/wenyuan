package storage

import (
	"context"
	"fmt"
	"sort"
)

// ReconcileBankVectorIndexes brings one bank's per-(bank, fact_type) partial
// vector index coverage to the plan, creating and dropping indexes as the
// threshold requires.
//
// The DDL runs outside a transaction because CONCURRENTLY cannot run inside
// one, and it is not wrapped in retries: a failed build leaves the partition
// uncovered and the next reconcile picks it up, which is safer than retrying a
// lock-taking statement in a loop. "Picks it up" is only true because an
// interrupted CONCURRENTLY build leaves an invalid index that the state read
// reports as unusable and the plan drops before rebuilding — CREATE ... IF NOT
// EXISTS alone would treat that leftover as the index it was asked for and
// never rebuild.
func (s *PostgresStore) ReconcileBankVectorIndexes(ctx context.Context, bankID string, minRows int) ([]BankVectorIndexAction, error) {
	if minRows <= 0 {
		return nil, nil
	}
	states, err := s.BankVectorIndexStates(ctx, bankID)
	if err != nil {
		return nil, err
	}
	plan := PlanBankVectorIndexes(states, minRows)
	applied := make([]BankVectorIndexAction, 0, len(plan))
	for _, action := range plan {
		statement := BankVectorIndexDDL(s.schema, s.vectorExtension, bankID, action)
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			// A failure on one partition must not hide the others: the sweep
			// continues and the caller sees what was actually applied.
			return applied, fmt.Errorf("apply vector index %s for bank %s: %w", action.IndexName, bankID, err)
		}
		applied = append(applied, action)
	}
	return applied, nil
}

// OrphanedVectorIndexes lists the per-bank vector indexes whose bank no longer
// exists. A bank's indexes are dropped with the bank, so one still present here
// is a leftover — from an interrupted delete, or from a bank removed while this
// deployment was not running — and it costs every statement on memory_units.
//
// The live set is computed from the banks table by name rather than parsed out
// of the index name, because the name's digest cannot be inverted: a bank
// identifier cannot be recovered from it, only matched against.
func (s *PostgresStore) OrphanedVectorIndexes(ctx context.Context) ([]string, error) {
	live, err := s.liveVectorIndexNames(ctx)
	if err != nil {
		return nil, err
	}
	present, err := s.presentBankVectorIndexNames(ctx)
	if err != nil {
		return nil, err
	}
	orphans := make([]string, 0)
	for _, name := range present {
		if !live[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	return orphans, nil
}

// liveVectorIndexNames is the set of per-bank vector index names the current
// banks are owed.
func (s *PostgresStore) liveVectorIndexNames(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bank_id FROM `+quoteIdent(s.schema)+`.banks`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	live := map[string]bool{}
	for rows.Next() {
		var bankID string
		if err := rows.Scan(&bankID); err != nil {
			return nil, err
		}
		for factType := range BankVectorIndexFactTypes {
			live[BankVectorIndexName(factType, bankID)] = true
		}
	}
	return live, rows.Err()
}

// presentBankVectorIndexNames lists every per-bank vector index that exists on
// memory_units in this schema, whether or not it is usable.
func (s *PostgresStore) presentBankVectorIndexNames(ctx context.Context) ([]string, error) {
	query := `SELECT c.relname
	          FROM pg_class c
	          JOIN pg_namespace n ON n.oid = c.relnamespace
	          JOIN pg_index i ON i.indexrelid = c.oid
	          JOIN pg_class t ON t.oid = i.indrelid
	          WHERE n.nspname = $1 AND t.relname = 'memory_units' AND c.relname LIKE $2`
	rows, err := s.db.QueryContext(ctx, query, s.schema, bankVectorIndexPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// DropVectorIndexes removes the named indexes. Every statement is DROP ... IF
// EXISTS, so a name that is already gone is not an error and the sweep can be
// re-run.
func (s *PostgresStore) DropVectorIndexes(ctx context.Context, names []string) error {
	var firstErr error
	for _, name := range names {
		statement := "DROP INDEX CONCURRENTLY IF EXISTS " + quoteIdent(s.schema) + "." + quoteIdent(name)
		if _, err := s.db.ExecContext(ctx, statement); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BanksNeedingVectorIndexes lists the banks the sweep must visit: those holding
// at least one fact-type partition at or above the threshold, plus those still
// carrying one of our per-bank indexes below it.
//
// The second group is what makes the plan's drop branch reachable. A bank whose
// partition shrank below the threshold no longer qualifies, so a threshold-only
// list would never revisit it and its now-unwanted index would live forever.
//
// Counting is bounded by the threshold rather than exact for a partition above
// it, because the sweep only needs to know whether the partition qualifies.
func (s *PostgresStore) BanksNeedingVectorIndexes(ctx context.Context, minRows int) ([]string, error) {
	if minRows <= 0 {
		return nil, nil
	}
	qualified := quoteIdent(s.schema)
	query := `SELECT bank_id FROM (
	            SELECT bank_id, fact_type, COUNT(*) AS rows
	            FROM ` + qualified + `.memory_units
	            WHERE fact_type = ANY($1)
	            GROUP BY bank_id, fact_type
	          ) counts
	          WHERE rows >= $2
	          GROUP BY bank_id
	          ORDER BY bank_id`
	rows, err := s.db.QueryContext(ctx, query, factTypeList(), minRows)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	banks := make([]string, 0)
	for rows.Next() {
		var bankID string
		if err := rows.Scan(&bankID); err != nil {
			return nil, err
		}
		if !seen[bankID] {
			seen[bankID] = true
			banks = append(banks, bankID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	indexed, err := s.banksHoldingVectorIndexes(ctx)
	if err != nil {
		return nil, err
	}
	for _, bankID := range indexed {
		if !seen[bankID] {
			seen[bankID] = true
			banks = append(banks, bankID)
		}
	}
	sort.Strings(banks)
	return banks, nil
}

// banksHoldingVectorIndexes lists the banks that currently carry one of our
// per-(bank, fact_type) partial vector indexes, whatever the partition's row
// count.
//
// The match is by name, using the same transform BankVectorIndexName applies
// (the bank identifier's dashes removed and truncated to sixteen characters), so
// it finds a bank whose index is present whether or not that index is usable —
// an invalid leftover still has to be revisited and dropped. The reverse lookup
// lives here rather than in a name parser because the name is lossy: a bank
// identifier longer than sixteen characters cannot be recovered from it, only
// matched against.
func (s *PostgresStore) banksHoldingVectorIndexes(ctx context.Context) ([]string, error) {
	qualified := quoteIdent(s.schema)
	query := `SELECT DISTINCT b.bank_id
	          FROM ` + qualified + `.banks b
	          JOIN pg_class c ON c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = $1)
	          JOIN pg_index i ON i.indexrelid = c.oid
	          JOIN pg_class t ON t.oid = i.indrelid
	          WHERE t.relname = 'memory_units'
	            AND c.relname LIKE 'idx\_mu\_emb\_%\_' || substr(encode(sha256(convert_to(b.bank_id, 'UTF8')), 'hex'), 1, 16)
	          ORDER BY b.bank_id`
	rows, err := s.db.QueryContext(ctx, query, s.schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var banks []string
	for rows.Next() {
		var bankID string
		if err := rows.Scan(&bankID); err != nil {
			return nil, err
		}
		banks = append(banks, bankID)
	}
	return banks, rows.Err()
}

// BankVectorIndexStates reports each fact-type partition's row count and
// whether its partial index exists and is usable.
func (s *PostgresStore) BankVectorIndexStates(ctx context.Context, bankID string) ([]BankVectorIndexState, error) {
	qualified := quoteIdent(s.schema)
	counts := map[string]int64{}
	query := `SELECT fact_type, COUNT(*) FROM ` + qualified + `.memory_units
	          WHERE bank_id = $1 AND fact_type = ANY($2)
	          GROUP BY fact_type`
	rows, err := s.db.QueryContext(ctx, query, bankID, factTypeList())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var factType string
		var count int64
		if err := rows.Scan(&factType, &count); err != nil {
			_ = rows.Close()
			return nil, err
		}
		counts[factType] = count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	usable, err := s.existingVectorIndexes(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]BankVectorIndexState, 0, len(BankVectorIndexFactTypes))
	for factType := range BankVectorIndexFactTypes {
		name := BankVectorIndexName(factType, bankID)
		valid, exists := usable[name]
		states = append(states, BankVectorIndexState{
			FactType: factType, Rows: counts[factType], IndexName: name,
			Present: exists && valid,
			Invalid: exists && !valid,
		})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].FactType < states[j].FactType })
	return states, nil
}

// existingVectorIndexes returns every index on memory_units, keyed by name,
// with true when the index is valid and ready to use.
//
// The key's presence records that an index of that name exists at all and the
// value records whether it is usable, so a caller can tell "absent" from
// "present but invalid" — the distinction the coverage check needs, because a
// cancelled or failed CONCURRENTLY build leaves an invalid row that a
// name-only read would mistake for a healthy index.
func (s *PostgresStore) existingVectorIndexes(ctx context.Context) (map[string]bool, error) {
	// pg_index, not the pg_indexes view: only pg_index.indisvalid/indisready
	// record whether a concurrent build finished. The index definition lives in
	// pg_class under the index's namespace, and the table it covers is
	// memory_units in this schema.
	query := `SELECT c.relname, (i.indisvalid AND i.indisready) AS usable
	          FROM pg_class c
	          JOIN pg_namespace n ON n.oid = c.relnamespace
	          JOIN pg_index i ON i.indexrelid = c.oid
	          JOIN pg_class t ON t.oid = i.indrelid
	          WHERE n.nspname = $1 AND t.relname = 'memory_units'`
	rows, err := s.db.QueryContext(ctx, query, s.schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		var usable bool
		if err := rows.Scan(&name, &usable); err != nil {
			return nil, err
		}
		present[name] = usable
	}
	return present, rows.Err()
}

// dropBankVectorIndexes removes a bank's per-(bank, fact_type) partial vector
// indexes. It is idempotent — every statement is DROP ... IF EXISTS — so it is a
// no-op for a bank that never earned one and for one whose drop already ran.
func (s *PostgresStore) dropBankVectorIndexes(ctx context.Context, bankID string) error {
	var firstErr error
	for _, factType := range factTypeList() {
		name := BankVectorIndexName(factType, bankID)
		statement := BankVectorIndexDDL(s.schema, s.vectorExtension, bankID, BankVectorIndexAction{
			FactType: factType, IndexName: name, Create: false,
		})
		if _, err := s.db.ExecContext(ctx, statement); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// factTypeList returns the fact types eligible for a partial index, ordered.
func factTypeList() []string {
	factTypes := make([]string, 0, len(BankVectorIndexFactTypes))
	for factType := range BankVectorIndexFactTypes {
		factTypes = append(factTypes, factType)
	}
	sort.Strings(factTypes)
	return factTypes
}

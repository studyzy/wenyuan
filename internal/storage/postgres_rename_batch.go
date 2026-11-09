package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RenameBanks moves several banks' rows to new identifiers in one transaction:
// either every pair moves or none does. It is what a restore uses to put a
// reference implementation's banks under this deployment's naming rule
// (`<tenant>::<name>`) without renaming them one command at a time.
//
// A pair whose target is already taken is refused before anything moves, and a
// pair that renames a bank to itself is skipped.
func (s *PostgresStore) RenameBanks(ctx context.Context, renames map[string]string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	pairs := canonicalRenamePairs(renames)
	if len(pairs) == 0 {
		return nil
	}
	for _, pair := range pairs {
		if !ValidBankID(pair[1]) {
			return ErrInvalidBankID
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qualified := quoteIdent(schema)

	if err := precheckRenamePairsTx(ctx, tx, qualified, pairs); err != nil {
		return err
	}

	columns, err := bankColumnsWithoutDefaultsTx(ctx, tx, schema)
	if err != nil {
		return err
	}
	copied := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != "bank_id" {
			copied = append(copied, quoteIdent(column))
		}
	}

	// The composite keys are deferred before the first insert: the copied
	// banks row is what every bank-scoped composite key names, and an immediate
	// key rejects it while the children still point at the source.
	deferred, err := makeCompositeBankKeysDeferrableTx(ctx, tx, schema)
	if err != nil {
		return err
	}
	insert := `INSERT INTO ` + qualified + `.banks (` + quoteIdent("bank_id") + `, ` + strings.Join(copied, ", ") + `)
	          SELECT $1, ` + strings.Join(copied, ", ") + ` FROM ` + qualified + `.banks WHERE bank_id = $2`
	for _, pair := range pairs {
		if _, err := tx.ExecContext(ctx, insert, pair[1], pair[0]); err != nil {
			return fmt.Errorf("copy bank %q: %w", pair[0], err)
		}
	}

	// One statement per table rewrites every moved bank at once. Per-pair
	// statements cannot work here: a row can name two moved banks, as a memory
	// link names both endpoints, so re-keying one end breaks the other before
	// this bank's own turn.
	for _, table := range renameBankTables {
		references, err := tableBankReferencesTx(ctx, tx, schema, table)
		if err != nil {
			return err
		}
		update := `UPDATE ` + qualified + `.` + quoteIdent(table) + ` SET ` +
			rekeyAssignments(references, len(pairs)) + ` WHERE ` + rekeyFilter(references, len(pairs))
		if _, err := tx.ExecContext(ctx, update, rekeyArgs(pairs)...); err != nil {
			return fmt.Errorf("re-key %s: %w", table, err)
		}
	}
	if err := rewriteOperationBankPayloadsTx(ctx, tx, qualified, pairs); err != nil {
		return err
	}
	if err := restoreCompositeBankKeysTx(ctx, tx, schema, deferred); err != nil {
		return err
	}

	sources := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		sources = append(sources, pair[0])
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+qualified+`.banks WHERE bank_id = ANY($1)`, sources); err != nil {
		return err
	}
	return tx.Commit()
}

// RenameBanks is the in-process counterpart: every bank-scoped collection
// moves, one bank at a time, under a single lock so a failure part-way is not
// observable.
func (s *MemoryStore) RenameBanks(ctx context.Context, renames map[string]string) error {
	pairs := canonicalRenamePairs(renames)
	if len(pairs) == 0 {
		return nil
	}
	for _, pair := range pairs {
		if err := s.RenameBank(ctx, pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}

// canonicalRenamePairs returns the renames that actually move something, in a
// stable order: source ascending, which makes the per-table statements and
// the identity copies deterministic run to run.
func canonicalRenamePairs(renames map[string]string) [][2]string {
	pairs := make([][2]string, 0, len(renames))
	for source, target := range renames {
		if source == target || source == "" || target == "" {
			continue
		}
		pairs = append(pairs, [2]string{source, target})
	}
	sortPairsBySource(pairs)
	return pairs
}

// precheckRenamePairsTx refuses the whole batch when a source is missing or a
// target is occupied, before any row moves.
func precheckRenamePairsTx(ctx context.Context, tx Tx, qualified string, pairs [][2]string) error {
	sources := make([]string, 0, len(pairs))
	targets := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		sources = append(sources, pair[0])
		targets = append(targets, pair[1])
	}
	found, err := bankIDsInTx(ctx, tx, qualified, sources)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if !found[source] {
			return fmt.Errorf("%w: %s", ErrBankNotFound, source)
		}
	}
	// A target that is one of the batch's own sources is not occupied: that
	// bank is being moved away in the same transaction.
	moving := make(map[string]bool, len(sources))
	for _, source := range sources {
		moving[source] = true
	}
	existing, err := bankIDsInTx(ctx, tx, qualified, targets)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if existing[target] && !moving[target] {
			return fmt.Errorf("%w: %s", ErrBankExists, target)
		}
	}
	return nil
}

// bankIDsInTx reports which of the identifiers name a bank.
func bankIDsInTx(ctx context.Context, tx Tx, qualified string, ids []string) (map[string]bool, error) {
	found := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT bank_id FROM `+qualified+`.banks WHERE bank_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = true
	}
	return found, rows.Err()
}

// rewriteOperationBankPayloadsTx updates the bank identifiers recorded inside
// queued operations. The rows are history — the worker re-reads the bank
// column, not the payload — but an operator tracing an operation would follow a
// name that no longer exists, so the payload is kept consistent with the move.
func rewriteOperationBankPayloadsTx(ctx context.Context, tx Tx, qualified string, pairs [][2]string) error {
	statement := `UPDATE ` + qualified + `.async_operations
	              SET task_payload = ` + payloadRekeyExpression("task_payload", len(pairs)) + `,
	                  result_metadata = ` + payloadRekeyExpression("result_metadata", len(pairs)) + `
	              WHERE ` + payloadRekeyPredicate("task_payload", len(pairs)) + `
	                 OR ` + payloadRekeyPredicate("result_metadata", len(pairs))
	if _, err := tx.ExecContext(ctx, statement, rekeyArgs(pairs)...); err != nil {
		return fmt.Errorf("re-key async_operations payload: %w", err)
	}
	return nil
}

// payloadRekeyExpression rewrites a jsonb column's `bank_id` string in place.
// A jsonb value the statement does not touch keeps its identity, so the
// expression is safe to apply broadly.
func payloadRekeyExpression(column string, pairs int) string {
	when := payloadRekeyCases(column, pairs)
	return `CASE WHEN ` + column + `->>'bank_id' IN (` + payloadSourcePlaceholders(pairs) + `) ` +
		`THEN jsonb_set(` + column + `, '{bank_id}', to_jsonb(CASE ` + column + `->>'bank_id' ` + when + ` END), false) ` +
		`ELSE ` + column + ` END`
}

// payloadRekeyPredicate picks the rows either jsonb column names a moved bank.
func payloadRekeyPredicate(column string, pairs int) string {
	return column + `->>'bank_id' IN (` + payloadSourcePlaceholders(pairs) + `)`
}

// payloadRekeyCases renders `WHEN $1 THEN $2 ...` over the pair arguments.
func payloadRekeyCases(column string, pairs int) string {
	cases := make([]string, 0, pairs)
	for pair := 0; pair < pairs; pair++ {
		cases = append(cases, fmt.Sprintf("WHEN $%d THEN $%d", 2*pair+1, 2*pair+2))
	}
	return strings.Join(cases, " ")
}

// payloadSourcePlaceholders renders the source arguments of the pairs.
func payloadSourcePlaceholders(pairs int) string {
	sources := make([]string, 0, pairs)
	for pair := 0; pair < pairs; pair++ {
		sources = append(sources, fmt.Sprintf("$%d", 2*pair+1))
	}
	return strings.Join(sources, ", ")
}

// sortPairsBySource orders pairs by their source identifier.
func sortPairsBySource(pairs [][2]string) {
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
}

package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BankLifecycle is the set of bank columns a whole-bank transfer archive
// carries verbatim but the Bank projection does not hold: the bank's internal
// identity and the two lifecycle timestamps. The reference dumps the bank row
// with every column, so an archive it produces names them; this is what lets
// this service's producer do the same.
type BankLifecycle struct {
	// InternalID is the bank's surrogate identity (a uuid), distinct from the
	// caller-chosen bank_id.
	InternalID string
	// LastConsolidatedAt is when the bank was last consolidated, nil if never.
	LastConsolidatedAt *time.Time
	// MissionChangedAt is when the bank's standing instruction last changed,
	// nil if never.
	MissionChangedAt *time.Time
}

// BankLifecycleSource is the optional capability of a backend that keeps the
// bank columns beyond the Bank projection. A backend that does not implement it
// contributes those columns as absent, which the archive reader treats as
// unset.
type BankLifecycleSource interface {
	// BankLifecycle returns a bank's internal identity and lifecycle
	// timestamps. A missing bank reports ErrBankNotFound.
	BankLifecycle(ctx context.Context, bankID string) (BankLifecycle, error)
}

// Postgres store backs the bank lifecycle read.
var _ BankLifecycleSource = (*PostgresStore)(nil)

// BankLifecycle reads a bank's internal identity and lifecycle timestamps.
func (s *PostgresStore) BankLifecycle(ctx context.Context, bankID string) (BankLifecycle, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return BankLifecycle{}, schemaErr
	}
	var out BankLifecycle
	var internalID *string
	var lastConsolidated, missionChanged *time.Time
	query := `SELECT internal_id::text, last_consolidated_at, mission_changed_at
	          FROM ` + quoteIdent(schema) + `.banks WHERE bank_id = $1`
	err := s.reader().QueryRowContext(ctx, query, bankID).
		Scan(&internalID, &lastConsolidated, &missionChanged)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BankLifecycle{}, ErrBankNotFound
		}
		return BankLifecycle{}, err
	}
	if internalID != nil {
		out.InternalID = *internalID
	}
	out.LastConsolidatedAt = lastConsolidated
	out.MissionChangedAt = missionChanged
	return out, nil
}

// The in-process store keeps no internal identity or lifecycle timestamps, so
// it contributes none.
var _ BankLifecycleSource = (*MemoryStore)(nil)

// BankLifecycle reports no lifecycle columns: the in-process store keeps none.
func (s *MemoryStore) BankLifecycle(context.Context, string) (BankLifecycle, error) {
	return BankLifecycle{}, nil
}

package storage

import "context"

// BankHasUnits reports whether a bank holds any unit at all.
//
// The in-memory backend implements the same capability as PostgreSQL so the
// team-read tests can exercise the per-bank decision without a database. Its
// semantics are the same trivial question — "does this bank have any row" — which
// is why this one has an in-memory analogue while MergeStore does not.
func (s *MemoryStore) BankHasUnits(ctx context.Context, bankID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return false, s.readErr
	}
	for _, unit := range s.units {
		if unit.BankID == bankID {
			return true, nil
		}
	}
	// The recall index is keyed by bank, so a non-empty entry is itself units.
	return len(s.memories[bankID]) > 0, nil
}

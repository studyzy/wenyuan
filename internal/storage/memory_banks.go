package storage

import (
	"context"
	"time"
)

// Migrate marks the in-process store as schema-current. The embedded migration
// series is validated even though there is no live database to alter.
func (s *MemoryStore) Migrate(ctx context.Context) error {
	if _, err := MigrationSeries(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.migrated = true
	return nil
}

// Migrated reports whether Migrate has run.
func (s *MemoryStore) Migrated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.migrated
}

// Ready reports whether the store is usable for readiness probing.
func (s *MemoryStore) Ready(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyErr
}

// ListBanks returns one page of banks ordered most-recently-written first,
// with never-written banks falling back to their creation time. The search
// filter matches case-insensitively against the identifier, the display name
// and any alias. The pre-paging match count is reported alongside the page.
func (s *MemoryStore) ListBanks(ctx context.Context, query BankQuery) (BankPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matched := make([]Bank, 0, len(s.order))
	matchedAliases := map[string][]string{}
	for _, id := range s.order {
		bank := s.banks[id]
		aliases := s.aliasNamesLocked(id)
		if !bankMatches(bank, aliases, query.Search) {
			continue
		}
		bank.FactCount = len(s.memories[id])
		if hits := aliasHits(aliases, query.Search); len(hits) > 0 {
			matchedAliases[id] = hits
		}
		matched = append(matched, bank)
	}

	sortBanksByRecency(matched)

	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return BankPage{
		Banks:          matched[start:end],
		Total:          total,
		Limit:          query.Limit,
		Offset:         query.Offset,
		MatchedAliases: matchedAliases,
	}, nil
}

// CreateBank inserts a bank and returns the stored representation.
func (s *MemoryStore) CreateBank(ctx context.Context, bank Bank) (Bank, error) {
	if !ValidBankID(bank.BankID) {
		return Bank{}, ErrInvalidBankID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.banks[bank.BankID]; exists {
		return Bank{}, ErrOperationConflict
	}
	if _, ok := s.aliasBankLocked(bank.BankID); ok {
		return Bank{}, ErrAliasConflict
	}
	now := time.Now().UTC()
	bank.CreatedAt = now
	bank.UpdatedAt = now
	applyDispositionDefaults(&bank)
	storeBankDefaults(&bank)
	s.banks[bank.BankID] = bank
	s.order = append(s.order, bank.BankID)
	s.invalidateBankLocked(bank.BankID)
	return bank, nil
}

// GetBank returns one bank by identifier.
func (s *MemoryStore) GetBank(ctx context.Context, bankID string) (Bank, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bank, ok := s.banks[bankID]
	if !ok {
		return Bank{}, ErrBankNotFound
	}
	bank.FactCount = len(s.memories[bankID])
	return bank, nil
}

// ReplaceBank overwrites a bank's configuration but leaves its memories in
// place.
func (s *MemoryStore) ReplaceBank(ctx context.Context, bank Bank) (Bank, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.banks[bank.BankID]
	if !ok {
		return Bank{}, ErrBankNotFound
	}
	bank.CreatedAt = existing.CreatedAt
	bank.UpdatedAt = time.Now().UTC()
	bank.LastWriteAt = existing.LastWriteAt
	bank.LastDocumentAt = existing.LastDocumentAt
	applyDispositionDefaults(&bank)
	storeBankDefaults(&bank)
	s.banks[bank.BankID] = bank
	s.invalidateBankLocked(bank.BankID)
	return bank, nil
}

// UpdateBank applies a partial bank update to an existing bank.
func (s *MemoryStore) UpdateBank(ctx context.Context, bankID string, patch BankPatch) (Bank, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bank, ok := s.banks[bankID]
	if !ok {
		return Bank{}, ErrBankNotFound
	}
	applyBankPatch(&bank, patch)
	bank.UpdatedAt = time.Now().UTC()
	s.banks[bankID] = bank
	s.invalidateBankLocked(bankID)
	return bank, nil
}

// DeleteBank removes a bank and its owned rows, returning the number of bank
// rows removed. Deleting a bank that never existed is a zero-item no-op.
func (s *MemoryStore) DeleteBank(ctx context.Context, bankID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.banks[bankID]; !ok {
		return 0, nil
	}
	delete(s.banks, bankID)
	delete(s.memories, bankID)
	delete(s.aliasesByBank, bankID)
	for alias, owner := range s.aliasOwners {
		if owner == bankID {
			delete(s.aliasOwners, alias)
		}
	}
	for i, id := range s.order {
		if id == bankID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.invalidateBankLocked(bankID)
	return 1, nil
}

// ResolveBankID maps an alias to its canonical bank identifier. A name that is
// itself a bank wins over an alias of the same text.
func (s *MemoryStore) ResolveBankID(ctx context.Context, name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.banks[name]; ok {
		return name, nil
	}
	if owner, ok := s.aliasBankLocked(name); ok {
		return owner, nil
	}
	return name, nil
}

// applyBankPatch mutates a bank with the supplied non-nil patch fields.
//
// The disposition is one map, so a patch merges into the traits already set
// rather than replacing the set: a caller that sets one trait means to change
// that trait, not to clear the other two.
func applyBankPatch(bank *Bank, patch BankPatch) {
	if patch.Name != nil {
		bank.Name = *patch.Name
	}
	if patch.Disposition != nil {
		merged := bank.NormalizedDisposition()
		for key, value := range *patch.Disposition {
			merged[key] = value
		}
		bank.Disposition = merged
	}
	if patch.Mission != nil {
		bank.Mission = *patch.Mission
	}
}

// applyDispositionDefaults fills an absent trait with the neutral value, so a
// partially populated bank reads as fully specified rather than as a bank with
// a zero trait.
func applyDispositionDefaults(bank *Bank) {
	bank.Disposition = bank.NormalizedDisposition()
}

// storeBankDefaults fills a bank's display name from its identifier when the
// name was omitted, so an implicitly created bank is immediately visible.
func storeBankDefaults(bank *Bank) {
	if bank.Name == "" {
		bank.Name = bank.BankID
	}
}

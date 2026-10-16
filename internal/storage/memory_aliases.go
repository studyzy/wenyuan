package storage

import (
	"context"
	"sort"
	"time"
)

// ListAliases returns a bank's aliases with the displayed alias first, then the
// remaining aliases oldest first.
func (s *MemoryStore) ListAliases(ctx context.Context, bankID string) ([]BankAlias, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.banks[bankID]; !ok {
		return nil, ErrBankNotFound
	}
	return s.aliasesForLocked(bankID), nil
}

// AddAlias attaches an alias to a bank. A self-alias, a malformed alias, or an
// alias that already names a bank or another alias is refused.
func (s *MemoryStore) AddAlias(ctx context.Context, bankID, alias string, primary bool) ([]BankAlias, error) {
	if !ValidBankID(alias) {
		return nil, ErrInvalidBankID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.banks[bankID]; !ok {
		return nil, ErrBankNotFound
	}
	if alias == bankID {
		return nil, ErrInvalidBankID
	}
	if _, ok := s.banks[alias]; ok {
		return nil, ErrAliasConflict
	}
	if _, ok := s.aliasBankLocked(alias); ok {
		return nil, ErrAliasConflict
	}

	now := time.Now().UTC()
	s.aliasesByBank[bankID] = append(s.aliasesByBank[bankID], BankAlias{
		Alias:     alias,
		BankID:    bankID,
		Primary:   primary,
		CreatedAt: now,
	})
	s.aliasOwners[alias] = bankID
	if primary {
		s.demoteOthersLocked(bankID, alias)
	}
	s.invalidateBankLocked(bankID)
	// The negative an earlier resolution cached for this name would otherwise
	// keep the new alias invisible on this process for the whole TTL.
	aliasCacheRef().invalidate(s.schema, alias)
	return s.aliasesForLocked(bankID), nil
}

// SetAliasPrimary displays the bank under the named alias when primary is true,
// demoting the incumbent in the same step. When primary is false it clears only
// that alias's display flag, so the bank falls back to its own id and no other
// alias is touched.
func (s *MemoryStore) SetAliasPrimary(ctx context.Context, bankID, alias string, primary bool) ([]BankAlias, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.banks[bankID]; !ok {
		return nil, ErrBankNotFound
	}
	if !s.ownsAliasLocked(bankID, alias) {
		return nil, ErrAliasNotFound
	}
	if primary {
		s.demoteOthersLocked(bankID, alias)
	} else {
		entries := s.aliasesByBank[bankID]
		for i := range entries {
			if entries[i].Alias == alias {
				entries[i].Primary = false
			}
		}
	}
	s.invalidateBankLocked(bankID)
	return s.aliasesForLocked(bankID), nil
}

// RemoveAlias detaches the named alias from the bank.
func (s *MemoryStore) RemoveAlias(ctx context.Context, bankID, alias string) ([]BankAlias, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.banks[bankID]; !ok {
		return nil, ErrBankNotFound
	}
	if !s.ownsAliasLocked(bankID, alias) {
		return nil, ErrAliasNotFound
	}
	kept := make([]BankAlias, 0, len(s.aliasesByBank[bankID]))
	for _, entry := range s.aliasesByBank[bankID] {
		if entry.Alias == alias {
			continue
		}
		kept = append(kept, entry)
	}
	s.aliasesByBank[bankID] = kept
	delete(s.aliasOwners, alias)
	if len(kept) == 0 {
		delete(s.aliasesByBank, bankID)
	}
	s.invalidateBankLocked(bankID)
	// A cached resolution for the detached name would otherwise keep routing to
	// the bank on this process for the whole TTL.
	aliasCacheRef().invalidate(s.schema, alias)
	return s.aliasesForLocked(bankID), nil
}

// aliasesForLocked returns a bank's aliases displayed-first then oldest-first.
func (s *MemoryStore) aliasesForLocked(bankID string) []BankAlias {
	entries := append([]BankAlias(nil), s.aliasesByBank[bankID]...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Primary != entries[j].Primary {
			return entries[i].Primary
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	return entries
}

// demoteOthersLocked clears the displayed flag on every alias of a bank except
// the named one, which is displayed.
func (s *MemoryStore) demoteOthersLocked(bankID, keep string) {
	entries := s.aliasesByBank[bankID]
	for i := range entries {
		entries[i].Primary = entries[i].Alias == keep
	}
}

// ownsAliasLocked reports whether the bank owns the named alias.
func (s *MemoryStore) ownsAliasLocked(bankID, alias string) bool {
	for _, entry := range s.aliasesByBank[bankID] {
		if entry.Alias == alias {
			return true
		}
	}
	return false
}

// aliasBankLocked returns the bank a name is an alias of, if any.
func (s *MemoryStore) aliasBankLocked(name string) (string, bool) {
	owner, ok := s.aliasOwners[name]
	return owner, ok
}

// aliasNamesLocked returns a bank's alias texts.
func (s *MemoryStore) aliasNamesLocked(bankID string) []string {
	entries := s.aliasesByBank[bankID]
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Alias)
	}
	return names
}

// PrimaryAlias returns a bank's displayed alias text, or "" when none.
func (s *MemoryStore) PrimaryAlias(ctx context.Context, bankID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.aliasesByBank[bankID] {
		if entry.Primary {
			return entry.Alias
		}
	}
	return ""
}

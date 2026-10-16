package storage

import (
	"context"
	"strings"
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
	if _, exists := s.banks[bank.BankID]; exists {
		s.mu.Unlock()
		return Bank{}, ErrOperationConflict
	}
	if _, ok := s.aliasBankLocked(bank.BankID); ok {
		s.mu.Unlock()
		return Bank{}, ErrAliasConflict
	}
	now := time.Now().UTC()
	bank.CreatedAt = now
	bank.UpdatedAt = now
	storeBankDefaults(&bank)
	s.banks[bank.BankID] = bank
	s.order = append(s.order, bank.BankID)
	s.invalidateBankLocked(bank.BankID)
	hook := s.bankCreateHook
	// The hook runs without the lock: it provisions the bank through the same
	// store, so holding the write lock here would deadlock.
	s.mu.Unlock()
	runBankCreateHook(ctx, hook, bank.BankID)
	return bank, nil
}

// EnsureBank lazily creates the bank when it is absent, so a bank-scoped write
// that addresses a bank by name does not need the caller to declare it first.
func (s *MemoryStore) EnsureBank(ctx context.Context, bankID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if _, ok := s.banks[bankID]; ok {
		s.mu.Unlock()
		return nil
	}
	if !ValidBankID(bankID) {
		s.mu.Unlock()
		return ErrInvalidBankID
	}
	if _, ok := s.aliasBankLocked(bankID); ok {
		s.mu.Unlock()
		return ErrAliasConflict
	}
	now := time.Now().UTC()
	bank := Bank{BankID: bankID, CreatedAt: now, UpdatedAt: now}
	storeBankDefaults(&bank)
	s.banks[bankID] = bank
	s.order = append(s.order, bankID)
	s.invalidateBankLocked(bankID)
	hook := s.bankCreateHook
	s.mu.Unlock()
	runBankCreateHook(ctx, hook, bankID)
	return nil
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

// DeleteBank removes a bank and its content — memory units, documents,
// entities, links and the rows that hang off them — returning the number of
// units, entities and documents removed. Deleting a bank that never existed is
// a zero-item no-op.
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
	// The bank's content is purged with it. Neither backend cascades from the
	// bank row, so an explicit purge is what keeps a deleted bank from leaving
	// its units, documents and entities behind.
	units := 0
	for id, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		delete(s.units, id)
		delete(s.unitEntities, id)
		delete(s.observationSources, id)
		delete(s.memoryHistory, id)
		units++
	}
	for id, unit := range s.invalidated {
		if unit.BankID == bankID {
			delete(s.invalidated, id)
		}
	}
	documents := 0
	for id, document := range s.documents {
		if document.BankID == bankID {
			delete(s.documents, id)
			documents++
		}
	}
	entities := 0
	for id, entity := range s.entities {
		if entity.BankID == bankID {
			delete(s.entities, id)
			entities++
		}
	}
	chunks := s.chunks[:0]
	for _, chunk := range s.chunks {
		if chunk.BankID == bankID {
			continue
		}
		chunks = append(chunks, chunk)
	}
	s.chunks = chunks
	links := s.links[:0]
	for _, link := range s.links {
		if link.BankID == bankID {
			continue
		}
		links = append(links, link)
	}
	s.links = links
	for key := range s.cooccurrences {
		if strings.HasPrefix(key, bankID+"\x00") {
			delete(s.cooccurrences, key)
		}
	}
	for key, attachment := range s.attachments {
		if attachment.BankID == bankID {
			delete(s.attachments, key)
		}
	}
	s.invalidateBankLocked(bankID)
	// The reported count is the backend's: memory units, entities and
	// documents, matching the reference's delete envelope.
	return units + entities + documents, nil
}

// ResolveBankID maps an alias to its canonical bank identifier. A name that is
// itself a bank wins over an alias of the same text.
func (s *MemoryStore) ResolveBankID(ctx context.Context, name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.banks[name]; ok {
		// A real bank always wins, so it is never shadowed by an alias of the
		// same text and never needs a cache entry of its own.
		return name, nil
	}
	cache := aliasCacheRef()
	if cached, ok := cache.get(s.schema, name); ok {
		return cached, nil
	}
	if owner, ok := s.aliasBankLocked(name); ok {
		cache.put(s.schema, name, owner)
		return owner, nil
	}
	// A miss is worth caching too: the next lookup of this name skips the store.
	cache.put(s.schema, name, name)
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
	if patch.ClearDisposition {
		bank.Disposition = nil
	}
	if patch.Disposition != nil {
		// The patch carries only the traits the caller set, so it merges into
		// the raw map rather than a normalized one: normalizing here would
		// materialize every unset trait and lose which ones were explicit.
		merged := make(map[string]int, len(bank.Disposition)+len(*patch.Disposition))
		for key, value := range bank.Disposition {
			merged[key] = value
		}
		for key, value := range *patch.Disposition {
			merged[key] = value
		}
		bank.Disposition = merged
	}
	if patch.Mission != nil {
		bank.Mission = *patch.Mission
	}
	if patch.ClearConfig {
		bank.Config = nil
	}
	if patch.Config != nil {
		merged := map[string]any{}
		for key, value := range bank.Config {
			merged[key] = value
		}
		for key, value := range *patch.Config {
			if value == nil {
				delete(merged, key)
				continue
			}
			merged[key] = value
		}
		bank.Config = merged
	}
}

// storeBankDefaults fills a bank's display name from its identifier when the
// name was omitted, so an implicitly created bank is immediately visible.
func storeBankDefaults(bank *Bank) {
	if bank.Name == "" {
		bank.Name = bank.BankID
	}
}

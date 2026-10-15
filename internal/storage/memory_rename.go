package storage

import (
	"context"
	"strings"
)

// RenameBank moves a bank and every row scoped to it from source to target, so
// a rename preserves the bank's data instead of recreating it empty.
//
// It is the in-process counterpart of the PostgreSQL implementation: the bank
// identity, its aliases, its bank-keyed collections, and every identifier-keyed
// record that carries a bank field move together. An unknown source is
// ErrBankNotFound and an occupied target is ErrBankExists, so a rename can
// never silently drop the source.
func (s *MemoryStore) RenameBank(ctx context.Context, source, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidBankID(target) {
		return ErrInvalidBankID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	bank, ok := s.banks[source]
	if !ok {
		return ErrBankNotFound
	}
	if source == target {
		return nil
	}
	if _, exists := s.banks[target]; exists {
		return ErrBankExists
	}

	// Bank identity and ordering.
	delete(s.banks, source)
	bank.BankID = target
	s.banks[target] = bank
	for i, id := range s.order {
		if id == source {
			s.order[i] = target
		}
	}

	// Aliases move with the bank.
	if aliases, ok := s.aliasesByBank[source]; ok {
		delete(s.aliasesByBank, source)
		for i := range aliases {
			aliases[i].BankID = target
		}
		s.aliasesByBank[target] = aliases
	}
	for alias, owner := range s.aliasOwners {
		if owner == source {
			s.aliasOwners[alias] = target
		}
	}

	// Collections keyed directly by bank identifier.
	rekeyBankKeyed(s.memories, source, target)
	rekeyBankKeyed(s.mentalModels, source, target)
	rekeyBankKeyed(s.retentionRows, source, target)
	rekeyBankKeyed(s.directives, source, target)

	// Collections keyed by a composite whose bank identifier is the prefix.
	rekeyBankPrefixed(s.attachments, source, target)
	rekeyBankPrefixed(s.webhooks, source, target)
	rekeyBankPrefixed(s.mentalModelRecords, source, target)
	rekeyBankPrefixed(s.mentalModelHistory, source, target)
	rekeyBankPrefixed(s.directiveRecords, source, target)
	rekeyBankPrefixed(s.knowledgeNodes, source, target)
	rekeyStoredFiles(s.storedFiles, source, target)

	// Identifier-keyed records that carry their bank in a field.
	for id, doc := range s.documents {
		if doc.BankID == source {
			doc.BankID = target
			s.documents[id] = doc
		}
	}
	for i := range s.chunks {
		if s.chunks[i].BankID == source {
			s.chunks[i].BankID = target
		}
	}
	for id, unit := range s.units {
		if unit.BankID == source {
			unit.BankID = target
			s.units[id] = unit
		}
	}
	for id, entity := range s.entities {
		if entity.BankID == source {
			entity.BankID = target
			s.entities[id] = entity
		}
	}
	for i := range s.links {
		if s.links[i].BankID == source {
			s.links[i].BankID = target
		}
	}
	for id, op := range s.operations {
		if op.BankID == source {
			op.BankID = target
			s.operations[id] = op
		}
	}
	for i := range s.auditEntries {
		if s.auditEntries[i].BankID == source {
			s.auditEntries[i].BankID = target
		}
	}
	for i := range s.llmRequests {
		if s.llmRequests[i].BankID == source {
			s.llmRequests[i].BankID = target
		}
	}
	s.invalidateBankLocked(source)
	s.invalidateBankLocked(target)
	return nil
}

// rekeyBankKeyed moves the value under source to target in a map keyed by bank
// identifier.
func rekeyBankKeyed[V any](m map[string]V, source, target string) {
	if value, ok := m[source]; ok {
		delete(m, source)
		m[target] = value
	}
}

// rekeyBankPrefixed rewrites the keys of a map whose key is "<bank>\x00<id>".
func rekeyBankPrefixed[V any](m map[string]V, source, target string) {
	prefix := source + "\x00"
	for key, value := range m {
		if strings.HasPrefix(key, prefix) {
			delete(m, key)
			m[target+"\x00"+key[len(prefix):]] = value
		}
	}
}

// rekeyStoredFiles rewrites the bank segment of "banks/<bank>/<name>" keys.
func rekeyStoredFiles(m map[string]StoredFile, source, target string) {
	oldPrefix := "banks/" + encodeSegment(source) + "/"
	newPrefix := "banks/" + encodeSegment(target) + "/"
	for key, value := range m {
		if strings.HasPrefix(key, oldPrefix) {
			delete(m, key)
			m[newPrefix+key[len(oldPrefix):]] = value
		}
	}
}

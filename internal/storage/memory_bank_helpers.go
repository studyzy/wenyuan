package storage

import (
	"sort"
	"strings"
	"time"
)

// bankMatches reports whether a bank matches the search filter against its
// identifier, display name or any alias.
func bankMatches(bank Bank, aliases []string, search string) bool {
	if matchesSearch(bank.BankID, search) || matchesSearch(bank.Name, search) {
		return true
	}
	return len(aliasHits(aliases, search)) > 0
}

// aliasHits returns the aliases that matched the search filter. An empty
// search produces no hits, so a row matched on its own fields reports none.
func aliasHits(aliases []string, search string) []string {
	if search == "" {
		return nil
	}
	hits := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if matchesSearch(alias, search) {
			hits = append(hits, alias)
		}
	}
	return hits
}

// aliasesExplaining returns the aliases worth reporting for one search hit: the
// aliases that matched the filter, but only when the bank's own id or name does
// not already contain the search text. This answers "why is this row in my
// results", so a row that explains itself needs no footnote. The list is sorted
// by alias text, matching the reference's ORDER BY alias.
func aliasesExplaining(bankID, name string, aliases []string, search string) []string {
	if search == "" {
		return nil
	}
	needle := strings.ToUpper(search)
	if strings.Contains(strings.ToUpper(bankID), needle) || strings.Contains(strings.ToUpper(name), needle) {
		return nil
	}
	hits := aliasHits(aliases, search)
	if len(hits) == 0 {
		return nil
	}
	sort.Strings(hits)
	return hits
}

// sortBanksByRecency orders banks most-recently-written first, falling back to
// the creation time for a never-written bank, with the identifier as a stable
// tie-breaker so pages remain disjoint and deterministic.
func sortBanksByRecency(banks []Bank) {
	sort.SliceStable(banks, func(i, j int) bool {
		li, lj := recencyKey(banks[i]), recencyKey(banks[j])
		if !li.Equal(lj) {
			return li.After(lj)
		}
		return banks[i].BankID < banks[j].BankID
	})
}

func recencyKey(bank Bank) time.Time {
	if bank.LastWriteAt != nil {
		return *bank.LastWriteAt
	}
	return bank.CreatedAt
}

// pageBounds clamps a limit/offset window to the match count. A limit of zero
// is a valid explicit page size that returns nothing (the reference's list
// endpoints declare ge=0); a negative limit is the internal "unbounded"
// sentinel. An offset past the end yields an empty slice.
func pageBounds(total, offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	switch {
	case limit < 0:
		end = total
	case limit == 0:
		end = offset
	case offset+limit < total:
		end = offset + limit
	}
	return offset, end
}

// invalidateBankLocked drops the per-process profile/configuration cache entry
// for a bank after any bank-row write in this process.
func (s *MemoryStore) invalidateBankLocked(bankID string) {
	if s.cache == nil {
		return
	}
	s.cache.invalidate(bankID)
}

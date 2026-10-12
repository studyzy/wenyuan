package storage

import (
	"sort"
	"time"
)

// bankMatches reports whether a bank matches the search filter against its
// identifier, display name or any alias.
func bankMatches(bank Bank, aliases []string, search string) bool {
	if matchesSearch(bank.ID, search) || matchesSearch(bank.Name, search) {
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

// sortBanksByRecency orders banks most-recently-written first, falling back to
// the creation time for a never-written bank, with the identifier as a stable
// tie-breaker so pages remain disjoint and deterministic.
func sortBanksByRecency(banks []Bank) {
	sort.SliceStable(banks, func(i, j int) bool {
		li, lj := recencyKey(banks[i]), recencyKey(banks[j])
		if !li.Equal(lj) {
			return li.After(lj)
		}
		return banks[i].ID < banks[j].ID
	})
}

func recencyKey(bank Bank) time.Time {
	if bank.LastWriteAt != nil {
		return *bank.LastWriteAt
	}
	return bank.CreatedAt
}

// pageBounds clamps a limit/offset window to the match count. A zero limit
// means "no limit" and is reported unchanged to the caller. An offset past the
// end yields an empty slice.
func pageBounds(total, offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < total {
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

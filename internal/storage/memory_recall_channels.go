package storage

import (
	"sort"
	"strings"
)

// attachSideChannels populates the entity, chunk and source-fact channels the
// request asked for. Each channel is independent of the fact token cap.
func (s *MemoryStore) attachSideChannels(result *RecallResult, q RecallQuery, types []string) {
	if q.IncludeEntities {
		result.EntityStates = s.entityStatesFor(q.BankID, result.Matches)
	}
	if q.IncludeChunks {
		result.ChunkEntries = s.chunksFor(result.Matches)
	}
	if q.IncludeSourceFacts {
		facts, truncated := s.sourceFactsFor(result.Matches)
		result.SourceFactMap = facts
		result.SourceFactsTruncated = truncated
	}
}

// entityStatesFor assembles the entity side channel keyed by entity name for
// the entities the returned matches carry.
func (s *MemoryStore) entityStatesFor(bankID string, matches []RecallMatch) []EntityState {
	byName := map[string]*EntityState{}
	for _, m := range matches {
		for _, name := range m.Entities {
			state := byName[name]
			if state == nil {
				state = &EntityState{Name: name, ID: name, CanonicalName: name}
				byName[name] = state
			}
			state.Observations = append(state.Observations, EntityObservation{
				Text:        m.Text,
				MentionedAt: m.MentionedAt,
			})
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]EntityState, 0, len(names))
	for _, name := range names {
		out = append(out, *byName[name])
	}
	return out
}

// chunksFor assembles the chunk side channel keyed by chunk id for the chunks
// the returned matches reference.
func (s *MemoryStore) chunksFor(matches []RecallMatch) []ChunkEntry {
	seen := map[string]bool{}
	out := make([]ChunkEntry, 0)
	for _, m := range matches {
		if m.ChunkID == "" || seen[m.ChunkID] {
			continue
		}
		seen[m.ChunkID] = true
		entry := ChunkEntry{ID: m.ChunkID}
		for _, chunk := range s.chunks {
			if chunk.ChunkID == m.ChunkID {
				entry.Text = chunk.ChunkText
				entry.Index = chunk.ChunkIndex
				break
			}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sourceFactsFor assembles the source-fact map keyed by source-fact id for the
// source facts the returned observations cite.
func (s *MemoryStore) sourceFactsFor(matches []RecallMatch) (map[string]string, bool) {
	out := map[string]string{}
	for _, m := range matches {
		for _, id := range m.SourceMemoryIDs {
			if _, ok := out[id]; ok {
				continue
			}
			out[id] = sourceFactText(id, s)
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return out, false
}

// sourceFactText resolves a source-fact id to its text when it names a stored
// memory unit; otherwise the id itself is a stable placeholder.
func sourceFactText(id string, s *MemoryStore) string {
	if unit, ok := s.units[id]; ok {
		return unit.Text
	}
	return id
}

// wordSet lowercases a string and splits it into a set of alphabetic words.
func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, field := range strings.FieldsFunc(s, func(r rune) bool {
		return !isWordRune(r)
	}) {
		out[strings.ToLower(field)] = true
	}
	return out
}

// hasWordChar reports whether a string carries at least one searchable word
// character.
func hasWordChar(s string) bool {
	for _, r := range s {
		if isWordRune(r) {
			return true
		}
	}
	return false
}

// isWordRune reports whether a rune is a letter, digit or underscore.
func isWordRune(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r > 127
}

// abs64 returns the absolute value of an int64.
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// cloneIntMap copies a string-to-int map.
func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

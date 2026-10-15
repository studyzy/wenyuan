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
		result.ChunkEntries = s.chunksFor(q, result.Matches)
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
// the returned matches reference. A chunk is returned only when its document
// passes the reader's tag filter, so a fact shared through a tag never drags
// along a document the reader's filter excludes (#5030); a chunk with no
// document is withheld under a filter (fail closed).
func (s *MemoryStore) chunksFor(q RecallQuery, matches []RecallMatch) []ChunkEntry {
	seen := map[string]bool{}
	out := make([]ChunkEntry, 0)
	documentIDs := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.ChunkID == "" || seen[m.ChunkID] {
			continue
		}
		documentIDs = append(documentIDs, m.DocumentID)
	}
	visible := s.visibleDocumentIDsLocked(q.BankID, documentIDs, q.Tags, q.TagsMatch, q.TagGroups)
	active := tagFilterActive(q.Tags, q.TagsMatch, q.TagGroups)
	for _, m := range matches {
		if m.ChunkID == "" || seen[m.ChunkID] {
			continue
		}
		if m.DocumentID != "" && !visible[m.DocumentID] {
			continue
		}
		if m.DocumentID == "" && active {
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
func (s *MemoryStore) sourceFactsFor(matches []RecallMatch) (map[string]RecallMatch, bool) {
	out := map[string]RecallMatch{}
	for _, m := range matches {
		for _, id := range m.SourceMemoryIDs {
			if _, ok := out[id]; ok {
				continue
			}
			out[id] = sourceFactMatch(id, s)
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return out, false
}

// sourceFactMatch resolves a source-fact id to the stored unit it names, so the
// source-fact channel can carry the same result shape as the ranked list. An
// id that names no stored unit becomes a placeholder carrying its own id as
// text, which keeps the reference stable without inventing a fact.
func sourceFactMatch(id string, s *MemoryStore) RecallMatch {
	if unit, ok := s.units[id]; ok {
		return RecallMatch{
			ID:            unit.ID,
			Text:          unit.Text,
			Type:          string(unit.FactType),
			DocumentID:    unit.DocumentID,
			ChunkID:       unit.ChunkID,
			OccurredStart: unit.OccurredStart,
			OccurredEnd:   unit.OccurredEnd,
			MentionedAt:   unit.MentionedAt,
		}
	}
	return RecallMatch{ID: id, Text: id}
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

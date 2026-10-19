package storage

import (
	"sort"
	"strings"
)

// attachSideChannels populates the chunk and source-fact channels the request
// asked for. Each channel is independent of the fact token cap.
func (s *MemoryStore) attachSideChannels(result *RecallResult, q RecallQuery, types []string) {
	if q.IncludeChunks {
		result.ChunkEntries = s.chunksFor(q, result.Matches)
	}
	if q.IncludeSourceFacts {
		facts, truncated := s.sourceFactsFor(result.Matches)
		result.SourceFactMap = facts
		result.SourceFactsTruncated = truncated
	}
}

// attachRecallEntities resolves the entities the returned matches carry and
// builds the name-keyed entity channel from them.
//
// It runs after the fact token budget has selected the returned matches: the
// entity channel is a view of those matches, so a fact the budget dropped must
// not leave its entities behind. A match that resolves to no entity keeps a
// null list and contributes no channel entry.
func (s *MemoryStore) attachRecallEntities(q RecallQuery, result *RecallResult) {
	if len(result.Matches) == 0 {
		return
	}
	ids := make([]string, 0, len(result.Matches))
	for _, m := range result.Matches {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	applyRecallEntities(result, s.recallEntityRefs(q.BankID, ids))
}

// recallEntityRefs resolves the entities each unit carries, keyed by unit id,
// in entity-id order. Direct postings answer for most units; an observation
// with no postings of its own inherits its source memories' entities, which is
// the set the unit's own detail read reports. A unit that resolves to no
// entity is absent from the result rather than mapped to an empty list.
//
// The caller holds the store's read lock.
func (s *MemoryStore) recallEntityRefs(bankID string, unitIDs []string) map[string][]EntityRef {
	out := make(map[string][]EntityRef, len(unitIDs))
	for _, unitID := range unitIDs {
		unit, ok := s.units[unitID]
		if !ok {
			unit, ok = s.invalidated[unitID]
		}
		if !ok || unit.BankID != bankID {
			continue
		}
		ids := s.unitEntities[unitID]
		if len(ids) == 0 && unit.FactType == TypeObservation && len(unit.SourceMemoryIDs) > 0 {
			for _, sourceID := range unit.SourceMemoryIDs {
				source, ok := s.units[sourceID]
				if !ok || source.BankID != bankID {
					continue
				}
				ids = append(ids, s.unitEntities[sourceID]...)
			}
		}
		if refs := entityRefsFor(ids, s.entities); len(refs) > 0 {
			out[unitID] = refs
		}
	}
	return out
}

// entityRefsFor maps entity ids onto their registry rows, deduped and ordered
// by entity id — the order the reference's own resolve returns. An id with no
// registry row is dropped, so a dangling posting never invents a label.
func entityRefsFor(ids []string, registry map[string]Entity) []EntityRef {
	if len(ids) == 0 {
		return nil
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	refs := make([]EntityRef, 0, len(ordered))
	seen := make(map[string]bool, len(ordered))
	for _, id := range ordered {
		if seen[id] {
			continue
		}
		seen[id] = true
		entity, ok := registry[id]
		if !ok {
			continue
		}
		refs = append(refs, EntityRef{ID: id, CanonicalName: entity.CanonicalName})
	}
	return refs
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

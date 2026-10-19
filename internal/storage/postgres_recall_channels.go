package storage

import (
	"context"
	"database/sql"
	"sort"
)

// attachRecallSideChannels populates the chunk and source-fact channels the
// request asked for. Each channel is independent of the fact token cap, so a
// small answer can still carry the source text its caller asked for.
//
// The chunk channel honours the source-scope rule (#5030): a chunk is returned
// only when its DOCUMENT passes the reader's tag filter, so a fact that reached
// the caller through a label-derived tag never drags along the rest of a
// document their filter excludes. A chunk with no document, or one whose
// document is out of scope, is withheld — fail closed.
func (s *PostgresStore) attachRecallSideChannels(ctx context.Context, q RecallQuery, result *RecallResult) error {
	if q.IncludeChunks {
		entries, err := s.recallChunkEntries(ctx, q, result.Matches)
		if err != nil {
			return err
		}
		result.ChunkEntries = entries
	}
	if q.IncludeSourceFacts {
		facts, err := s.recallSourceFacts(ctx, q, result.Matches)
		if err != nil {
			return err
		}
		result.SourceFactMap = facts
		result.SourceFactsTruncated = false
	}
	return nil
}

// attachRecallEntities resolves the entities the returned matches carry and
// builds the name-keyed entity channel from them.
//
// It runs after the fact token budget has selected the returned matches: the
// entity channel is a view of those matches, so a fact the budget dropped must
// not leave its entities behind. A match that resolves to no entity keeps a
// null list and contributes no channel entry.
func (s *PostgresStore) attachRecallEntities(ctx context.Context, q RecallQuery, result *RecallResult) error {
	if len(result.Matches) == 0 {
		return nil
	}
	ids := make([]string, 0, len(result.Matches))
	for _, m := range result.Matches {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	refs, err := s.recallUnitEntityRefs(ctx, q.BankID, ids)
	if err != nil {
		return err
	}
	applyRecallEntities(result, refs)
	return nil
}

// recallUnitEntityRefs resolves the entities each unit carries, keyed by unit
// id, in entity-id order. Direct postings answer for most units; an
// observation with no postings of its own inherits its source memories'
// entities, which is the set its own detail read reports. A unit that resolves
// to no entity is absent from the result rather than mapped to an empty list.
func (s *PostgresStore) recallUnitEntityRefs(ctx context.Context, bankID string, unitIDs []string) (map[string][]EntityRef, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	qualified := quoteIdent(s.schema)
	rows, err := s.reader().QueryContext(ctx, `
		SELECT ue.unit_id::text, ue.entity_id::text, e.canonical_name
		FROM `+qualified+`.unit_entities ue
		JOIN `+qualified+`.entities e ON e.id = ue.entity_id
		WHERE ue.unit_id = ANY($1::uuid[]) AND e.bank_id = $2
		UNION
		SELECT obs.id::text, src_ue.entity_id::text, e.canonical_name
		FROM `+qualified+`.memory_units obs
		CROSS JOIN LATERAL unnest(obs.source_memory_ids) AS src_id
		JOIN `+qualified+`.unit_entities src_ue ON src_ue.unit_id = src_id
		JOIN `+qualified+`.entities e ON e.id = src_ue.entity_id
		WHERE obs.id = ANY($1::uuid[]) AND obs.fact_type = 'observation'
		  AND obs.source_memory_ids IS NOT NULL
		  AND e.bank_id = $2
		  AND NOT EXISTS (SELECT 1 FROM `+qualified+`.unit_entities d WHERE d.unit_id = obs.id)
		ORDER BY 1, 2`, unitIDs, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byUnit := map[string][]EntityRef{}
	for rows.Next() {
		var unitID string
		var ref EntityRef
		if err := rows.Scan(&unitID, &ref.ID, &ref.CanonicalName); err != nil {
			return nil, err
		}
		// UNION removes duplicate rows, but a unit can reach one entity
		// through several source memories, so dedupe per unit as well.
		if containsEntityRef(byUnit[unitID], ref.ID) {
			continue
		}
		byUnit[unitID] = append(byUnit[unitID], ref)
	}
	return byUnit, rows.Err()
}

// containsEntityRef reports whether a unit's ref list already names an entity
// id, so a duplicated posting is kept once.
func containsEntityRef(refs []EntityRef, id string) bool {
	for _, ref := range refs {
		if ref.ID == id {
			return true
		}
	}
	return false
}

// recallChunkEntries assembles the chunk side channel keyed by chunk id for the
// chunks the returned matches reference, dropping any chunk whose document the
// reader may not see.
func (s *PostgresStore) recallChunkEntries(ctx context.Context, q RecallQuery, matches []RecallMatch) ([]ChunkEntry, error) {
	ordered := make([]string, 0, len(matches))
	seen := map[string]bool{}
	docByChunk := map[string]string{}
	for _, m := range matches {
		if m.ChunkID == "" || seen[m.ChunkID] {
			continue
		}
		seen[m.ChunkID] = true
		ordered = append(ordered, m.ChunkID)
		docByChunk[m.ChunkID] = m.DocumentID
	}
	if len(ordered) == 0 {
		return nil, nil
	}
	docIDs := make([]string, 0, len(ordered))
	for _, id := range ordered {
		docIDs = append(docIDs, docByChunk[id])
	}
	visible, err := s.visibleDocumentIDs(ctx, q.BankID, docIDs, q.Tags, q.TagsMatch, q.TagGroups)
	if err != nil {
		return nil, err
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT chunk_id, COALESCE(document_id, ''), COALESCE(chunk_index, 0), COALESCE(chunk_text, '')
		 FROM `+quoteIdent(s.schema)+`.chunks
		 WHERE bank_id = $1 AND chunk_id = ANY($2::text[])`, q.BankID, ordered)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	found := map[string]ChunkEntry{}
	for rows.Next() {
		var entry ChunkEntry
		var documentID string
		if err := rows.Scan(&entry.ID, &documentID, &entry.Index, &entry.Text); err != nil {
			return nil, err
		}
		found[entry.ID] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ChunkEntry, 0, len(ordered))
	for _, id := range ordered {
		documentID := docByChunk[id]
		if documentID != "" && !visible[documentID] {
			// The reader's filter excludes the source document, so the chunk is
			// not returned even though the fact matched.
			continue
		}
		if documentID == "" && tagFilterActive(q.Tags, q.TagsMatch, q.TagGroups) {
			// A chunk with no document cannot be checked against the filter, so
			// under a filter it is withheld.
			continue
		}
		entry, ok := found[id]
		if !ok {
			entry = ChunkEntry{ID: id}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// recallSourceFacts resolves the source-fact ids the returned observations cite
// to the stored units they name, keyed by id, so the source-fact channel carries
// the same result shape as the ranked list.
func (s *PostgresStore) recallSourceFacts(ctx context.Context, q RecallQuery, matches []RecallMatch) (map[string]RecallMatch, error) {
	ids := make([]string, 0)
	seen := map[string]bool{}
	for _, m := range matches {
		for _, id := range m.SourceMemoryIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT id::text, COALESCE(text, ''), COALESCE(fact_type, ''), COALESCE(document_id, ''),
		        COALESCE(chunk_id, ''), occurred_start, occurred_end, mentioned_at,
		        COALESCE(metadata, '{}'::jsonb), COALESCE(tags, '{}')
		 FROM `+quoteIdent(s.schema)+`.memory_units
		 WHERE bank_id = $1 AND id::text = ANY($2::text[])`, q.BankID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]RecallMatch{}
	for rows.Next() {
		var m RecallMatch
		var occurredStart, occurredEnd, mentionedAt sql.NullTime
		var metadataRaw, tagsRaw []byte
		if err := rows.Scan(&m.ID, &m.Text, &m.Type, &m.DocumentID, &m.ChunkID,
			&occurredStart, &occurredEnd, &mentionedAt, &metadataRaw, &tagsRaw); err != nil {
			return nil, err
		}
		m.OccurredStart = nullTimePtr(occurredStart)
		m.OccurredEnd = nullTimePtr(occurredEnd)
		m.MentionedAt = nullTimePtr(mentionedAt)
		m.Metadata = decodeJSONMap(metadataRaw)
		m.Tags = decodePostgresTextArray(tagsRaw)
		out[m.ID] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

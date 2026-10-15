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
	rows, err := s.db.QueryContext(ctx,
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
	rows, err := s.db.QueryContext(ctx,
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

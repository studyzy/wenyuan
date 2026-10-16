package storage

import (
	"context"
	"sort"
	"strings"
)

// The store implements the read surface a reflection loop needs, so the loop
// asks for evidence through the same interface every other caller uses rather
// than reaching a table itself.

// ReflectMentalModels returns the bank's perspectives ranked by how much their
// text overlaps the query, best first. Each citation carries the model's
// freshness so the reflection loop can tell a page it may answer from without
// descending to raw facts.
func (s *MemoryStore) ReflectMentalModels(ctx context.Context, bankID, query string, maxResults int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	models := append([]ReflectMentalModel(nil), s.mentalModels[bankID]...)
	models = rankMentalModelsByOverlap(query, models, func(m ReflectMentalModel) string { return m.Text })
	out := make([]ReflectCitation, 0, len(models))
	for _, model := range models {
		if maxResults > 0 && len(out) >= maxResults {
			break
		}
		out = append(out, ReflectCitation{ID: model.ID, Text: model.Text, Fresh: !model.Stale})
	}
	return out, nil
}

// rankMentalModelsByOverlap orders items by word overlap between the query and
// the text the accessor reads, best first and stable on ties.
//
// The reference ranks perspectives by embedding similarity. The offline store
// cannot embed, so word overlap is the deterministic stand-in: it keeps the page
// the question's own words name ahead of the rest, which is what "the
// best-ranked page comes back whole" depends on. An empty query imposes no
// order.
func rankMentalModelsByOverlap[T any](query string, items []T, text func(T) string) []T {
	ranked := append([]T(nil), items...)
	terms := queryTerms(query)
	if len(terms) == 0 {
		return ranked
	}
	type scored struct {
		item  T
		score int
	}
	decorated := make([]scored, len(ranked))
	for i, item := range ranked {
		decorated[i] = scored{item: item, score: termOverlap(terms, text(item))}
	}
	sort.SliceStable(decorated, func(i, j int) bool { return decorated[i].score > decorated[j].score })
	for i, entry := range decorated {
		ranked[i] = entry.item
	}
	return ranked
}

// queryTerms splits a query into distinct lowercase alphanumeric terms.
func queryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

// termOverlap counts how many of the query's terms appear in the text.
func termOverlap(terms []string, text string) int {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		words[word] = true
	}
	count := 0
	for _, term := range terms {
		if words[term] {
			count++
		}
	}
	return count
}

// SearchObservations returns the bank's consolidated conclusions matching a
// query.
func (s *MemoryStore) ReflectObservations(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0)
	budget := s.tuning.internalRecallFactTokens(maxTokens)
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		if query != "" && !containsFold(unit.Text, query) {
			continue
		}
		cost := len(unit.Text)/4 + 1
		if budget > 0 && cost > budget {
			break
		}
		budget -= cost
		out = append(out, ReflectCitation{ID: unit.ID, Text: unit.Text})
		if budget <= 0 {
			break
		}
	}
	return out, nil
}

// ReflectMemories returns the bank's raw memories matching a query.
func (s *MemoryStore) ReflectMemories(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0)
	budget := s.tuning.internalRecallFactTokens(maxTokens)
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType == TypeObservation {
			continue
		}
		if query != "" && !containsFold(unit.Text, query) {
			continue
		}
		cost := len(unit.Text)/4 + 1
		if budget > 0 && cost > budget {
			break
		}
		budget -= cost
		out = append(out, ReflectCitation{ID: unit.ID, Text: unit.Text})
		if budget <= 0 {
			break
		}
	}
	return out, nil
}

// ReadMentalModels returns specific perspectives by identifier.
func (s *MemoryStore) ReflectReadModels(ctx context.Context, bankID string, ids []string) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReflectCitation, 0, len(ids))
	for _, id := range ids {
		for _, model := range s.mentalModels[bankID] {
			if model.ID == id {
				out = append(out, ReflectCitation{ID: model.ID, Text: model.Text})
			}
		}
	}
	return out, nil
}

// ReflectExpand returns the source context of specific memories, opened to the
// requested depth and confined to the reader's tag scope. A memory outside the
// scope is dropped (it reads as not found); a memory inside the scope whose
// DOCUMENT is outside it keeps the memory but withholds its source text, so a
// fact shared through a tag never opens the rest of a document the reader
// cannot see (#5030). With no active filter the source is returned as before.
func (s *MemoryStore) ReflectExpand(ctx context.Context, req ReflectExpandRequest) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := tagFilterActive(req.Tags, req.TagsMatch, req.TagGroups)
	documentIDs := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if unit, ok := s.units[id]; ok && unit.BankID == req.BankID {
			documentIDs = append(documentIDs, unit.DocumentID)
		}
	}
	visible := s.visibleDocumentIDsLocked(req.BankID, documentIDs, req.Tags, req.TagsMatch, req.TagGroups)
	out := make([]ReflectCitation, 0, len(req.IDs))
	for _, id := range req.IDs {
		unit, ok := s.units[id]
		if !ok || unit.BankID != req.BankID {
			continue
		}
		// A memory whose own tags are outside the reader's scope is not found.
		if active && !matchesTagFilter(unit.Tags, req.Tags, req.TagsMatch, req.TagGroups) {
			continue
		}
		// A memory whose document is outside the scope withholds its source.
		if unit.DocumentID != "" && !visible[unit.DocumentID] {
			out = append(out, ReflectCitation{ID: unit.ID, Text: unit.Text})
			continue
		}
		if unit.DocumentID == "" && unit.ChunkID != "" && active {
			// A chunk with no document cannot be checked, so under a filter the
			// source is withheld (fail closed).
			out = append(out, ReflectCitation{ID: unit.ID, Text: unit.Text})
			continue
		}
		text := unit.Text
		switch req.Depth {
		case "chunk":
			if chunkText, ok := s.chunkTextLocked(unit.ChunkID); ok && chunkText != "" {
				text = chunkText
			}
		default: // document, and an unset depth which the expand tool requires
			if doc, ok := s.documents[unit.DocumentID]; ok && doc.OriginalText != "" {
				text = doc.OriginalText
			}
		}
		out = append(out, ReflectCitation{ID: unit.ID, Text: text, DocumentID: unit.DocumentID, ChunkID: unit.ChunkID})
	}
	return out, nil
}

// chunkTextLocked returns a stored chunk's text. The caller holds the lock.
func (s *MemoryStore) chunkTextLocked(chunkID string) (string, bool) {
	if chunkID == "" {
		return "", false
	}
	for _, chunk := range s.chunks {
		if chunk.ChunkID == chunkID {
			return chunk.ChunkText, true
		}
	}
	return "", false
}

// The PostgreSQL store implements the same reflection read surface over its own
// tables, so the model-driven loop runs unchanged against either backend. Each
// method reuses the store's existing read paths rather than reaching a table
// with a parallel query.

// Compile-time proof that the database store is the retrieval surface the
// agentic loop calls back into.
var _ ReflectionRetrieval = (*PostgresStore)(nil)

// ReflectMentalModels returns the bank's stored perspectives, ranked by how
// much their text overlaps the query, each carrying its freshness so the loop
// can tell a current page from a stale one. Freshness is the store's own
// staleness judgement, the same one the maintenance tick uses.
func (s *PostgresStore) ReflectMentalModels(ctx context.Context, bankID, query string, maxResults int) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	models, err := s.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, err
	}
	models = rankMentalModelsByOverlap(query, models, func(m MentalModel) string { return m.Content })
	out := make([]ReflectCitation, 0, len(models))
	for _, model := range models {
		if maxResults > 0 && len(out) >= maxResults {
			break
		}
		out = append(out, ReflectCitation{
			ID:    model.ID,
			Text:  model.Content,
			Fresh: !s.IsStale(ctx, bankID, model),
		})
	}
	return out, nil
}

// ReflectObservations returns the bank's consolidated observations matching a
// query.
func (s *PostgresStore) ReflectObservations(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error) {
	return s.reflectRecall(ctx, bankID, query, maxTokens, []string{string(TypeObservation)})
}

// ReflectMemories returns the bank's raw memories matching a query.
func (s *PostgresStore) ReflectMemories(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error) {
	types := make([]string, 0, len(FinalMemoryUnitTypes()))
	for _, unitType := range FinalMemoryUnitTypes() {
		if unitType == TypeObservation {
			continue
		}
		types = append(types, string(unitType))
	}
	return s.reflectRecall(ctx, bankID, query, maxTokens, types)
}

// internalRecallFactTokens resolves the internal recall's fact-channel budget:
// the caller's budget when it set one, else the deployment's process default,
// and zero (unbounded) when neither is set. The chunk channel's budget and the
// include-chunks switch are read straight from the tuning, because the caller
// names no chunk budget of its own.
func (t Tuning) internalRecallFactTokens(caller int) int {
	if caller > 0 {
		return caller
	}
	return t.InternalRecallMaxFactTokens
}

// reflectRecall runs the shared recall pipeline for one reflection layer. It
// reuses recall's arms and the bank's disabled-arm configuration instead of
// re-implementing retrieval, then applies the reflection token budget to the
// ranked matches the same way the in-memory backend does: a match whose text
// would cross the remaining budget ends the layer.
//
// The reflection retrieval interface carries no tag scope and the in-memory
// backend applies none, so none is applied here either; the recall pipeline
// still honours whatever bank configuration the store holds.
func (s *PostgresStore) reflectRecall(ctx context.Context, bankID, query string, maxTokens int, types []string) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bank, err := s.GetBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	result, err := s.Recall(ctx, RecallQuery{
		BankID:         bankID,
		Query:          query,
		Types:          types,
		DisabledArms:   bank.RecallDisabledArms(),
		IncludeChunks:  s.tuning.InternalRecallIncludeChunks,
		MaxTokens:      s.tuning.internalRecallFactTokens(maxTokens),
		MaxChunkTokens: s.tuning.InternalRecallMaxChunkTokens,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ReflectCitation, 0, len(result.Matches))
	budget := maxTokens
	for _, match := range result.Matches {
		cost := len(match.Text)/4 + 1
		if budget > 0 && cost > budget {
			break
		}
		budget -= cost
		out = append(out, ReflectCitation{
			ID:            match.ID,
			Text:          match.Text,
			Type:          match.Type,
			Context:       match.Context,
			OccurredStart: match.OccurredStart,
			OccurredEnd:   match.OccurredEnd,
			MentionedAt:   match.MentionedAt,
			DocumentID:    match.DocumentID,
			ChunkID:       match.ChunkID,
			Metadata:      match.Metadata,
			Tags:          match.Tags,
			Attachments:   match.Attachments,
		})
		if budget <= 0 {
			break
		}
	}
	return out, nil
}

// ReflectReadModels returns specific perspectives by identifier.
func (s *PostgresStore) ReflectReadModels(ctx context.Context, bankID string, ids []string) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []ReflectCitation{}, nil
	}
	// The identifier is cast to text so it scans into a string exactly as the
	// arm queries do, rather than depending on how the driver renders a uuid.
	query := `SELECT id::text, COALESCE(content, '') FROM ` + quoteIdent(s.schema) + `.mental_models
	          WHERE bank_id = $1 AND id::text = ANY($2::text[])`
	rows, err := s.reader().QueryContext(ctx, query, bankID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byID := map[string]ReflectCitation{}
	for rows.Next() {
		var citation ReflectCitation
		if err := rows.Scan(&citation.ID, &citation.Text); err != nil {
			return nil, err
		}
		byID[citation.ID] = citation
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Preserve the caller's identifier order and drop unknown ones, matching
	// the in-memory backend.
	out := make([]ReflectCitation, 0, len(ids))
	for _, id := range ids {
		if citation, ok := byID[id]; ok {
			out = append(out, citation)
		}
	}
	return out, nil
}

// ReflectExpand returns the source context of specific memories, opened to the
// requested depth and confined to the reader's tag scope. A memory outside the
// scope is dropped (it reads as not found); a memory inside the scope whose
// DOCUMENT is outside it keeps the memory but withholds its source text, so a
// fact shared through a tag never opens the rest of a document the reader
// cannot see (#5030). One query fetches the memories and their document and
// chunk text; the visibility check runs before any source text is substituted.
func (s *PostgresStore) ReflectExpand(ctx context.Context, req ReflectExpandRequest) ([]ReflectCitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(req.IDs) == 0 {
		return []ReflectCitation{}, nil
	}
	active := tagFilterActive(req.Tags, req.TagsMatch, req.TagGroups)
	query := `SELECT u.id::text, COALESCE(u.text, ''), COALESCE(u.document_id, ''), COALESCE(u.chunk_id, ''),
	                 COALESCE(u.tags, '{}'), COALESCE(d.original_text, ''), COALESCE(d.tags, '{}'), COALESCE(c.chunk_text, '')
	          FROM ` + quoteIdent(s.schema) + `.memory_units u
	          LEFT JOIN ` + quoteIdent(s.schema) + `.documents d
	                 ON d.id = u.document_id AND d.bank_id = u.bank_id
	          LEFT JOIN ` + quoteIdent(s.schema) + `.chunks c
	                 ON c.chunk_id = u.chunk_id AND c.bank_id = u.bank_id
	          WHERE u.bank_id = $1 AND u.id::text = ANY($2::text[])`
	rows, err := s.reader().QueryContext(ctx, query, req.BankID, req.IDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type expandRow struct {
		text       string
		documentID string
		chunkID    string
		memoryTags []string
		docText    string
		docTags    []string
		chunkText  string
	}
	byID := map[string]expandRow{}
	for rows.Next() {
		var id string
		var row expandRow
		var memoryTagsRaw, docTagsRaw []byte
		if err := rows.Scan(&id, &row.text, &row.documentID, &row.chunkID,
			&memoryTagsRaw, &row.docText, &docTagsRaw, &row.chunkText); err != nil {
			return nil, err
		}
		row.memoryTags = decodePostgresTextArray(memoryTagsRaw)
		row.docTags = decodePostgresTextArray(docTagsRaw)
		byID[id] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ReflectCitation, 0, len(req.IDs))
	for _, id := range req.IDs {
		row, ok := byID[id]
		if !ok {
			continue
		}
		// A memory whose own tags are outside the reader's scope is not found.
		if active && !matchesTagFilter(row.memoryTags, req.Tags, req.TagsMatch, req.TagGroups) {
			continue
		}
		citation := ReflectCitation{ID: id, Text: row.text}
		// The source is withheld when its document is out of scope, or when a
		// chunk has no document to check under a filter (fail closed).
		visible := true
		switch {
		case row.documentID != "":
			visible = !active || matchesTagFilter(row.docTags, req.Tags, req.TagsMatch, req.TagGroups)
		case row.chunkID != "" && active:
			visible = false
		}
		if !visible {
			out = append(out, citation)
			continue
		}
		switch req.Depth {
		case "chunk":
			if row.chunkText != "" {
				citation.Text = row.chunkText
			}
		default: // document, and an unset depth which the expand tool requires
			if row.docText != "" {
				citation.Text = row.docText
			}
		}
		citation.DocumentID = row.documentID
		citation.ChunkID = row.chunkID
		out = append(out, citation)
	}
	return out, nil
}

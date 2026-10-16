package storage

import "context"

// Source-text visibility (#5030). A fact can reach a reader through its own
// tags — a label-derived tag such as `kind:rule` shares it — while the document
// it was extracted from carries only the writer's tags. Source text therefore
// follows its DOCUMENT's tags, not the fact's: returning a chunk or a document
// because the fact matched would hand the reader the rest of a document their
// filter excludes. Every read that returns source text for memories found by
// tag goes through these helpers, so the rule lives in one place.

// TagsVisibleUnderScope reports whether a row's tags satisfy the caller's
// resolved read scope, expressed as a compound tag-group list (the top level is
// an AND). An empty scope leaves every row visible. It is the read-gate a
// by-id handler applies to confirm a caller may see the row at all before
// answering, so an out-of-scope id reads as not-found.
func TagsVisibleUnderScope(tags []string, scope []TagGroup) bool {
	if len(scope) == 0 {
		return true
	}
	return matchesTagFilter(tags, nil, "", scope)
}

// visibleDocumentIDs returns the subset of documentIDs whose document tags pass
// the reader's filter. With no active filter every named id is visible without
// a read. It fails closed: a document that cannot be found (deleted mid-read,
// or in another bank) is not visible, and neither is an empty id (a chunk with
// no document).
func (s *PostgresStore) visibleDocumentIDs(ctx context.Context, bankID string, documentIDs []string, tags []string, mode string, groups []TagGroup) (map[string]bool, error) {
	wanted := make([]string, 0, len(documentIDs))
	seen := map[string]bool{}
	for _, id := range documentIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		wanted = append(wanted, id)
	}
	visible := make(map[string]bool, len(wanted))
	if len(wanted) == 0 {
		return visible, nil
	}
	if !tagFilterActive(tags, mode, groups) {
		for _, id := range wanted {
			visible[id] = true
		}
		return visible, nil
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT id, COALESCE(tags, '{}') FROM `+quoteIdent(s.schema)+`.documents
		 WHERE bank_id = $1 AND id = ANY($2::text[])`, bankID, wanted)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var tagsRaw []byte
		if err := rows.Scan(&id, &tagsRaw); err != nil {
			return nil, err
		}
		if matchesTagFilter(decodePostgresTextArray(tagsRaw), tags, mode, groups) {
			visible[id] = true
		}
	}
	return visible, rows.Err()
}

// visibleDocumentIDsLocked is the in-process twin of the Postgres helper. The
// caller holds the store lock.
func (s *MemoryStore) visibleDocumentIDsLocked(bankID string, documentIDs []string, tags []string, mode string, groups []TagGroup) map[string]bool {
	visible := make(map[string]bool, len(documentIDs))
	active := tagFilterActive(tags, mode, groups)
	for _, id := range documentIDs {
		if id == "" || visible[id] {
			continue
		}
		if !active {
			visible[id] = true
			continue
		}
		doc, ok := s.documents[id]
		if !ok || doc.BankID != bankID {
			continue
		}
		if matchesTagFilter(doc.Tags, tags, mode, groups) {
			visible[id] = true
		}
	}
	return visible
}

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RetainWrite persists an ordered-item memory write against PostgreSQL. It
// lazily creates the bank, honors operation-id idempotency, segments content
// into chunks, persists units, resolves entities, and returns created unit
// identifiers grouped per item in item order.
func (s *PostgresStore) RetainWrite(ctx context.Context, req RetainRequest) (RetainOutcome, error) {
	if _, err := s.GetBank(ctx, req.BankID); err != nil {
		if !errors.Is(err, ErrBankNotFound) {
			return RetainOutcome{}, err
		}
		// Callers address a bank by name; declaring it first is not part of
		// the contract, so a first write creates it. Every bank-scoped write
		// below has the bank row as its foreign-key target, and without it the
		// first one fails with a raw constraint violation.
		if _, err := s.ensureBankExists(ctx, req.BankID); err != nil {
			return RetainOutcome{}, err
		}
	}
	// Extraction calls a model, so it runs before any write begins: a slow
	// model call must not hold a database transaction open.
	extracted, err := extractForItems(ctx, req)
	if err != nil {
		return RetainOutcome{}, err
	}
	opID := req.OperationID
	if opID == "" {
		opID = newUUID()
	}
	now := time.Now().UTC()
	grouped := make([][]string, 0, len(req.Items))
	for index, item := range req.Items {
		units, err := s.writeItemWithAppendRetry(ctx, req, item, extracted[index], now)
		if err != nil {
			return RetainOutcome{}, err
		}
		grouped = append(grouped, units)
	}
	usage, processedTokens := RetainAccounting(req.Items)
	outcome := RetainOutcome{
		ItemUnitIDs:            grouped,
		Usage:                  usage,
		OperationID:            opID,
		OperationIDs:           []string{opID},
		ProcessedContentTokens: processedTokens,
	}
	status := StatusCompleted
	var completedAt *time.Time
	if req.Async {
		status = StatusPending
	} else {
		completedAt = &now
	}
	if req.SkipOperationRecord {
		// The queued write's worker already owns the operation record, so this
		// write must not create a second one for itself.
		return outcome, nil
	}
	if _, err := s.PutOperation(ctx, Operation{
		OperationID: opID, BankID: req.BankID, OperationType: OperationTypeRetain,
		Status: status, Run: 1, CreatedAt: now, UpdatedAt: now, CompletedAt: completedAt,
	}); err != nil {
		return RetainOutcome{}, err
	}
	return outcome, nil
}

// appendConflictAttempts bounds how many times an append re-reads the document
// and redoes itself after losing the race to a concurrent append. Contention
// this deep means the queue-level serialization is not doing its job, or the
// caller is racing itself on the synchronous path; failing then is better than
// looping, because the operation-level retry re-runs the whole submission.
const appendConflictAttempts = 3

// writeItemWithAppendRetry writes one item, redoing an append that lost its
// race. An append is a read-modify-write: it reads the stored document, joins
// the new text onto it and writes the result back. A concurrent append
// committing in between would make this write overwrite a turn it never saw, so
// the write is a compare-and-set on the stored content hash and a lost race
// costs a redo rather than the caller's content. Non-append items cannot raise
// the conflict, so they run exactly once.
func (s *PostgresStore) writeItemWithAppendRetry(ctx context.Context, req RetainRequest, item RetainItem, facts []ExtractedFact, now time.Time) ([]string, error) {
	for attempt := 1; ; attempt++ {
		units, err := s.writeItem(ctx, req, item, facts, now)
		if !errors.Is(err, ErrDocumentConflict) || item.UpdateMode != "append" || attempt >= appendConflictAttempts {
			return units, err
		}
		// The delay grows with the attempt so simultaneous losers do not line
		// up and collide again, and it is short: the writer that won is
		// normally finished within milliseconds.
		time.Sleep(time.Duration(attempt) * 20 * time.Millisecond)
	}
}

// writeItem persists one item's document, chunks, units, entity links, and the
// links between the units the item produced.
func (s *PostgresStore) writeItem(ctx context.Context, req RetainRequest, item RetainItem, facts []ExtractedFact, now time.Time) ([]string, error) {
	docID := item.DocumentID
	if docID == "" {
		docID = newID()
	}
	storeText := !req.DiscardDocumentText
	sizes := segmentOptionsFor(req)
	originalText := item.Content
	baseIndex := 0
	var existingHashes map[string]bool
	// expectHash, when set, makes the document write a compare-and-set on the
	// stored content hash: the append read this document, and a write that
	// followed another append must not silently overwrite the turn that landed
	// in between.
	expectHash := ""
	// pruneCandidates are the entities the replaced text named; each is dropped
	// after the write if nothing mentions it any more.
	var pruneCandidates []string
	// A re-retain against an explicit document id honours the update mode.
	if item.DocumentID != "" {
		// Delta retain: an unchanged document is not re-extracted.
		if !item.ForceReextract && item.UpdateMode != "replace" && item.UpdateMode != "append" {
			if existing, err := s.GetDocument(ctx, req.BankID, item.DocumentID); err == nil &&
				existing.ContentHash == ContentHash(item.Content) {
				return s.unitIDsForDocument(ctx, req.BankID, item.DocumentID)
			}
		}
		// The default mode is replace: re-retaining a document id with new
		// content supersedes what the document said, so its units, chunks and
		// links are discarded before the new evidence is written. "append" is
		// the only mode that keeps what is already stored.
		switch item.UpdateMode {
		case "append":
			existing, err := s.GetDocument(ctx, req.BankID, docID)
			if err == nil && existing.OriginalText != "" {
				// The stored body is the prefix; the incoming content is the new
				// tail, joined the one way a document's parts are joined (a JSON
				// conversation array merges, anything else is newline-joined).
				originalText = joinDocumentParts(existing.OriginalText, item.Content)
				hashes, count, err := s.documentChunkHashes(ctx, req.BankID, docID)
				if err != nil {
					return nil, err
				}
				// The new tail's chunks continue past the chunks already stored,
				// and a chunk the document already holds is not stored again — so
				// a re-sent turn adds no chunk and no fact.
				baseIndex = count
				existingHashes = hashes
				expectHash = existing.ContentHash
			}
		default:
			// The entities the document's units mention now become prune
			// candidates: an entity only the replaced text named has no mention
			// left once the new evidence is written, so it is dropped — but only
			// after the write, because an entity the new text still names must
			// survive as the same identity.
			stale, err := s.documentEntityIDs(ctx, req.BankID, docID)
			if err != nil {
				return nil, err
			}
			if err := s.clearDocumentUnits(ctx, req.BankID, docID); err != nil {
				return nil, err
			}
			pruneCandidates = stale
		}
	}
	tags := mergeTags(item.Tags, req.DocumentTags)
	document := Document{
		OriginalText: originalText,
		ContentHash:  ContentHash(originalText),
		Metadata:     item.Metadata,
		RetainParams: retainParamsFor(item),
		Tags:         tags,
	}
	if _, err := s.writeDocument(ctx, req.BankID, docID, document, !req.DiscardDocumentText, expectHash); err != nil {
		return nil, err
	}
	// An attachment is derived from the document's canonical text and nothing
	// else, so a re-retain that drops an image retires it here.
	if err := s.SyncDocumentAttachments(ctx, req.BankID, docID, item.Content); err != nil {
		return nil, err
	}
	chunkBySegment, err := s.putItemChunks(ctx, req.BankID, docID, item.Content, storeText, sizes, baseIndex, existingHashes)
	if err != nil {
		return nil, err
	}
	contents := itemContents(req.BankID, docID, chunkBySegment, item, facts, tags, sizes, modelExtractionEnabled(req))
	if err := embedContents(ctx, req.Embeddings, contents); err != nil {
		return nil, err
	}
	units := make([]string, 0, len(contents))
	for _, content := range contents {
		stored, err := s.UpsertUnit(ctx, content.unit)
		if err != nil {
			return nil, err
		}
		// The entities this unit mentions, de-duplicated by identity, so the
		// co-occurrence cross-product below counts each pair once per unit.
		mentioned := make([]string, 0, len(content.entities))
		seen := map[string]bool{}
		for _, entity := range content.entities {
			resolved, err := s.ResolveRetainEntity(ctx, req.BankID, entity, item.ResolveEntities)
			if err != nil {
				return nil, err
			}
			// Intake drops a blank or over-long name, which resolves to no
			// entity; the fact keeps its other entities and stays written.
			if resolved.ID == "" {
				continue
			}
			if err := s.UpsertUnitEntity(ctx, stored.ID, resolved.ID); err != nil {
				return nil, err
			}
			if !seen[resolved.ID] {
				seen[resolved.ID] = true
				mentioned = append(mentioned, resolved.ID)
			}
		}
		if err := s.recordUnitCooccurrences(ctx, req.BankID, mentioned); err != nil {
			return nil, err
		}
		units = append(units, stored.ID)
	}
	if err := s.linkItemUnits(ctx, req.BankID, units, contents); err != nil {
		return nil, err
	}
	if err := s.createSemanticLinks(ctx, req.BankID, units); err != nil {
		return nil, err
	}
	if len(pruneCandidates) > 0 {
		if err := s.pruneEntitiesIfOrphaned(ctx, req.BankID, pruneCandidates); err != nil {
			return nil, err
		}
	}
	return units, nil
}

// documentEntityIDs returns the distinct entities the document's stored units
// mention, the candidate set a replace re-checks for orphanhood.
func (s *PostgresStore) documentEntityIDs(ctx context.Context, bankID, docID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT ue.entity_id::text
		 FROM `+quoteIdent(s.schema)+`.unit_entities ue
		 JOIN `+quoteIdent(s.schema)+`.memory_units mu ON mu.id = ue.unit_id
		 WHERE mu.bank_id = $1 AND mu.document_id = $2`, bankID, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// pruneEntitiesIfOrphaned deletes the named entities that no surviving unit
// mentions. The co-occurrence rows that named them cascade away with them.
func (s *PostgresStore) pruneEntitiesIfOrphaned(ctx context.Context, bankID string, entityIDs []string) error {
	if len(entityIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.entities e
		 WHERE e.bank_id = $1 AND e.id = ANY($2::uuid[])
		   AND NOT EXISTS (SELECT 1 FROM `+qualified+`.unit_entities ue WHERE ue.entity_id = e.id)`,
		bankID, entityIDs)
	return err
}

// createSemanticLinks links each new unit to existing units whose embedding is
// within the similarity floor, using the pgvector distance operator.
func (s *PostgresStore) createSemanticLinks(ctx context.Context, bankID string, unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
		 SELECT u1.bank_id, u1.id, u2.id, 'semantic', 1 - (u1.embedding <=> u2.embedding)
		 FROM `+qualified+`.memory_units u1
		 JOIN `+qualified+`.memory_units u2
		   ON u2.bank_id = u1.bank_id AND u2.id <> u1.id AND u2.embedding IS NOT NULL
		 WHERE u1.bank_id = $1 AND u1.id = ANY($2::uuid[]) AND u1.embedding IS NOT NULL
		   AND 1 - (u1.embedding <=> u2.embedding) >= $3
		 ON CONFLICT DO NOTHING`,
		bankID, unitIDs, s.tuning.SemanticLinkMinSimilarity)
	return err
}

// unitIDsForDocument returns a document's stored unit ids in a deterministic
// order, for a delta-skipped retain.
func (s *PostgresStore) unitIDsForDocument(ctx context.Context, bankID, docID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id::text FROM `+quoteIdent(s.schema)+`.memory_units
		 WHERE bank_id = $1 AND document_id = $2 ORDER BY id ASC`, bankID, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// clearDocumentUnits removes a document's units, chunks and links while keeping
// the document row, so a replace-mode re-retain rebuilds its evidence.
func (s *PostgresStore) clearDocumentUnits(ctx context.Context, bankID, docID string) error {
	qualified := quoteIdent(s.schema)
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.memory_links
		 WHERE from_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)
		    OR to_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		`DELETE FROM ` + qualified + `.unit_entities
		 WHERE unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		`DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2`,
		`DELETE FROM ` + qualified + `.chunks WHERE bank_id = $1 AND document_id = $2`,
	} {
		if _, err := s.db.ExecContext(ctx, statement, bankID, docID); err != nil {
			return err
		}
	}
	return nil
}

// putItemChunks stores one item's segments, starting at baseIndex so an append
// continues a document's chunk numbering instead of colliding with it. When
// storeText is false the raw chunk text is not kept, but the content hash is
// still computed from it.
//
// A segment whose content hash is already in existing is skipped — an append
// whose tail the document already holds stores nothing new — and the returned
// map omits it, so no unit is built from it. The map is keyed by the segment's
// position in the item.
func (s *PostgresStore) putItemChunks(ctx context.Context, bankID, docID, content string, storeText bool, sizes SegmentOptions, baseIndex int, existing map[string]bool) (map[int]string, error) {
	chunks := SegmentWith(content, sizes)
	bySegment := make(map[int]string, len(chunks))
	next := baseIndex
	for i, chunk := range chunks {
		if existing[ContentHash(chunk)] {
			continue
		}
		text := chunk
		if !storeText {
			text = ""
		}
		// The index is claimed a statement at a time, so a chunk another writer
		// stored between this item's read and its write does not make this one
		// fail: a taken index is stepped over, and the chunk lands at the next
		// free one. Losing the caller's content to a bookkeeping collision would
		// be the wrong trade, and the indices only ever grow.
		var chunkID string
		for attempt := 0; ; attempt++ {
			chunkID = BuildChunkID(bankID, docID, next)
			_, err := s.db.ExecContext(ctx,
				`INSERT INTO `+quoteIdent(s.schema)+`.chunks (chunk_id, bank_id, document_id, chunk_index, chunk_text, content_hash)
				 VALUES ($1,$2,$3,$4,$5,$6)`,
				chunkID, bankID, docID, next, text, ContentHash(chunk))
			if err == nil {
				break
			}
			if !isUniqueViolation(err) || attempt >= chunkIndexAttempts {
				return nil, err
			}
			next++
		}
		next++
		bySegment[i] = chunkID
	}
	return bySegment, nil
}

// chunkIndexAttempts bounds how far one chunk steps past a taken index before
// giving up, so a table that is somehow full of the same document cannot spin.
const chunkIndexAttempts = 50

// documentChunkHashes returns the content hashes of a document's stored chunks
// and how many it holds, so an append can skip a tail it already stored and
// number its new chunks past the ones present.
func (s *PostgresStore) documentChunkHashes(ctx context.Context, bankID, docID string) (map[string]bool, int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(content_hash,'') FROM `+quoteIdent(s.schema)+`.chunks
		 WHERE bank_id = $1 AND document_id = $2`, bankID, docID)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	hashes := map[string]bool{}
	count := 0
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, 0, err
		}
		hashes[hash] = true
		count++
	}
	return hashes, count, rows.Err()
}

// linkItemUnits records the links one item's units take part in: the semantic
// links between consecutive units and the caused_by links extraction declared.
func (s *PostgresStore) linkItemUnits(ctx context.Context, bankID string, unitIDs []string, contents []itemContent) error {
	links := append(linkTuplesFor(bankID, unitIDs), causalLinksFor(bankID, unitIDs, contents)...)
	for _, link := range links {
		if err := s.UpsertLink(ctx, link); err != nil {
			return err
		}
	}
	return nil
}

// GetDocument returns one stored document by identifier.
func (s *PostgresStore) GetDocument(ctx context.Context, bankID, docID string) (Document, error) {
	query := `SELECT id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''), created_at, updated_at,
	                 COALESCE(retain_params, '{}'::jsonb), COALESCE(tags, '{}')
	          FROM ` + quoteIdent(s.schema) + `.documents WHERE id=$1 AND bank_id=$2`
	var doc Document
	var paramsRaw, tagsRaw []byte
	err := s.db.QueryRowContext(ctx, query, docID, bankID).Scan(&doc.ID, &doc.BankID, &doc.OriginalText,
		&doc.ContentHash, &doc.CreatedAt, &doc.UpdatedAt, &paramsRaw, &tagsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrDocumentNotFound
	}
	if err != nil {
		return Document{}, err
	}
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &doc.RetainParams); err != nil {
			return Document{}, err
		}
	}
	// tags is a text[] column, not jsonb: its literal form is an array, which
	// json.Unmarshal would read as an object.
	doc.Tags = decodePostgresTextArray(tagsRaw)
	return doc, nil
}

// PutDocument stores a document's text, hash, metadata and parameters.
func (s *PostgresStore) PutDocument(ctx context.Context, bankID, docID string, doc Document) (Document, error) {
	return s.writeDocument(ctx, bankID, docID, doc, true, "")
}

// writeDocument stores a document's text, hash, parameters and tags. With
// storeText false the text column is written as NULL — the store-text opt-out
// keeps the body out of the row while content_hash is still computed from it,
// so delta retention is unaffected. With expectHash set it is a compare-and-set
// on that document: the write is applied only while the stored row still holds
// that content hash, and otherwise reports ErrDocumentConflict having written
// nothing. That is what makes an append's read-modify-write safe against a
// concurrent one, which without it silently erases the other's turn. An empty
// expectHash writes unconditionally.
func (s *PostgresStore) writeDocument(ctx context.Context, bankID, docID string, doc Document, storeText bool, expectHash string) (Document, error) {
	if doc.ContentHash == "" {
		doc.ContentHash = ContentHash(doc.OriginalText)
	}
	// A NULL original_text is what the reference stores when document text
	// storage is off; the hash above still comes from the real text.
	var originalText any
	if storeText {
		originalText = doc.OriginalText
	}
	// The document carries no metadata column in the reference: the value is
	// sourced from retain_params.metadata, so it is merged there before the
	// write rather than stored beside it.
	params := doc.RetainParams
	if params == nil {
		params = map[string]any{}
	}
	if len(doc.Metadata) > 0 {
		params = copyAnyMap(params)
		params["metadata"] = doc.Metadata
	}
	retainParams, err := jsonColumn(params)
	if err != nil {
		return Document{}, err
	}
	tags := normalizeTags(doc.Tags)
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.documents
	          (id, bank_id, original_text, content_hash, retain_params, tags, created_at, updated_at)
	          VALUES ($1,$2,$3,$4,$5::jsonb,$6, now(), now())
	          ON CONFLICT (id, bank_id) DO UPDATE SET
	              original_text = $3, content_hash = $4,
	              retain_params = $5::jsonb, tags = $6, updated_at = now()
	          RETURNING id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
	                    COALESCE(retain_params, '{}'::jsonb),
	                    COALESCE(tags, '{}'), created_at, updated_at`
	args := []any{docID, bankID, originalText, doc.ContentHash, retainParams, tags}
	if expectHash != "" {
		query = `UPDATE ` + quoteIdent(s.schema) + `.documents
		         SET original_text = $3, content_hash = $4,
		             retain_params = $5::jsonb, tags = $6, updated_at = now()
		         WHERE id = $1 AND bank_id = $2 AND COALESCE(content_hash, '') = $7
		         RETURNING id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
		                   COALESCE(retain_params, '{}'::jsonb),
		                   COALESCE(tags, '{}'), created_at, updated_at`
		args = append(args, expectHash)
	}
	var out Document
	var retainRaw, tagsRaw []byte
	err = s.db.QueryRowContext(ctx, query, args...).
		Scan(&out.ID, &out.BankID, &out.OriginalText, &out.ContentHash, &retainRaw, &tagsRaw, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Only the conditional form can match nothing: the row moved under the
		// write, so nothing was applied and the caller re-reads.
		return Document{}, ErrDocumentConflict
	}
	if err != nil {
		return Document{}, err
	}
	out.RetainParams = decodeJSONMap(retainRaw)
	out.Metadata = documentMetadata(out.RetainParams)
	out.Tags = decodePostgresTextArray(tagsRaw)
	return out, nil
}

// AppendDocument concatenates text onto the stored document, failing with
// ErrDocumentConflict when the stored hash no longer matches expectedHash.
//
// The read and the write are one compare-and-set rather than a read followed by
// an unconditional write: a caller that read the document and computed its new
// body from it must not overwrite whatever landed in between.
func (s *PostgresStore) AppendDocument(ctx context.Context, bankID, docID, text, expectedHash string) (Document, error) {
	current, err := s.GetDocument(ctx, bankID, docID)
	if err != nil && !errors.Is(err, ErrDocumentNotFound) {
		return Document{}, err
	}
	if err == nil && expectedHash != "" && current.ContentHash != expectedHash {
		return Document{}, ErrDocumentConflict
	}
	combined := current.OriginalText + text
	return s.writeDocument(ctx, bankID, docID, Document{OriginalText: combined}, true, expectedHash)
}

// entityColumns is the column list every entity read selects, so a scan can
// never drift from the query that produced it.
const entityColumns = `id, bank_id, canonical_name, COALESCE(entity_kind,'regular'), COALESCE(mention_count,0)`

// ResolveEntity resolves an entity name against the bank, creating it with the
// given kind when nothing matches.
func (s *PostgresStore) ResolveEntity(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error) {
	return s.resolveEntityWithKind(ctx, bankID, entityMatchKey(name), kind, resolve)
}

// ResolveRetainEntity resolves an entity supplied on a retain item, honoring
// the item's resolution switch. The entity's label groups the mention during
// resolution and is not stored on the row.
func (s *PostgresStore) ResolveRetainEntity(ctx context.Context, bankID string, entity RetainEntity, resolve bool) (Entity, error) {
	return s.resolveEntityWithKind(ctx, bankID, entityMatchKey(entity.Text), EntityKindRegular, resolve)
}

// resolveEntityWithKind finds or creates the entity named by a normalized name.
//
// Resolution on treats the name as a guess: an existing entity whose name
// scores at or above the similarity threshold is reused, with the trigram
// similarity ordered by score so the closest match wins. Resolution off takes
// the name literally and reuses an entity only on a case-insensitive exact
// match.
func (s *PostgresStore) resolveEntityWithKind(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error) {
	if name == "" {
		return Entity{}, nil
	}
	if kind == "" {
		kind = EntityKindRegular
	}
	if existing, found, err := s.findEntity(ctx, bankID, name, resolve); err != nil {
		return Entity{}, err
	} else if found {
		return existing, nil
	}
	entity := Entity{ID: newID(), BankID: bankID, CanonicalName: name, EntityKind: kind, MentionCount: 0}
	// mention_count starts at zero: the mention that links this entity to its
	// first unit is what bumps it, through UpsertUnitEntity, so the count is the
	// number of mentions rather than the number of entities.
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.entities (id, bank_id, canonical_name, entity_kind, mention_count, first_seen, last_seen)
	           VALUES ($1,$2,$3,$4,0,now(),now())
	           ON CONFLICT (id) DO NOTHING`
	if _, err := s.db.ExecContext(ctx, insert, entity.ID, bankID, name, string(kind)); err != nil {
		// The read above and this insert are two statements: a concurrent writer
		// can create the same entity — the table holds one row per bank and
		// lower-cased name — between them. Losing that race is not a failure of
		// this write, so the winner's row is read back and used instead.
		if isUniqueViolation(err) {
			if existing, found, findErr := s.findEntity(ctx, bankID, name, resolve); findErr != nil {
				return Entity{}, findErr
			} else if found {
				return existing, nil
			}
		}
		return Entity{}, err
	}
	return entity, nil
}

// findEntity locates an existing entity for a name, either exactly or by
// similarity depending on the resolution switch.
func (s *PostgresStore) findEntity(ctx context.Context, bankID, name string, resolve bool) (Entity, bool, error) {
	table := quoteIdent(s.schema) + `.entities`
	if !resolve {
		query := `SELECT ` + entityColumns + ` FROM ` + table +
			` WHERE bank_id=$1 AND lower(canonical_name)=lower($2) LIMIT 1`
		return s.scanEntity(ctx, query, bankID, name)
	}
	// The trigram probe narrows the pool; the similarity score then decides the
	// merge. The two are separate stages on purpose, because trigram ranks
	// names differently from the score the threshold is expressed in, so a
	// candidate that passes the pool filter may still be rejected.
	candidates, err := s.entityCandidates(ctx, bankID, name)
	if err != nil {
		// The trigram extension may be unavailable. Falling back to an exact
		// match keeps the write working rather than failing it, because
		// failing to fuzzy-resolve is far less costly than refusing a memory.
		fallback := `SELECT ` + entityColumns + ` FROM ` + table +
			` WHERE bank_id=$1 AND lower(canonical_name)=lower($2) LIMIT 1`
		return s.scanEntity(ctx, fallback, bankID, name)
	}
	return bestEntityCandidate(candidates, name)
}

// entityCandidates returns the stored entities whose names are close enough to
// the supplied name to be worth scoring.
func (s *PostgresStore) entityCandidates(ctx context.Context, bankID, name string) ([]Entity, error) {
	limit := s.tuning.EntityResolutionCandidates
	if limit <= 0 {
		limit = DefaultEntityResolutionCandidates
	}
	floor := s.tuning.EntityCandidateSimilarity
	if floor <= 0 {
		floor = EntityCandidateSimilarity
	}
	query := `SELECT ` + entityColumns + ` FROM ` + quoteIdent(s.schema) + `.entities
	          WHERE bank_id=$1 AND similarity(lower(canonical_name), lower($2)) >= $3
	          ORDER BY similarity(lower(canonical_name), lower($2)) DESC, id ASC
	          LIMIT $4`
	rows, err := s.db.QueryContext(ctx, query, bankID, name, floor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	candidates := make([]Entity, 0)
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(&entity.ID, &entity.BankID, &entity.CanonicalName, &entity.EntityKind, &entity.MentionCount); err != nil {
			return nil, err
		}
		candidates = append(candidates, entity)
	}
	return candidates, rows.Err()
}

// bestEntityCandidate picks the candidate the similarity score accepts,
// preferring the highest score and breaking ties on the identifier so the
// resolution is deterministic.
func bestEntityCandidate(candidates []Entity, name string) (Entity, bool, error) {
	best := Entity{}
	bestScore := 0.0
	for _, candidate := range candidates {
		score := entitySimilarity(candidate.CanonicalName, name)
		if score < EntitySimilarityThreshold {
			continue
		}
		if score > bestScore || (score == bestScore && best.ID != "" && candidate.ID < best.ID) {
			best = candidate
			bestScore = score
		}
	}
	return best, best.ID != "", nil
}

// scanEntity runs an entity query and reads at most one row. A missing row is
// reported as not-found rather than as an error, because creating the entity
// is the next step.
func (s *PostgresStore) scanEntity(ctx context.Context, query string, args ...any) (Entity, bool, error) {
	var entity Entity
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&entity.ID, &entity.BankID, &entity.CanonicalName, &entity.EntityKind, &entity.MentionCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, false, nil
	}
	if err != nil {
		return Entity{}, false, err
	}
	return entity, true, nil
}

// UpsertUnit persists one memory unit.
func (s *PostgresStore) UpsertUnit(ctx context.Context, unit MemoryUnit) (MemoryUnit, error) {
	if unit.ID == "" {
		unit.ID = newID()
	}
	// An embedding is validated before it is stored: a vector of the wrong
	// width would rank against every row incorrectly rather than fail loudly.
	embedding := any(nil)
	if len(unit.Embedding) > 0 {
		if err := ValidateEmbedding(unit.Embedding); err != nil {
			return MemoryUnit{}, err
		}
		embedding = NullableEmbedding(unit.Embedding)
	}
	// Metadata is serialized rather than defaulted: the memory backend keeps the
	// caller's map, so a hard-coded '{}' here would lose it on every Postgres
	// retain and make the two backends disagree.
	metadata, err := jsonColumn(unit.Metadata)
	if err != nil {
		return MemoryUnit{}, err
	}
	// Every column the memory backend stores is named here, so a unit
	// round-trips identically on both backends. proof_count is deliberately
	// absent: retain never sets it, the column defaults it, and the reference's
	// insert omits it too. search_vector stays the generated expression because
	// it must track text, which is parameter $3.
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.memory_units
	           (id, bank_id, text, search_vector, context, fact_type, metadata, event_date, occurred_start,
	            occurred_end, mentioned_at, document_id, chunk_id, tags, embedding,
	            source_memory_ids, observation_scopes, consolidated_at, consolidation_failed_at, attachment_ids,
	            text_signals)
	           VALUES ($1,$2,$3,` + searchVectorExpr("$3", "$4", "$20") + `,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14::vector,
	                   $15::uuid[],$16::jsonb,$17,$18,$19,$20)
	           ON CONFLICT (id) DO UPDATE SET embedding = EXCLUDED.embedding
	           WHERE ` + quoteIdent(s.schema) + `.memory_units.embedding IS NULL`
	// A nil tag slice would violate the column's not-null constraint, so an
	// absent tag set is stored as an empty array.
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
	}
	attachmentIDs := unit.AttachmentIDs
	if attachmentIDs == nil {
		attachmentIDs = []string{}
	}
	sourceMemoryIDs := unit.SourceMemoryIDs
	if sourceMemoryIDs == nil {
		sourceMemoryIDs = []string{}
	}
	scopes := any(nil)
	if len(unit.ObservationScopes) > 0 {
		scopes = string(unit.ObservationScopes)
	}
	if _, err := s.db.ExecContext(ctx, insert, unit.ID, unit.BankID, unit.Text, unit.Context,
		string(unit.FactType), metadata, unit.EventDate, unit.OccurredStart, unit.OccurredEnd,
		unit.MentionedAt, nullString(unit.DocumentID), nullString(unit.ChunkID), tags, embedding,
		sourceMemoryIDs, scopes, unit.ConsolidatedAt, unit.ConsolidationFailedAt, attachmentIDs,
		nullString(unit.TextSignals)); err != nil {
		return MemoryUnit{}, err
	}
	return unit, nil
}

// UpsertUnitEntity links a memory unit to an entity identity.
func (s *PostgresStore) UpsertUnitEntity(ctx context.Context, unitID, entityID string) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO `+quoteIdent(s.schema)+`.unit_entities (unit_id, entity_id)
	          VALUES ($1,$2) ON CONFLICT DO NOTHING`, unitID, entityID)
	if err != nil {
		return err
	}
	// The mention count and last-seen stamp are per mention, so they move only
	// when a new link was actually created; a re-link of the same pair is not a
	// second mention.
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE `+quoteIdent(s.schema)+`.entities
	          SET mention_count = mention_count + 1, last_seen = GREATEST(last_seen, now())
	          WHERE id = $1::uuid`, entityID)
	return err
}

// SetUnitEntities replaces the entities one memory unit mentions.
//
// The names are resolved first so a name that creates an entity is not left
// half-applied when another name fails; then the links that are no longer named
// are dropped and the new ones are added. An entity that stays in the set keeps
// its existing link, because write-then-delete would count the same mention
// twice in the mention count that reads project.
func (s *PostgresStore) SetUnitEntities(ctx context.Context, bankID, unitID string, names []string, resolve bool) error {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		entity, err := s.ResolveEntity(ctx, bankID, trimmed, EntityKindRegular, resolve)
		if err != nil {
			return err
		}
		if entity.ID == "" {
			continue
		}
		wanted[entity.ID] = struct{}{}
	}

	qualified := quoteIdent(s.schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id::text FROM `+qualified+`.unit_entities WHERE unit_id = $1::uuid`, unitID)
	if err != nil {
		return err
	}
	linked := make([]string, 0, len(wanted))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		linked = append(linked, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, id := range linked {
		if _, keep := wanted[id]; keep {
			delete(wanted, id)
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM `+qualified+`.unit_entities WHERE unit_id = $1::uuid AND entity_id = $2::uuid`,
			unitID, id); err != nil {
			return err
		}
	}
	for id := range wanted {
		if err := s.UpsertUnitEntity(ctx, unitID, id); err != nil {
			return err
		}
	}
	return nil
}

// UpsertEntityCooccurrence records that two entities co-occur in a bank.
//
// The reference keys the pair by identifier with a check that the lower one
// comes first, so the row is not addressed by bank at all. The endpoints are
// ordered here, so no caller has to know which of the two is smaller.
func (s *PostgresStore) UpsertEntityCooccurrence(ctx context.Context, bankID, entityID1, entityID2 string) error {
	if entityID1 > entityID2 {
		entityID1, entityID2 = entityID2, entityID1
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.entity_cooccurrences (entity_id_1, entity_id_2, cooccurrence_count)
	          VALUES ($1,$2,1)
	          ON CONFLICT (entity_id_1, entity_id_2)
	          DO UPDATE SET cooccurrence_count = entity_cooccurrences.cooccurrence_count + 1,
	                        last_cooccurred = now()`
	_, err := s.db.ExecContext(ctx, query, entityID1, entityID2)
	return err
}

// recordUnitCooccurrences records one co-occurrence for every unordered pair of
// the entities a single memory unit mentions, which is the shape the entity
// graph reads and the resolution weighting counts. Pairing is per unit, so two
// entities mentioned by two different facts are not neighbours.
//
// It is idempotent per unit: the caller passes the unit's entities already
// de-duplicated by identity, so a fact that names the same entity twice counts
// the pair once.
func (s *PostgresStore) recordUnitCooccurrences(ctx context.Context, bankID string, entityIDs []string) error {
	for i := range len(entityIDs) {
		for j := i + 1; j < len(entityIDs); j++ {
			if err := s.UpsertEntityCooccurrence(ctx, bankID, entityIDs[i], entityIDs[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpsertLink persists one directed memory link.
//
// The reference identifies a link by its endpoints, type and entity, with a
// null entity folded to the nil uuid so the uniqueness spans the null case.
// The conflict target is the unique index's expression, which is what makes
// the insert apply to an existing row rather than duplicate it.
func (s *PostgresStore) UpsertLink(ctx context.Context, link MemoryLink) error {
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.memory_links (bank_id, from_unit_id, to_unit_id, link_type, entity_id, weight)
	          VALUES ($1,$2,$3,$4,$5,$6)
	          ON CONFLICT (from_unit_id, to_unit_id, link_type, COALESCE(entity_id, '00000000-0000-0000-0000-000000000000'::uuid))
	          DO UPDATE SET weight = EXCLUDED.weight`
	_, err := s.db.ExecContext(ctx, query, link.BankID, link.FromUnitID, link.ToUnitID,
		string(link.LinkType), nullString(link.EntityID), link.Weight)
	return err
}

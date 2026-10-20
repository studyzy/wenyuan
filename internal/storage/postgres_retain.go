package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RetainWrite persists an ordered-item memory write against PostgreSQL. It
// lazily creates the bank, honors operation-id idempotency, segments content
// into chunks, persists units, resolves entities, and returns created unit
// identifiers grouped per item in item order.
func (s *PostgresStore) RetainWrite(ctx context.Context, req RetainRequest) (RetainOutcome, error) {
	ctx, phases := beginRetainPhases(ctx, postgresStorePhaseLabel)
	defer phases.finish()
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
	extracted, err := extractForItems(ctx, req, s.tuning.ExtractCausalLinks)
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
		// Checkpoint: a queued write aborts between items if its operation was
		// cancelled or its bank (and so its operation row, by the
		// async_operations cascade) was deleted, mirroring the reference's
		// per-document check. A failed liveness read is treated as alive, so a
		// transient database error does not abort work that is still valid.
		if req.SkipOperationRecord && req.OperationID != "" {
			if alive, err := s.OperationAlive(ctx, req.BankID, req.OperationID); err == nil && !alive {
				return RetainOutcome{}, ErrOperationAborted
			}
		}
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
	// The item's mentions are clustered once, before any is scored, so two
	// spellings of one name that appear in this same document become one entity
	// rather than two.
	canonicalNames := intrabatchCanonicalNames(itemEntityMentions(contents), item.ResolveEntities, s.intrabatchMergeSimilarity())
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
			resolved, err := s.ResolveRetainEntity(ctx, req.BankID, applyIntrabatchCanonical(entity, canonicalNames), item.ResolveEntities)
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
		if err := s.UpsertUnitCooccurrences(ctx, req.BankID, mentioned, UnitSeenAt(stored)); err != nil {
			return nil, err
		}
		units = append(units, stored.ID)
	}
	if err := s.linkItemUnits(ctx, req.BankID, units, contents); err != nil {
		return nil, err
	}
	if err := s.createTemporalLinks(ctx, req.BankID, units); err != nil {
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
	// The document's units are about to go with their postings, so give back
	// the mention counts the postings contributed before they are deleted.
	if err := releaseEntityPostings(ctx, s.db, qualified,
		`ue.unit_id IN (SELECT id FROM `+qualified+`.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		bankID, docID); err != nil {
		return err
	}
	// A replaced document's units are being re-derived, so the survivors that
	// pointed at them lose their outgoing edges: queue them for a relink before
	// the links go.
	unitIDs, err := s.unitIDsForDocument(ctx, bankID, docID)
	if err != nil {
		return err
	}
	if err := s.enqueueRelinkVictims(ctx, s.db, bankID, unitIDs, false); err != nil {
		return err
	}
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

// PutDocumentChunks upserts one document's chunks in a single statement and
// reports the identifier each index resolves to.
//
// The upsert is guarded on the conflicting row belonging to the same bank:
// chunks is keyed on the identifier alone, so an identifier that collides with
// another bank's row (possible for rows written before the chunk-id escaping)
// must refuse the write rather than overwrite that bank's text.
func (s *PostgresStore) PutDocumentChunks(ctx context.Context, bankID, documentID string, chunks []DocumentChunk, storeText bool) (map[int]string, error) {
	if len(chunks) == 0 {
		return map[int]string{}, nil
	}
	ids := make([]string, 0, len(chunks))
	documentIDs := make([]string, 0, len(chunks))
	bankIDs := make([]string, 0, len(chunks))
	texts := make([]string, 0, len(chunks))
	indices := make([]int32, 0, len(chunks))
	hashes := make([]string, 0, len(chunks))
	byIndex := make(map[int]string, len(chunks))
	for _, chunk := range chunks {
		id := BuildChunkID(bankID, documentID, chunk.ChunkIndex)
		ids = append(ids, id)
		documentIDs = append(documentIDs, documentID)
		bankIDs = append(bankIDs, bankID)
		text := chunk.ChunkText
		if !storeText {
			text = ""
		}
		texts = append(texts, text)
		indices = append(indices, int32(chunk.ChunkIndex))
		// The hash comes from the real text either way, so a deployment that
		// keeps no text still recognises a chunk it has already stored.
		hashes = append(hashes, ContentHash(chunk.ChunkText))
		byIndex[chunk.ChunkIndex] = id
	}

	table := quoteIdent(s.schema) + `.chunks`
	rows, err := s.db.QueryContext(ctx,
		`INSERT INTO `+table+` (chunk_id, bank_id, document_id, chunk_index, chunk_text, content_hash)
		 SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::integer[], $5::text[], $6::text[])
		 ON CONFLICT (chunk_id) DO UPDATE SET
		     chunk_text = EXCLUDED.chunk_text,
		     chunk_index = EXCLUDED.chunk_index,
		     content_hash = EXCLUDED.content_hash
		 WHERE `+table+`.bank_id = EXCLUDED.bank_id
		 RETURNING chunk_id`,
		ids, bankIDs, documentIDs, indices, texts, hashes)
	if err != nil {
		return nil, err
	}
	written := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		written[id] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(written) != len(ids) {
		skipped := make([]string, 0, len(ids)-len(written))
		for _, id := range ids {
			if !written[id] {
				skipped = append(skipped, id)
			}
		}
		return nil, fmt.Errorf("chunk id(s) %v already belong to another bank", skipped)
	}
	return byIndex, nil
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

// GetDocument returns one stored document by identifier, with the count of the
// memory units written from it (units carrying its document_id; an observation
// has no document id of its own) counted in the same statement, so the count
// never depends on the document being inside a listing window.
func (s *PostgresStore) GetDocument(ctx context.Context, bankID, docID string) (Document, error) {
	query := `SELECT id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''), created_at, updated_at,
	                 COALESCE(retain_params, '{}'::jsonb), COALESCE(tags, '{}'),
	                 (SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.memory_units u
	                   WHERE u.bank_id = documents.bank_id AND u.document_id = documents.id)
	          FROM ` + quoteIdent(s.schema) + `.documents WHERE id=$1 AND bank_id=$2`
	var doc Document
	var paramsRaw, tagsRaw []byte
	err := s.reader().QueryRowContext(ctx, query, docID, bankID).Scan(&doc.ID, &doc.BankID, &doc.OriginalText,
		&doc.ContentHash, &doc.CreatedAt, &doc.UpdatedAt, &paramsRaw, &tagsRaw, &doc.MemoryUnitCount)
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
	// document_metadata is sourced from retain_params.metadata, the same key the
	// listing serves it on, so the detail read agrees with the list read.
	doc.Metadata = DocumentMetadata(doc.RetainParams)
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
	if metadata := DocumentMetadataValue(doc.Metadata); metadata != nil {
		params = copyAnyMap(params)
		params["metadata"] = metadata
	}
	retainParams, err := jsonColumn(params)
	if err != nil {
		return Document{}, err
	}
	tags := normalizeTags(doc.Tags)
	// A caller that carries the source's creation time (an archive import) keeps
	// it, so the restored document shows when it was written rather than when it
	// was copied; a zero value falls back to now(), which is what an ordinary
	// write means. The conflict path below leaves created_at untouched, so a
	// rewrite never re-stamps an existing row.
	var createdArg any
	if !doc.CreatedAt.IsZero() {
		createdArg = doc.CreatedAt
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.documents
	          (id, bank_id, original_text, content_hash, retain_params, tags, created_at, updated_at)
	          VALUES ($1,$2,$3,$4,$5::jsonb,$6, COALESCE($7, now()), now())
	          ON CONFLICT (id, bank_id) DO UPDATE SET
	              original_text = $3, content_hash = $4,
	              retain_params = $5::jsonb, tags = $6, updated_at = now()
	          RETURNING id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
	                    COALESCE(retain_params, '{}'::jsonb),
	                    COALESCE(tags, '{}'), created_at, updated_at`
	args := []any{docID, bankID, originalText, doc.ContentHash, retainParams, tags, createdArg}
	if expectHash != "" {
		query = `UPDATE ` + quoteIdent(s.schema) + `.documents
		         SET original_text = $3, content_hash = $4,
		             retain_params = $5::jsonb, tags = $6, updated_at = now()
		         WHERE id = $1 AND bank_id = $2 AND COALESCE(content_hash, '') = $7
		         RETURNING id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
		                   COALESCE(retain_params, '{}'::jsonb),
		                   COALESCE(tags, '{}'), created_at, updated_at`
		args = []any{docID, bankID, originalText, doc.ContentHash, retainParams, tags, expectHash}
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
	out.Metadata = DocumentMetadata(out.RetainParams)
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
// Resolution on treats the name as a guess: the exact case-insensitive match
// the unique index enforces is tried first, then an existing entity whose name
// scores at or above the similarity threshold, with the trigram similarity
// ordered by score so the closest match wins. Resolution off takes the name
// literally and reuses an entity only on a case-insensitive exact match.
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
		//
		// The read-back is the exact case-insensitive lookup the index enforces,
		// never the fuzzy path: the fuzzy probe is a trigram similarity that
		// scores zero for symbol- or punctuation-only names, so a name that
		// collided on lower() after a fuzzy miss would miss again and turn a lost
		// race into a failed write. PostgreSQL's LOWER() and Go's case folding
		// also disagree on some code points, so the comparison lower-cases both
		// sides in the database rather than binding a Go-lowercased value.
		if isUniqueViolation(err) {
			if existing, found, findErr := s.findEntityByExactName(ctx, bankID, name); findErr != nil {
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
	if !resolve {
		return s.findEntityByExactName(ctx, bankID, name)
	}
	// The exact case-insensitive match the unique index enforces is tried first,
	// matching the reference's resolution order: a name that lower-cases onto an
	// existing row is that row, whatever its similarity score. This also keeps
	// the resolve path from missing its own just-inserted row when the fuzzy
	// probe scores the name below the pool floor, which is every name made only
	// of symbols or punctuation (the trigram probe returns nothing for them).
	if existing, found, err := s.findEntityByExactName(ctx, bankID, name); err != nil {
		return Entity{}, false, err
	} else if found {
		return existing, true, nil
	}
	// The trigram probe narrows the pool; the similarity score then decides the
	// merge. The two are separate stages on purpose, because trigram ranks
	// names differently from the score the threshold is expressed in, so a
	// candidate that passes the pool filter may still be rejected.
	candidates, err := s.entityCandidates(ctx, bankID, name)
	if err != nil {
		// The trigram extension may be unavailable. An exact match was already
		// ruled out above, so a missing extension leaves nothing to match on and
		// the caller creates the entity.
		return Entity{}, false, nil
	}
	return bestEntityCandidate(candidates, name, s.mergeMinSimilarity())
}

// findEntityByExactName reuses an entity only when its name lower-cases to the
// same string, which is the predicate of the unique (bank_id,
// lower(canonical_name)) index. Both sides are lower-cased in the database:
// PostgreSQL's LOWER() and Go's case folding disagree on some code points, so
// binding a Go-lowercased value could miss the row the index would collide on.
func (s *PostgresStore) findEntityByExactName(ctx context.Context, bankID, name string) (Entity, bool, error) {
	query := `SELECT ` + entityColumns + ` FROM ` + quoteIdent(s.schema) + `.entities
	          WHERE bank_id=$1 AND lower(canonical_name)=lower($2) LIMIT 1`
	return s.scanEntity(ctx, query, bankID, name)
}

// mergeMinSimilarity is the reference's second trigram gate, falling back to
// the shipped default when the tuning was not set.
func (s *PostgresStore) mergeMinSimilarity() float64 {
	if s.tuning.EntityMergeMinSimilarity > 0 {
		return s.tuning.EntityMergeMinSimilarity
	}
	return DefaultEntityMergeMinSimilarity
}

// intrabatchMergeSimilarity is the pg_trgm similarity at or above which two
// names in one extraction batch are merged before either is scored, falling
// back to the shipped default when the tuning was not set.
func (s *PostgresStore) intrabatchMergeSimilarity() float64 {
	if s.tuning.EntityIntrabatchMerge > 0 {
		return s.tuning.EntityIntrabatchMerge
	}
	return DefaultEntityIntrabatchMergeSimilarity
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
//
// A candidate must clear the merge floor (the reference's second trigram gate)
// before it is scored: the probe admits candidates far looser than the merge
// threshold, and the score alone can reach it on signals other than the name,
// so the floor keeps a name that is only coincidentally close from being merged
// onto.
func bestEntityCandidate(candidates []Entity, name string, mergeFloor float64) (Entity, bool, error) {
	best := Entity{}
	bestScore := 0.0
	for _, candidate := range candidates {
		if !entityMergeFloorPasses(name, candidate.CanonicalName, mergeFloor) {
			continue
		}
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
	           VALUES ($1,$2,$3,` + s.searchVectorExpr("$3", "$4", "$20") + `,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14::vector,
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
	if _, err := s.db.ExecContext(ctx, insert, unit.ID, unit.BankID, unit.Text, nullableContext(unit.Context, unit.ContextAbsent),
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

// releaseEntityPostings gives back the mention counts the entity postings a
// cascade is about to remove contributed, the inverse of UpsertUnitEntity.
//
// mention_count is otherwise increment-only, so without this the denormalised
// count drifts upward every time a document is replaced, a memory is deleted
// or invalidated, memories are cleared, or an edit rewrites a unit's entity
// set. postingsWhere is a predicate over the postings alias `ue` selecting
// exactly the rows the caller is about to delete; it must run in the same
// transaction as — and before — that delete, because afterwards the rows the
// entity ids are read from are gone. The decrement is per entity and floored
// at zero, counting the postings actually going rather than assuming one per
// unit, mirroring the reference's single unlink choke point (its
// enqueue_entity_prune_candidates → release_entity_postings).
func releaseEntityPostings(ctx context.Context, q stmtExecer, qualified, postingsWhere string, args ...any) error {
	_, err := q.ExecContext(ctx, `UPDATE `+qualified+`.entities e
		SET mention_count = GREATEST(e.mention_count - v.n, 0)
		FROM (
			SELECT ue.entity_id AS id, COUNT(*) AS n
			FROM `+qualified+`.unit_entities ue
			WHERE `+postingsWhere+`
			GROUP BY ue.entity_id
		) v
		WHERE e.id = v.id`, args...)
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

	dropped := make([]string, 0, len(linked))
	for _, id := range linked {
		if _, keep := wanted[id]; keep {
			delete(wanted, id)
			continue
		}
		dropped = append(dropped, id)
	}
	// The postings that are about to go each contributed one mention, so give
	// them back before the rows are deleted: an edit that rewrites the entity
	// set is one of the unlink paths that would otherwise drift the count.
	if len(dropped) > 0 {
		if err := releaseEntityPostings(ctx, s.db, qualified,
			`ue.unit_id = $1::uuid AND ue.entity_id = ANY($2::uuid[])`, unitID, dropped); err != nil {
			return err
		}
	}
	for _, id := range dropped {
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
	// The set the unit now names witnesses its pairs, which is what the
	// reference records when an edit re-links the unit: a pair that no longer
	// shares a unit is left to the entity prune, which deletes a co-occurrence
	// no current unit witnesses.
	return s.recordEditedSetCooccurrences(ctx, bankID, unitID)
}

// recordEditedSetCooccurrences records the pairs the unit's resulting entity set
// witnesses, reading the set back from the postings so a link that survived the
// edit is counted exactly as a new one is. An edit that names one entity
// records nothing, because a pair needs two.
func (s *PostgresStore) recordEditedSetCooccurrences(ctx context.Context, bankID, unitID string) error {
	qualified := quoteIdent(s.schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id::text FROM `+qualified+`.unit_entities WHERE unit_id = $1::uuid ORDER BY entity_id`, unitID)
	if err != nil {
		return err
	}
	entityIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		entityIDs = append(entityIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(entityIDs) < 2 {
		return nil
	}
	// The pair carries the unit's own seen time, exactly as a retain of that
	// unit stamps it, so an edit does not collapse the pair's recency onto the
	// edit moment.
	var occurred, mentioned, event sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		`SELECT occurred_start, mentioned_at, event_date FROM `+qualified+`.memory_units WHERE id = $1::uuid`,
		unitID).Scan(&occurred, &mentioned, &event); err != nil {
		return err
	}
	unit := MemoryUnit{
		OccurredStart: nullTimePtr(occurred),
		MentionedAt:   nullTimePtr(mentioned),
		EventDate:     nullTimePtr(event),
	}
	return s.UpsertUnitCooccurrences(ctx, bankID, entityIDs, UnitSeenAt(unit))
}

// UnitEntityNames returns the entities each unit mentions, keyed by unit id.
//
// Direct postings answer for most units. An observation rarely has postings of
// its own: its entities live transitively through the memories it was
// consolidated from, so it inherits theirs when it has none — which is what the
// unit's own detail read reports, and a listing that disagreed with it would be
// showing the same memory two ways.
func (s *PostgresStore) UnitEntityNames(ctx context.Context, bankID string, unitIDs []string) (map[string][]string, error) {
	if len(unitIDs) == 0 {
		return map[string][]string{}, nil
	}
	qualified := quoteIdent(s.schema)
	query := `
		SELECT ue.unit_id::text, e.canonical_name
		FROM ` + qualified + `.unit_entities ue
		JOIN ` + qualified + `.entities e ON e.id = ue.entity_id
		WHERE ue.unit_id = ANY($1::uuid[]) AND e.bank_id = $2
		UNION
		SELECT obs.id::text, e.canonical_name
		FROM ` + qualified + `.memory_units obs
		CROSS JOIN LATERAL unnest(obs.source_memory_ids) AS src_id
		JOIN ` + qualified + `.unit_entities src_ue ON src_ue.unit_id = src_id
		JOIN ` + qualified + `.entities e ON e.id = src_ue.entity_id
		WHERE obs.id = ANY($1::uuid[]) AND obs.fact_type = 'observation'
		  AND obs.source_memory_ids IS NOT NULL
		  AND e.bank_id = $2
		  AND NOT EXISTS (SELECT 1 FROM ` + qualified + `.unit_entities d WHERE d.unit_id = obs.id)`
	rows, err := s.db.QueryContext(ctx, query, unitIDs, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byUnit := map[string][]string{}
	for rows.Next() {
		var unitID, name string
		if err := rows.Scan(&unitID, &name); err != nil {
			return nil, err
		}
		// UNION removes duplicate rows, but a unit can reach one entity through
		// several source memories, so the names are deduped per unit as well.
		if slices.Contains(byUnit[unitID], name) {
			continue
		}
		byUnit[unitID] = append(byUnit[unitID], name)
	}
	return byUnit, rows.Err()
}

// UpsertEntityCooccurrence records that two entities co-occur in a bank.
//
// The reference keys the pair by identifier with a check that the lower one
// comes first, so the row is not addressed by bank at all. The endpoints are
// ordered here, so no caller has to know which of the two is smaller.
func (s *PostgresStore) UpsertEntityCooccurrence(ctx context.Context, bankID, entityID1, entityID2 string) error {
	return s.upsertEntityCooccurrence(ctx, entityID1, entityID2, time.Now().UTC())
}

// UpsertUnitCooccurrences records one co-occurrence for every unordered pair of
// the entities a single memory unit mentions — the same cross product retain
// records, so an imported unit contributes the pairs its own entity set
// witnesses and nothing more.
func (s *PostgresStore) UpsertUnitCooccurrences(ctx context.Context, bankID string, entityIDs []string, seenAt time.Time) error {
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	for i := range len(entityIDs) {
		for j := i + 1; j < len(entityIDs); j++ {
			if err := s.upsertEntityCooccurrence(ctx, entityIDs[i], entityIDs[j], seenAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// upsertEntityCooccurrence upserts one unordered pair, stamping the pair's
// last-co-occurrence with the time the witnessing unit was seen.
//
// Both members of the write are the reference's: the count accumulates rather
// than being replaced, and the stamp is the LATEST of the two times, so a unit
// seen before the pair's existing stamp cannot pull the pair's recency back.
func (s *PostgresStore) upsertEntityCooccurrence(ctx context.Context, entityID1, entityID2 string, seenAt time.Time) error {
	if entityID1 > entityID2 {
		entityID1, entityID2 = entityID2, entityID1
	}
	table := quoteIdent(s.schema) + `.entity_cooccurrences`
	query := `INSERT INTO ` + table + ` (entity_id_1, entity_id_2, cooccurrence_count, last_cooccurred)
	          VALUES ($1,$2,1,$3)
	          ON CONFLICT (entity_id_1, entity_id_2)
	          DO UPDATE SET cooccurrence_count = ` + table + `.cooccurrence_count + 1,
	                        last_cooccurred = GREATEST(` + table + `.last_cooccurred, EXCLUDED.last_cooccurred)`
	_, err := s.db.ExecContext(ctx, query, entityID1, entityID2, seenAt)
	return err
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

// nullableContext renders a unit's context column: NULL for a unit whose context
// is absent (an observation, which the reference's insert never names the column
// for) and the string itself otherwise, including the empty one a fact with no
// context stores.
func nullableContext(context string, absent bool) any {
	if absent {
		return nil
	}
	return context
}

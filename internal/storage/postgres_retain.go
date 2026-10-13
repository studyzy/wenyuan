package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		units, err := s.writeItem(ctx, req, item, extracted[index], now)
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
	if _, err := s.PutOperation(ctx, Operation{
		ID: opID, BankID: req.BankID, OperationType: OperationTypeRetain,
		Status: status, Run: 1, CreatedAt: now, UpdatedAt: now, CompletedAt: completedAt,
	}); err != nil {
		return RetainOutcome{}, err
	}
	return outcome, nil
}

// writeItem persists one item's document, chunks, units, entity links, and the
// links between the units the item produced.
func (s *PostgresStore) writeItem(ctx context.Context, req RetainRequest, item RetainItem, facts []ExtractedFact, now time.Time) ([]string, error) {
	docID := item.DocumentID
	if docID == "" {
		docID = newID()
	}
	tags := mergeTags(item.Tags, req.DocumentTags)
	if _, err := s.PutDocument(ctx, req.BankID, docID, Document{
		OriginalText: item.Content,
		ContentHash:  ContentHash(item.Content),
		Metadata:     item.Metadata,
		RetainParams: map[string]any{"strategy": item.Strategy, "update_mode": item.UpdateMode},
		Tags:         tags,
	}); err != nil {
		return nil, err
	}
	chunkIDs, err := s.putItemChunks(ctx, req.BankID, docID, item.Content)
	if err != nil {
		return nil, err
	}
	contents := itemContents(req.BankID, docID, chunkIDs, item, facts, tags)
	units := make([]string, 0, len(contents))
	for _, content := range contents {
		stored, err := s.UpsertUnit(ctx, content.unit)
		if err != nil {
			return nil, err
		}
		for _, entity := range content.entities {
			resolved, err := s.ResolveRetainEntity(ctx, req.BankID, entity, item.ResolveEntities)
			if err != nil {
				return nil, err
			}
			if err := s.UpsertUnitEntity(ctx, stored.ID, resolved.ID); err != nil {
				return nil, err
			}
		}
		units = append(units, stored.ID)
	}
	if err := s.linkItemUnits(ctx, req.BankID, units); err != nil {
		return nil, err
	}
	return units, nil
}

// putItemChunks persists one item's segments and returns their identifiers in
// order.
func (s *PostgresStore) putItemChunks(ctx context.Context, bankID, docID, content string) ([]string, error) {
	chunks := Segment(content)
	chunkIDs := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		chunkID := newID()
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO `+quoteIdent(s.schema)+`.chunks (id, bank_id, document_id, index, text, content_hash)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			chunkID, bankID, docID, i, chunk, ContentHash(chunk)); err != nil {
			return nil, err
		}
		chunkIDs = append(chunkIDs, chunkID)
	}
	return chunkIDs, nil
}

// linkItemUnits records the links between consecutive units one item produced.
func (s *PostgresStore) linkItemUnits(ctx context.Context, bankID string, unitIDs []string) error {
	for _, link := range linkTuplesFor(bankID, unitIDs) {
		if err := s.UpsertLink(ctx, link); err != nil {
			return err
		}
	}
	return nil
}

// GetDocument returns one stored document by identifier.
func (s *PostgresStore) GetDocument(ctx context.Context, bankID, docID string) (Document, error) {
	query := `SELECT id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''), created_at
	          FROM ` + quoteIdent(s.schema) + `.documents WHERE id=$1 AND bank_id=$2`
	var doc Document
	err := s.db.QueryRowContext(ctx, query, docID, bankID).Scan(&doc.ID, &doc.BankID, &doc.OriginalText, &doc.ContentHash, &doc.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrDocumentNotFound
	}
	return doc, err
}

// PutDocument stores a document's text, hash, metadata and parameters.
func (s *PostgresStore) PutDocument(ctx context.Context, bankID, docID string, doc Document) (Document, error) {
	if doc.ContentHash == "" {
		doc.ContentHash = ContentHash(doc.OriginalText)
	}
	// The metadata, retain-parameter and tag columns exist on the table, so
	// they are written here rather than silently dropped.
	metadata, err := jsonColumn(doc.Metadata)
	if err != nil {
		return Document{}, err
	}
	retainParams, err := jsonColumn(doc.RetainParams)
	if err != nil {
		return Document{}, err
	}
	tags := normalizeTags(doc.Tags)
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.documents
	          (id, bank_id, original_text, content_hash, metadata, retain_params, tags, created_at)
	          VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7, now())
	          ON CONFLICT (id) DO UPDATE SET
	              original_text = $3, content_hash = $4, metadata = $5::jsonb,
	              retain_params = $6::jsonb, tags = $7
	          RETURNING id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
	                    COALESCE(metadata, '{}'::jsonb), COALESCE(retain_params, '{}'::jsonb),
	                    COALESCE(tags, '{}'), created_at, created_at`
	var out Document
	var metadataRaw, retainRaw, tagsRaw []byte
	err = s.db.QueryRowContext(ctx, query, docID, bankID, doc.OriginalText, doc.ContentHash,
		metadata, retainParams, tags).
		Scan(&out.ID, &out.BankID, &out.OriginalText, &out.ContentHash, &metadataRaw, &retainRaw, &tagsRaw, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return Document{}, err
	}
	out.Metadata = decodeJSONMap(metadataRaw)
	out.RetainParams = decodeJSONMap(retainRaw)
	out.Tags = decodePostgresTextArray(tagsRaw)
	return out, nil
}

// AppendDocument concatenates text onto the stored document, failing with
// ErrDocumentConflict when the stored hash no longer matches expectedHash.
func (s *PostgresStore) AppendDocument(ctx context.Context, bankID, docID, text, expectedHash string) (Document, error) {
	current, err := s.GetDocument(ctx, bankID, docID)
	if err == nil && expectedHash != "" && current.ContentHash != expectedHash {
		return Document{}, ErrDocumentConflict
	}
	combined := current.OriginalText + text
	return s.PutDocument(ctx, bankID, docID, Document{OriginalText: combined})
}

// entityColumns is the column list every entity read selects, so a scan can
// never drift from the query that produced it.
const entityColumns = `id, bank_id, name, COALESCE(kind,'regular'), COALESCE(mention_count,0)`

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
	entity := Entity{ID: newID(), BankID: bankID, Name: name, Kind: kind, MentionCount: 1}
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.entities (id, bank_id, name, kind, mention_count, first_seen, last_seen)
	           VALUES ($1,$2,$3,$4,1,now(),now())
	           ON CONFLICT (id) DO NOTHING`
	if _, err := s.db.ExecContext(ctx, insert, entity.ID, bankID, name, string(kind)); err != nil {
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
			` WHERE bank_id=$1 AND lower(name)=lower($2) LIMIT 1`
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
			` WHERE bank_id=$1 AND lower(name)=lower($2) LIMIT 1`
		return s.scanEntity(ctx, fallback, bankID, name)
	}
	return bestEntityCandidate(candidates, name)
}

// entityCandidates returns the stored entities whose names are close enough to
// the supplied name to be worth scoring.
func (s *PostgresStore) entityCandidates(ctx context.Context, bankID, name string) ([]Entity, error) {
	query := `SELECT ` + entityColumns + ` FROM ` + quoteIdent(s.schema) + `.entities
	          WHERE bank_id=$1 AND similarity(lower(name), lower($2)) >= $3
	          ORDER BY similarity(lower(name), lower($2)) DESC, id ASC
	          LIMIT 100`
	rows, err := s.db.QueryContext(ctx, query, bankID, name, EntityCandidateSimilarity)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	candidates := make([]Entity, 0)
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(&entity.ID, &entity.BankID, &entity.Name, &entity.Kind, &entity.MentionCount); err != nil {
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
		score := entitySimilarity(candidate.Name, name)
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
		&entity.ID, &entity.BankID, &entity.Name, &entity.Kind, &entity.MentionCount)
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
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.memory_units
	           (id, bank_id, text, type, metadata, document_id, chunk_id, occurred_start, tags, embedding,
	            source_id, observation_scopes, consolidated_at, consolidation_failed_at, attachment_ids)
	           VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::vector,$11,$12::jsonb,$13,$14,$15)
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
	scopes := any(nil)
	if len(unit.ObservationScopes) > 0 {
		encoded, err := json.Marshal(unit.ObservationScopes)
		if err != nil {
			return MemoryUnit{}, err
		}
		scopes = string(encoded)
	}
	if _, err := s.db.ExecContext(ctx, insert, unit.ID, unit.BankID, unit.Text, string(unit.Type),
		"{}", nullString(unit.DocumentID), nullString(unit.ChunkID), unit.OccurredStart, tags, embedding,
		nullString(unit.SourceID), scopes, unit.ConsolidatedAt, unit.ConsolidationFailedAt, attachmentIDs); err != nil {
		return MemoryUnit{}, err
	}
	return unit, nil
}

// UpsertUnitEntity links a memory unit to an entity identity.
func (s *PostgresStore) UpsertUnitEntity(ctx context.Context, unitID, entityID string) error {
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.unit_entities (unit_id, entity_id)
	          VALUES ($1,$2) ON CONFLICT DO NOTHING`
	_, err := s.db.ExecContext(ctx, query, unitID, entityID)
	return err
}

// UpsertEntityCooccurrence records that two entities co-occur in a bank.
func (s *PostgresStore) UpsertEntityCooccurrence(ctx context.Context, bankID, entityA, entityB string) error {
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.entity_cooccurrences (bank_id, entity_a, entity_b, count)
	          VALUES ($1,$2,$3,1)
	          ON CONFLICT (bank_id, entity_a, entity_b) DO UPDATE SET count = entity_cooccurrences.count + 1`
	_, err := s.db.ExecContext(ctx, query, bankID, entityA, entityB)
	return err
}

// UpsertLink persists one directed memory link.
func (s *PostgresStore) UpsertLink(ctx context.Context, link MemoryLink) error {
	if link.ID == "" {
		link.ID = newID()
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.memory_links (id, bank_id, from_id, to_id, link_type)
	          VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`
	_, err := s.db.ExecContext(ctx, query, link.ID, link.BankID, link.FromID, link.ToID, string(link.LinkType))
	return err
}

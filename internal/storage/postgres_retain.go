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
	opID := req.OperationID
	if opID == "" {
		opID = newUUID()
	}
	now := time.Now().UTC()
	grouped := make([][]string, 0, len(req.Items))
	total := 0
	for _, item := range req.Items {
		units, err := s.writeItem(ctx, req, item, now)
		if err != nil {
			return RetainOutcome{}, err
		}
		grouped = append(grouped, units)
		total += len(units)
	}
	outcome := RetainOutcome{
		ItemUnitIDs:            grouped,
		Usage:                  RetainUsageFor(total),
		OperationID:            opID,
		OperationIDs:           []string{opID},
		ProcessedContentTokens: total,
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

// writeItem persists one item's document, chunks, units, and entity links.
func (s *PostgresStore) writeItem(ctx context.Context, req RetainRequest, item RetainItem, now time.Time) ([]string, error) {
	docID := item.DocumentID
	if docID == "" {
		docID = newID()
	}
	if _, err := s.PutDocument(ctx, req.BankID, docID, Document{
		OriginalText: item.Content,
		ContentHash:  ContentHash(item.Content),
		Metadata:     item.Metadata,
		RetainParams: map[string]any{"strategy": item.Strategy, "update_mode": item.UpdateMode},
	}); err != nil {
		return nil, err
	}
	tags := mergeTags(item.Tags, req.DocumentTags)
	chunks := Segment(item.Content)
	units := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		chunkID := newID()
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO `+quoteIdent(s.schema)+`.chunks (id, bank_id, document_id, index, text, content_hash)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			chunkID, req.BankID, docID, i, chunk, ContentHash(chunk)); err != nil {
			return nil, err
		}
		unit := MemoryUnit{
			ID: newID(), BankID: req.BankID, Text: chunk, Type: TypeWorld,
			Metadata: item.Metadata, DocumentID: docID, ChunkID: chunkID, Tags: tags,
		}
		if item.Timestamp != nil {
			unit.EventDate = item.Timestamp
			unit.OccurredStart = item.Timestamp
		}
		stored, err := s.UpsertUnit(ctx, unit)
		if err != nil {
			return nil, err
		}
		for _, name := range item.Entities {
			entity, err := s.ResolveEntity(ctx, req.BankID, name, EntityKindRegular, item.ResolveEntities)
			if err != nil {
				return nil, err
			}
			if err := s.UpsertUnitEntity(ctx, stored.ID, entity.ID); err != nil {
				return nil, err
			}
		}
		units = append(units, stored.ID)
	}
	return units, nil
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

// ResolveEntity resolves an entity name against the bank.
func (s *PostgresStore) ResolveEntity(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error) {
	if kind == "" {
		kind = EntityKindRegular
	}
	query := `SELECT id, bank_id, name, COALESCE(kind,'regular'), COALESCE(mention_count,0)
	          FROM ` + quoteIdent(s.schema) + `.entities WHERE bank_id=$1 AND lower(name)=lower($2) LIMIT 1`
	var entity Entity
	err := s.db.QueryRowContext(ctx, query, bankID, name).Scan(&entity.ID, &entity.BankID, &entity.Name, &entity.Kind, &entity.MentionCount)
	if err == nil {
		return entity, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Entity{}, err
	}
	entity = Entity{ID: newID(), BankID: bankID, Name: name, Kind: kind, MentionCount: 1}
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.entities (id, bank_id, name, kind, mention_count, first_seen, last_seen)
	           VALUES ($1,$2,$3,$4,1,now(),now())
	           ON CONFLICT (id) DO NOTHING`
	if _, err := s.db.ExecContext(ctx, insert, entity.ID, bankID, name, string(kind)); err != nil {
		return Entity{}, err
	}
	return entity, nil
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
	            source_id, observation_scopes, consolidated_at, consolidation_failed_at)
	           VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::vector,$11,$12::jsonb,$13,$14)
	           ON CONFLICT (id) DO UPDATE SET embedding = EXCLUDED.embedding
	           WHERE ` + quoteIdent(s.schema) + `.memory_units.embedding IS NULL`
	// A nil tag slice would violate the column's not-null constraint, so an
	// absent tag set is stored as an empty array.
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
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
		nullString(unit.SourceID), scopes, unit.ConsolidatedAt, unit.ConsolidationFailedAt); err != nil {
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

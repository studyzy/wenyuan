package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// RetainWrite persists an ordered-item memory write in the in-process store. It
// lazily creates the bank, honors operation-id idempotency, segments content
// into chunks, persists units, resolves entities, and returns created unit
// identifiers grouped per item in item order.
func (s *MemoryStore) RetainWrite(ctx context.Context, req RetainRequest) (RetainOutcome, error) {
	// Extraction calls a model, so it runs before the write lock is taken: a
	// slow model call must not block every other write to the store.
	extracted, err := extractForItems(ctx, req)
	if err != nil {
		return RetainOutcome{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return RetainOutcome{}, s.writeErr
	}
	if _, ok := s.banks[req.BankID]; !ok {
		s.createBankLocked(req.BankID)
	}
	opID := req.OperationID
	if opID == "" {
		opID = newUUID()
	} else if _, exists := s.operations[opID]; exists {
		return RetainOutcome{}, ErrOperationConflict
	}
	now := time.Now().UTC()

	grouped := make([][]string, 0, len(req.Items))
	for index, item := range req.Items {
		units := s.writeItemLocked(req, item, extracted[index], now)
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
	s.recordWriteWatermarkLocked(req.BankID, now)
	if req.Async {
		s.operations[opID] = Operation{
			ID:            opID,
			BankID:        req.BankID,
			OperationType: OperationTypeRetain,
			Status:        StatusPending,
			Run:           1,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
	} else {
		s.operations[opID] = Operation{
			ID:            opID,
			BankID:        req.BankID,
			OperationType: OperationTypeRetain,
			Status:        StatusCompleted,
			Run:           1,
			CreatedAt:     now,
			UpdatedAt:     now,
			CompletedAt:   &now,
		}
	}
	return outcome, nil
}

// writeItemLocked persists one item and returns the created unit identifiers in
// segmentation order.
// writeItemLocked persists one item and returns the created unit identifiers in
// extraction order when the item was extracted, and in segmentation order
// otherwise.
func (s *MemoryStore) writeItemLocked(req RetainRequest, item RetainItem, facts []ExtractedFact, now time.Time) []string {
	docID := item.DocumentID
	if docID == "" {
		docID = newID()
	}
	s.putDocumentLocked(req.BankID, docID, item.Content, item, now)
	tags := mergeTags(item.Tags, req.DocumentTags)

	chunkIDs := s.putItemChunksLocked(req.BankID, docID, item.Content)
	contents := itemContents(req.BankID, docID, chunkIDs, item, facts, tags)
	units := make([]string, 0, len(contents))
	for _, content := range contents {
		unit := content.unit
		unit.ID = newID()
		s.upsertUnitLocked(unit)
		for _, entity := range content.entities {
			resolved := s.resolveEntityLocked(req.BankID, entity, item.ResolveEntities)
			if resolved.ID == "" {
				continue
			}
			s.unitEntities[unit.ID] = append(s.unitEntities[unit.ID], resolved.ID)
		}
		units = append(units, unit.ID)
	}
	s.links = append(s.links, linkTuplesFor(req.BankID, units)...)
	s.links = append(s.links, causalLinksFor(req.BankID, units, contents)...)
	return units
}

// putItemChunksLocked stores one item's segments and returns their identifiers
// in order.
func (s *MemoryStore) putItemChunksLocked(bankID, docID, content string) []string {
	chunks := Segment(content)
	chunkIDs := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		chunkID := newID()
		s.chunks = append(s.chunks, Chunk{
			ID: chunkID, BankID: bankID, DocumentID: docID, Index: i, Text: chunk, ContentHash: ContentHash(chunk),
		})
		chunkIDs = append(chunkIDs, chunkID)
	}
	return chunkIDs
}

// putDocumentLocked stores a document's text, hash, metadata and parameters.
func (s *MemoryStore) putDocumentLocked(bankID, docID, text string, item RetainItem, now time.Time) {
	tags := item.Tags
	if tags == nil {
		tags = []string{}
	}
	s.documents[docID] = Document{
		ID:           docID,
		BankID:       bankID,
		OriginalText: text,
		ContentHash:  ContentHash(text),
		Metadata:     item.Metadata,
		RetainParams: map[string]any{"strategy": item.Strategy, "update_mode": item.UpdateMode},
		Tags:         tags,
		CreatedAt:    now,
	}
}

// upsertUnitLocked stores a memory unit in the in-process store, keeping the
// unit map and the per-bank recall list consistent. Recall reads the per-bank
// list while unit lookups read the map, so a unit written through either path
// must appear in both.
func (s *MemoryStore) upsertUnitLocked(unit MemoryUnit) {
	_, existed := s.units[unit.ID]
	s.units[unit.ID] = unit
	if existed {
		s.updateRecallMatchLocked(unit)
		return
	}
	s.memories[unit.BankID] = append(s.memories[unit.BankID], recallMatchFromUnit(unit))
}

// updateRecallMatchLocked refreshes the recall row for an existing unit.
func (s *MemoryStore) updateRecallMatchLocked(unit MemoryUnit) {
	rows := s.memories[unit.BankID]
	for i := range rows {
		if rows[i].ID == unit.ID {
			rows[i] = recallMatchFromUnit(unit)
			return
		}
	}
	s.memories[unit.BankID] = append(rows, recallMatchFromUnit(unit))
}

// recallMatchFromUnit projects a stored unit into the recall row shape.
func recallMatchFromUnit(unit MemoryUnit) RecallMatch {
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
	}
	return RecallMatch{
		ID:            unit.ID,
		Text:          unit.Text,
		Type:          string(unit.Type),
		Context:       unit.Context,
		OccurredStart: unit.OccurredStart,
		OccurredEnd:   unit.OccurredEnd,
		MentionedAt:   unit.MentionedAt,
		DocumentID:    unit.DocumentID,
		ChunkID:       unit.ChunkID,
		Metadata:      unit.Metadata,
		Tags:          tags,
		Scores:        map[string]float64{},
		SourceFactIDs: []string{},
	}
}

// resolveEntityLocked resolves one supplied entity against the bank.
//
// With resolution on, a name is a guess at which entity is meant: an existing
// entity is reused when it matches exactly or scores at or above the
// similarity threshold. With resolution off, a name is taken literally — an
// existing entity is reused only on a case-insensitive exact match, and any
// other name creates its own entity. The entity's label is not stored on the
// row; it only groups the mention during resolution.
func (s *MemoryStore) resolveEntityLocked(bankID string, entity RetainEntity, resolve bool) Entity {
	name := entityMatchKey(entity.Text)
	if name == "" {
		return Entity{}
	}
	if resolve {
		if match, found := s.bestEntityMatchLocked(bankID, name); found {
			return match
		}
	} else if match, found := s.exactEntityMatchLocked(bankID, name); found {
		return match
	}
	created := Entity{ID: newID(), BankID: bankID, Name: name, Kind: EntityKindRegular, MentionCount: 1}
	s.entities[created.ID] = created
	return created
}

// exactEntityMatchLocked finds a stored entity whose name matches
// case-insensitively.
func (s *MemoryStore) exactEntityMatchLocked(bankID, name string) (Entity, bool) {
	for id, stored := range s.entities {
		if stored.BankID == bankID && strings.EqualFold(entityMatchKey(stored.Name), name) {
			return s.entities[id], true
		}
	}
	return Entity{}, false
}

// bestEntityMatchLocked finds the stored entity most similar to name, provided
// it scores at or above the threshold. Ties break on the identifier so the
// resolution is deterministic across runs.
func (s *MemoryStore) bestEntityMatchLocked(bankID, name string) (Entity, bool) {
	best := Entity{}
	bestScore := 0.0
	for id, stored := range s.entities {
		if stored.BankID != bankID {
			continue
		}
		score := entitySimilarity(stored.Name, name)
		if score < EntitySimilarityThreshold {
			continue
		}
		if score > bestScore || (score == bestScore && best.ID != "" && id < best.ID) {
			best = s.entities[id]
			bestScore = score
		}
	}
	return best, best.ID != ""
}

// ContentHash returns the lowercase hex SHA-256 of the given content.
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func mergeTags(itemTags, documentTags []string) []string {
	out := make([]string, 0, len(itemTags)+len(documentTags))
	seen := map[string]bool{}
	for _, tag := range itemTags {
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	for _, tag := range documentTags {
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

// GetDocument returns one stored document by identifier.
func (s *MemoryStore) GetDocument(ctx context.Context, bankID, docID string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.documents[docID]
	if !ok || doc.BankID != bankID {
		return Document{}, ErrDocumentNotFound
	}
	return doc, nil
}

// PutDocument stores a document's text, hash, metadata and parameters.
func (s *MemoryStore) PutDocument(ctx context.Context, bankID, docID string, doc Document) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc.ID = docID
	doc.BankID = bankID
	if doc.ContentHash == "" {
		doc.ContentHash = ContentHash(doc.OriginalText)
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	s.documents[docID] = doc
	return doc, nil
}

// AppendDocument concatenates text onto the stored document, failing with
// ErrDocumentConflict when the stored hash no longer matches expectedHash.
func (s *MemoryStore) AppendDocument(ctx context.Context, bankID, docID, text, expectedHash string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.documents[docID]
	if ok && expectedHash != "" && doc.ContentHash != expectedHash {
		return Document{}, ErrDocumentConflict
	}
	doc.ID = docID
	doc.BankID = bankID
	doc.OriginalText = doc.OriginalText + text
	doc.ContentHash = ContentHash(doc.OriginalText)
	s.documents[docID] = doc
	return doc, nil
}

// ResolveEntity resolves an entity name against the bank, creating it with the
// given kind when nothing matches.
func (s *MemoryStore) ResolveEntity(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveEntityOfKindLocked(bankID, name, kind, resolve), nil
}

// resolveEntityOfKindLocked is the resolution a caller naming an explicit kind
// uses. Kind is the internal classification stored on the row; it is unrelated
// to the caller's label for an entity on the retain route.
func (s *MemoryStore) resolveEntityOfKindLocked(bankID, name string, kind EntityKind, resolve bool) Entity {
	entity := RetainEntity{Text: name}
	resolved := s.resolveEntityLocked(bankID, entity, resolve)
	if resolved.ID != "" && kind != "" && kind != EntityKindRegular {
		stored := s.entities[resolved.ID]
		stored.Kind = kind
		s.entities[resolved.ID] = stored
		resolved = stored
	}
	return resolved
}

// UpsertUnit persists one memory unit.
func (s *MemoryStore) UpsertUnit(ctx context.Context, unit MemoryUnit) (MemoryUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if unit.ID == "" {
		unit.ID = newID()
	}
	s.upsertUnitLocked(unit)
	return unit, nil
}

// UpsertUnitEntity links a memory unit to an entity identity.
func (s *MemoryStore) UpsertUnitEntity(ctx context.Context, unitID, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unitEntities[unitID] = append(s.unitEntities[unitID], entityID)
	return nil
}

// UpsertEntityCooccurrence records that two entities co-occur in a bank.
func (s *MemoryStore) UpsertEntityCooccurrence(ctx context.Context, bankID, entityA, entityB string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := bankID + "\x00" + entityA + "\x00" + entityB
	s.cooccurrences[key]++
	return nil
}

// UpsertLink persists one directed memory link.
func (s *MemoryStore) UpsertLink(ctx context.Context, link MemoryLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if link.ID == "" {
		link.ID = newID()
	}
	s.links = append(s.links, link)
	return nil
}

// UnitsForTest returns the stored memory units in insertion order for tests.
func (s *MemoryStore) UnitsForTest(bankID string) []MemoryUnit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MemoryUnit, 0)
	ids := make([]string, 0, len(s.units))
	for id := range s.units {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		unit := s.units[id]
		if unit.BankID == bankID {
			out = append(out, unit)
		}
	}
	return out
}

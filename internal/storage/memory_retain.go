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
	total := 0
	for _, item := range req.Items {
		units := s.writeItemLocked(req, item, now)
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
func (s *MemoryStore) writeItemLocked(req RetainRequest, item RetainItem, now time.Time) []string {
	docID := item.DocumentID
	if docID == "" {
		docID = newID()
	}
	text := item.Content
	chunks := Segment(text)
	s.putDocumentLocked(req.BankID, docID, text, item, now)
	tags := mergeTags(item.Tags, req.DocumentTags)
	units := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		chunkID := newID()
		unitID := newID()
		hash := ContentHash(chunk)
		s.chunks = append(s.chunks, Chunk{ID: chunkID, BankID: req.BankID, DocumentID: docID, Index: i, Text: chunk, ContentHash: hash})
		unit := MemoryUnit{
			ID:            unitID,
			BankID:        req.BankID,
			Text:          chunk,
			Type:          TypeWorld,
			Metadata:      item.Metadata,
			DocumentID:    docID,
			ChunkID:       chunkID,
			Tags:          tags,
			AttachmentIDs: item.AttachmentIDs,
		}
		if item.Timestamp != nil {
			unit.EventDate = item.Timestamp
			unit.OccurredStart = item.Timestamp
		}
		s.upsertUnitLocked(unit)
		for _, name := range item.Entities {
			entity := s.resolveEntityLocked(req.BankID, name, EntityKindRegular, item.ResolveEntities)
			s.unitEntities[unitID] = append(s.unitEntities[unitID], entity.ID)
		}
		units = append(units, unitID)
		s.memories[req.BankID] = append(s.memories[req.BankID], RecallMatch{
			ID: unitID, Text: chunk, Type: string(TypeWorld), DocumentID: docID, ChunkID: chunkID,
			Tags: tags, Scores: map[string]float64{}, SourceFactIDs: []string{},
		})
	}
	return units
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

// resolveEntityLocked resolves an entity name against the bank. With resolve on
// a name close to an existing entity resolves to it; with resolve off a name is
// reused only on a case-insensitive exact match and otherwise creates a new
// entity.
func (s *MemoryStore) resolveEntityLocked(bankID, name string, kind EntityKind, resolve bool) Entity {
	key := strings.ToLower(strings.TrimSpace(name))
	for id, entity := range s.entities {
		if entity.BankID != bankID {
			continue
		}
		if strings.EqualFold(entity.Name, name) {
			return s.entities[id]
		}
		if resolve && entityKey(entity.Name) == key {
			return s.entities[id]
		}
	}
	entity := Entity{ID: newID(), BankID: bankID, Name: name, Kind: kind, MentionCount: 1}
	if kind == "" {
		entity.Kind = EntityKindRegular
	}
	s.entities[entity.ID] = entity
	return entity
}

// RetainUsageFor derives the token usage for a write that produced unitCount
// memory units.
func RetainUsageFor(unitCount int) Usage {
	input := unitCount
	return Usage{InputTokens: input, OutputTokens: unitCount, TotalTokens: input + unitCount}
}

// ContentHash returns the lowercase hex SHA-256 of the given content.
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Segment splits content into ordered chunks by blank-line boundaries so later
// reads can return each chunk with its index. Empty content yields one chunk.
func Segment(content string) []string {
	trimmed := strings.TrimRight(content, "\n")
	if strings.TrimSpace(trimmed) == "" {
		return []string{content}
	}
	parts := strings.Split(trimmed, "\n\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return []string{content}
	}
	return out
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

func entityKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
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

// ResolveEntity resolves an entity name against the bank.
func (s *MemoryStore) ResolveEntity(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveEntityLocked(bankID, name, kind, resolve), nil
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

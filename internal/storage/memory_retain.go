package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// slow model call must not block every other write to the store. The
	// causal-links switch is read under the lock first, so a concurrent
	// SetTuning cannot race this read.
	s.mu.RLock()
	extractCausalLinks := s.tuning.ExtractCausalLinks
	s.mu.RUnlock()
	extracted, err := extractForItems(ctx, req, extractCausalLinks)
	if err != nil {
		return RetainOutcome{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return RetainOutcome{}, s.writeErr
	}
	// A first write declares the bank, as the postgres backend does. An
	// identifier the create path refuses is refused here too, so a write cannot
	// leave a bank behind under a name the create route would not accept.
	if _, ok := s.banks[req.BankID]; !ok {
		if err := s.createBankLocked(req.BankID); err != nil {
			return RetainOutcome{}, err
		}
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
		units, err := s.writeItemLocked(ctx, req, item, extracted[index], now)
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
	s.recordWriteWatermarkLocked(req.BankID, now)
	if req.SkipOperationRecord {
		// The caller (a queued write's worker) already owns the operation
		// record, so the write leaves the queue alone.
		return outcome, nil
	}
	if req.Async {
		s.operations[opID] = Operation{
			OperationID:   opID,
			BankID:        req.BankID,
			OperationType: OperationTypeRetain,
			Status:        StatusPending,
			Run:           1,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
	} else {
		s.operations[opID] = Operation{
			OperationID:   opID,
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
// extraction order when the item was extracted, and in segmentation order
// otherwise.
func (s *MemoryStore) writeItemLocked(ctx context.Context, req RetainRequest, item RetainItem, facts []ExtractedFact, now time.Time) ([]string, error) {
	docID := item.DocumentID
	if docID == "" {
		docID = newID()
	}
	originalText := item.Content
	baseIndex := 0
	var existingHashes map[string]bool
	// pruneCandidates are the entities the replaced text named; each is dropped
	// after the write if nothing mentions it any more.
	var pruneCandidates []string
	// A re-retain against an explicit document id honours the update mode:
	// replace discards the previous document's units, chunks and links, append
	// concatenates the new text onto the stored document.
	if item.DocumentID != "" {
		// Delta retain: an unchanged document is not re-extracted, so a
		// repeated import is idempotent.
		if !item.ForceReextract && item.UpdateMode != "replace" && item.UpdateMode != "append" {
			if existing, ok := s.documents[item.DocumentID]; ok &&
				existing.BankID == req.BankID && existing.ContentHash == ContentHash(item.Content) {
				return s.unitIDsForDocumentLocked(req.BankID, item.DocumentID), nil
			}
		}
		// The default mode is replace: re-retaining a document id with new
		// content supersedes what the document said, so its units, chunks and
		// links are discarded before the new evidence is written. "append" is
		// the only mode that keeps what is already stored.
		switch item.UpdateMode {
		case "append":
			if existing, ok := s.documents[docID]; ok && existing.OriginalText != "" {
				originalText = joinDocumentParts(existing.OriginalText, item.Content)
				baseIndex, existingHashes = s.documentChunkStateLocked(req.BankID, docID)
			}
		default:
			// The replaced text's entities become prune candidates: one only the
			// old text named has no mention left once the new evidence is
			// written, but the prune waits until then so an entity the new text
			// still names survives as the same identity.
			pruneCandidates = s.clearDocumentUnitsLocked(req.BankID, docID)
		}
	}
	s.putDocumentLocked(req.BankID, docID, originalText, !req.DiscardDocumentText, item, now)
	// An attachment is derived from the document's canonical text and nothing
	// else, so a re-retain that drops an image retires it here.
	s.syncDocumentAttachmentsLocked(req.BankID, docID, item.Content)
	tags := mergeTags(item.Tags, req.DocumentTags)

	chunkBySegment := s.putItemChunksLocked(req.BankID, docID, item.Content, !req.DiscardDocumentText, segmentOptionsFor(req), baseIndex, existingHashes)
	contents := itemContents(req.BankID, docID, chunkBySegment, item, facts, tags, segmentOptionsFor(req), modelExtractionEnabled(req))
	if err := embedContents(ctx, req.Embeddings, contents); err != nil {
		return nil, err
	}
	// The item's mentions are clustered once, before any is scored, so two
	// spellings of one name that appear in this same document become one entity
	// rather than two. The tuning is already read under the write lock.
	canonicalNames := intrabatchCanonicalNames(itemEntityMentions(contents), item.ResolveEntities, s.intrabatchMergeSimilarityLocked())
	units := make([]string, 0, len(contents))
	for _, content := range contents {
		unit := content.unit
		unit.ID = newID()
		s.upsertUnitLocked(unit)
		mentioned := make([]string, 0, len(content.entities))
		seen := map[string]bool{}
		for _, entity := range content.entities {
			resolved := s.resolveEntityLocked(req.BankID, applyIntrabatchCanonical(entity, canonicalNames), item.ResolveEntities)
			if resolved.ID == "" {
				continue
			}
			s.unitEntities[unit.ID] = append(s.unitEntities[unit.ID], resolved.ID)
			// A mention moves the entity's stats: the count is per mention, not
			// per entity, and the last mention stamps last_seen.
			if stored, ok := s.entities[resolved.ID]; ok {
				stored.MentionCount++
				stamp := now
				stored.LastSeen = &stamp
				s.entities[resolved.ID] = stored
			}
			if !seen[resolved.ID] {
				seen[resolved.ID] = true
				mentioned = append(mentioned, resolved.ID)
			}
		}
		// Every pair of entities this unit mentions co-occurs, which is what the
		// entity graph draws and resolution weights. Per unit, so two entities a
		// unit does not share are not neighbours.
		for i := range len(mentioned) {
			for j := i + 1; j < len(mentioned); j++ {
				a, b := mentioned[i], mentioned[j]
				if a > b {
					a, b = b, a
				}
				s.cooccurrences[req.BankID+"\x00"+a+"\x00"+b]++
			}
		}
		units = append(units, unit.ID)
	}
	s.links = append(s.links, linkTuplesFor(req.BankID, units)...)
	s.links = append(s.links, causalLinksFor(req.BankID, units, contents)...)
	// Embedding-similar units are linked semantically, among this item's units
	// and their nearest existing neighbours.
	s.links = append(s.links, s.semanticLinksLocked(req.BankID, units)...)
	if len(pruneCandidates) > 0 {
		s.pruneEntitiesIfOrphanedLocked(req.BankID, pruneCandidates)
	}
	return units, nil
}

// semanticLinksLocked builds semantic links for a freshly written item: its own
// units plus a bounded window of the bank's existing units, so a neighbour set
// is available without scanning the whole bank.
func (s *MemoryStore) semanticLinksLocked(bankID string, unitIDs []string) []MemoryLink {
	candidates := make([]MemoryUnit, 0, len(unitIDs)+semanticNeighbourWindow)
	for _, id := range unitIDs {
		if unit, ok := s.units[id]; ok {
			candidates = append(candidates, unit)
		}
	}
	added := 0
	for _, unit := range s.units {
		if added >= semanticNeighbourWindow {
			break
		}
		if unit.BankID != bankID || len(unit.Embedding) == 0 {
			continue
		}
		candidates = append(candidates, unit)
		added++
	}
	return semanticLinksFor(bankID, candidates, s.tuning.SemanticLinkMinSimilarity)
}

// semanticNeighbourWindow bounds how many existing units a write compares
// against when building semantic links.
const semanticNeighbourWindow = 200

// unitIDsForDocumentLocked returns the ids of a document's stored units in a
// deterministic order, so a delta-skipped retain reports the units already on
// record.
func (s *MemoryStore) unitIDsForDocumentLocked(bankID, docID string) []string {
	ids := make([]string, 0)
	for id, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// clearDocumentUnitsLocked removes a document's units, chunks and links while
// keeping the document row, so a replace-mode re-retain rebuilds the document's
// evidence instead of accumulating it. It returns the entities the removed
// units mentioned — the candidates a later prune re-checks, once the new
// evidence is written and an entity the new text still names has been re-linked.
func (s *MemoryStore) clearDocumentUnitsLocked(bankID, docID string) []string {
	removed := map[string]bool{}
	candidates := map[string]bool{}
	for id, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			for _, entityID := range s.unitEntities[id] {
				candidates[entityID] = true
			}
			delete(s.units, id)
			delete(s.unitEntities, id)
			removed[id] = true
		}
	}
	chunks := s.chunks[:0]
	for _, chunk := range s.chunks {
		if chunk.BankID == bankID && chunk.DocumentID == docID {
			continue
		}
		chunks = append(chunks, chunk)
	}
	s.chunks = chunks
	links := s.links[:0]
	for _, link := range s.links {
		if removed[link.FromUnitID] || removed[link.ToUnitID] {
			continue
		}
		links = append(links, link)
	}
	s.links = links
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	return ids
}

// pruneEntitiesIfOrphanedLocked deletes the named entities that no surviving
// unit mentions, along with the co-occurrence rows that named them.
func (s *MemoryStore) pruneEntitiesIfOrphanedLocked(bankID string, entityIDs []string) {
	pruned := map[string]bool{}
	for _, id := range entityIDs {
		entity, ok := s.entities[id]
		if !ok || entity.BankID != bankID {
			continue
		}
		if entityMentioned(id, s.unitEntities) {
			continue
		}
		delete(s.entities, id)
		pruned[id] = true
	}
	if len(pruned) > 0 {
		for key := range s.cooccurrences {
			if cooccurrenceTouches(key, pruned) {
				delete(s.cooccurrences, key)
			}
		}
	}
}

// putItemChunksLocked stores one item's segments and returns their identifiers
// in order. When storeText is false the raw chunk text is not kept, but the
// content hash is still computed from it so delta dedup is unaffected.
func (s *MemoryStore) putItemChunksLocked(bankID, docID, content string, storeText bool, sizes SegmentOptions, baseIndex int, existing map[string]bool) map[int]string {
	chunks := SegmentWith(content, sizes)
	bySegment := make(map[int]string, len(chunks))
	next := baseIndex
	for i, chunk := range chunks {
		if existing[ContentHash(chunk)] {
			continue
		}
		chunkID := BuildChunkID(bankID, docID, next)
		text := chunk
		if !storeText {
			text = ""
		}
		s.chunks = append(s.chunks, Chunk{
			ChunkID: chunkID, BankID: bankID, DocumentID: docID, ChunkIndex: next, ChunkText: text, ContentHash: ContentHash(chunk),
		})
		bySegment[i] = chunkID
		next++
	}
	return bySegment
}

// documentChunkStateLocked returns a document's stored chunk content hashes and
// how many it holds, so an append skips a tail it already stored and numbers its
// new chunks past the ones present.
func (s *MemoryStore) documentChunkStateLocked(bankID, docID string) (int, map[string]bool) {
	hashes := map[string]bool{}
	count := 0
	for _, chunk := range s.chunks {
		if chunk.BankID == bankID && chunk.DocumentID == docID {
			hashes[chunk.ContentHash] = true
			count++
		}
	}
	return count, hashes
}

// retainParamsFor assembles the retain parameters persisted on a document: the
// strategy and update mode plus the item context, event date, observation
// scopes and metadata, so a document read reports what the write was given.
func retainParamsFor(item RetainItem) map[string]any {
	params := map[string]any{"strategy": item.Strategy, "update_mode": item.UpdateMode}
	if item.Context != "" {
		params["context"] = item.Context
	}
	if item.Timestamp != nil {
		params["event_date"] = item.Timestamp.Format(time.RFC3339)
	}
	if len(item.Metadata) > 0 {
		params["metadata"] = item.Metadata
	}
	if len(item.ObservationScopes) > 0 {
		var scopes any
		if err := json.Unmarshal(item.ObservationScopes, &scopes); err == nil {
			params["observation_scopes"] = scopes
		}
	}
	return params
}

// putDocumentLocked stores a document's text, hash, metadata and parameters.
// When storeText is false the raw text is not kept, but the content hash is
// still computed from it so delta dedup is unaffected.
func (s *MemoryStore) putDocumentLocked(bankID, docID, text string, storeText bool, item RetainItem, now time.Time) {
	tags := item.Tags
	if tags == nil {
		tags = []string{}
	}
	params := retainParamsFor(item)
	stored := Document{
		ID:           docID,
		BankID:       bankID,
		ContentHash:  ContentHash(text),
		Metadata:     item.Metadata,
		RetainParams: params,
		Tags:         tags,
		CreatedAt:    now,
	}
	if storeText {
		stored.OriginalText = text
	}
	s.documents[docID] = stored
}

// upsertUnitLocked stores a memory unit in the in-process store, keeping the
// unit map and the per-bank recall list consistent. Recall reads the per-bank
// list while unit lookups read the map, so a unit written through either path
// must appear in both.
func (s *MemoryStore) upsertUnitLocked(unit MemoryUnit) {
	// The column has a default, so a unit written without a creation time is
	// stamped at insert; the created_at axis of the statistics reads it.
	if unit.CreatedAt == nil {
		now := time.Now().UTC()
		unit.CreatedAt = &now
	}
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
		ID:              unit.ID,
		Text:            unit.Text,
		Type:            string(unit.FactType),
		Context:         unit.Context,
		OccurredStart:   unit.OccurredStart,
		OccurredEnd:     unit.OccurredEnd,
		MentionedAt:     unit.MentionedAt,
		DocumentID:      unit.DocumentID,
		ChunkID:         unit.ChunkID,
		Metadata:        unit.Metadata,
		Tags:            tags,
		Scores:          map[string]float64{},
		SourceMemoryIDs: []string{},
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
	created := Entity{ID: newID(), BankID: bankID, CanonicalName: name, EntityKind: EntityKindRegular, MentionCount: 0}
	s.entities[created.ID] = created
	return created
}

// exactEntityMatchLocked finds a stored entity whose name matches
// case-insensitively.
func (s *MemoryStore) exactEntityMatchLocked(bankID, name string) (Entity, bool) {
	for id, stored := range s.entities {
		if stored.BankID == bankID && strings.EqualFold(entityMatchKey(stored.CanonicalName), name) {
			return s.entities[id], true
		}
	}
	return Entity{}, false
}

// intrabatchMergeSimilarityLocked is the pg_trgm similarity at or above which
// two names in one extraction batch are merged before either is scored. It
// reads the tuning, so the caller must hold the store lock.
func (s *MemoryStore) intrabatchMergeSimilarityLocked() float64 {
	if s.tuning.EntityIntrabatchMerge > 0 {
		return s.tuning.EntityIntrabatchMerge
	}
	return DefaultEntityIntrabatchMergeSimilarity
}

// bestEntityMatchLocked finds the stored entity most similar to name, provided
// it scores at or above the threshold. Ties break on the identifier so the
// resolution is deterministic across runs.
func (s *MemoryStore) bestEntityMatchLocked(bankID, name string) (Entity, bool) {
	best := Entity{}
	bestScore := 0.0
	mergeFloor := s.tuning.EntityMergeMinSimilarity
	if mergeFloor <= 0 {
		mergeFloor = DefaultEntityMergeMinSimilarity
	}
	for id, stored := range s.entities {
		if stored.BankID != bankID {
			continue
		}
		// The merge floor is the reference's second trigram gate: a candidate
		// sharing too few trigrams is dropped before it is scored.
		if !entityMergeFloorPasses(name, stored.CanonicalName, mergeFloor) {
			continue
		}
		score := entitySimilarity(stored.CanonicalName, name)
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

// RetagDocumentMemories carries a document's renamed tags onto its units and
// the observation scopes frozen on them, then removes the observations built
// over them and returns how many were deleted. It mirrors the database-backed
// store: the units' tags and scopes move together and the stale-observation
// sweep that follows requeues the surviving co-sources.
func (s *MemoryStore) RetagDocumentMemories(
	ctx context.Context, bankID, docID string, oldTags, newTags []string,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	srcIDs := make([]string, 0)
	for id, unit := range s.units {
		if unit.BankID != bankID || unit.DocumentID != docID {
			continue
		}
		if rewritten := renamedScopes(unit.ObservationScopes, oldTags, newTags); rewritten != nil {
			unit.ObservationScopes = rewritten
		}
		unit.Tags = append([]string(nil), normalizeTags(newTags)...)
		s.units[id] = unit
		if unit.FactType == TypeWorld || unit.FactType == TypeExperience {
			srcIDs = append(srcIDs, id)
		}
	}
	if len(srcIDs) == 0 {
		return 0, nil
	}
	deleted, err := s.deleteStaleObservationsLocked(bankID, srcIDs)
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		for _, id := range srcIDs {
			if unit, ok := s.units[id]; ok {
				unit.ConsolidatedAt = nil
				s.units[id] = unit
				delete(s.consolidated, id)
			}
		}
	}
	return deleted, nil
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
		stored.EntityKind = kind
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
func (s *MemoryStore) UpsertEntityCooccurrence(ctx context.Context, bankID, entityID1, entityID2 string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// One row per unordered pair, smaller id first, keyed by bank so reads stay
	// bank-scoped — the same shape the reader in EntityGraph parses.
	if entityID1 > entityID2 {
		entityID1, entityID2 = entityID2, entityID1
	}
	key := bankID + "\x00" + entityID1 + "\x00" + entityID2
	s.cooccurrences[key]++
	return nil
}

// UpsertLink persists one directed memory link.
//
// The reference identifies a link by its endpoints and type rather than a
// synthetic row id, so a re-upsert of the same edge replaces it in place.
func (s *MemoryStore) UpsertLink(ctx context.Context, link MemoryLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.links {
		if existing.BankID == link.BankID && existing.FromUnitID == link.FromUnitID &&
			existing.ToUnitID == link.ToUnitID && existing.LinkType == link.LinkType {
			s.links[i] = link
			return nil
		}
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

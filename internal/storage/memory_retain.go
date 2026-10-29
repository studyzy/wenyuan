package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/timefmt"
)

// RetainWrite persists an ordered-item memory write in the in-process store. It
// lazily creates the bank, honors operation-id idempotency, segments content
// into chunks, persists units, resolves entities, and returns created unit
// identifiers grouped per item in item order.
func (s *MemoryStore) RetainWrite(ctx context.Context, req RetainRequest) (RetainOutcome, error) {
	ctx, phases := beginRetainPhases(ctx, memoryStorePhaseLabel)
	defer phases.finish()
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
	} else if !req.SkipOperationRecord {
		// An operation identifier is refused only when this write owns the
		// record it would create. A queued write's worker already owns the
		// child's operation row, so finding that row in the queue is this write
		// finding itself, not a reused identifier.
		if _, exists := s.operations[opID]; exists {
			return RetainOutcome{}, ErrOperationConflict
		}
	}
	now := time.Now().UTC()

	grouped := make([][]string, 0, len(req.Items))
	for index, item := range req.Items {
		// Checkpoint: a queued write aborts between items if its operation was
		// cancelled or its bank (and so its operation row) was deleted. Without
		// it the write would keep recreating the deleted bank through the lazy
		// create above.
		if req.SkipOperationRecord && opID != "" && !s.operationAliveLocked(req.BankID, opID) {
			return RetainOutcome{}, ErrOperationAborted
		}
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
	if !req.Async {
		// A synchronous retain writes no operation row; see the Postgres twin for
		// the reference's own statement of that (api/http.py:10280). The outcome
		// still carries the operation id the response reports.
		return outcome, nil
	}
	s.operations[opID] = Operation{
		OperationID:   opID,
		BankID:        req.BankID,
		OperationType: OperationTypeRetain,
		Status:        StatusPending,
		Run:           1,
		CreatedAt:     now,
		UpdatedAt:     now,
		Payload:       map[string]any{},
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

	chunkBySegment := s.putItemPartsLocked(req.BankID, docID, item.ContentParts(), !req.DiscardDocumentText, segmentOptionsFor(req), baseIndex, existingHashes)
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
			s.incrementEntityMentionLocked(resolved.ID, now)
			if !seen[resolved.ID] {
				seen[resolved.ID] = true
				mentioned = append(mentioned, resolved.ID)
			}
		}
		// Every pair of entities this unit mentions co-occurs, which is what the
		// entity graph draws and resolution weights. Per unit, so two entities a
		// unit does not share are not neighbours.
		s.upsertUnitCooccurrencesLocked(req.BankID, mentioned)
		units = append(units, unit.ID)
	}
	s.links = append(s.links, linkTuplesFor(req.BankID, units)...)
	s.links = append(s.links, causalLinksFor(req.BankID, units, contents)...)
	// Dated units are linked temporally to their nearest same-kind neighbours,
	// then embedding-similar units semantically, among this item's units and
	// their nearest existing neighbours — the reference's retain order.
	s.links = append(s.links, s.temporalLinksLocked(req.BankID, units)...)
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
			s.releaseUnitPostingsLocked(id)
			delete(s.units, id)
			delete(s.unitEntities, id)
			removed[id] = true
		}
	}
	// The survivors that pointed at these units lose their outgoing edges with
	// them, so they are queued for a relink while the links are still readable.
	s.enqueueRelinkVictimsLocked(bankID, keysOf(removed), false)
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

// releaseUnitPostingsLocked gives back the mention count each entity a unit
// names contributed, one decrement per posting, floored at zero. It is the
// memory backend's counterpart to the postgres give-back and must run before
// the unit's postings are dropped, because afterwards the entity ids are gone.
// Expects s.mu held.
func (s *MemoryStore) releaseUnitPostingsLocked(unitID string) {
	seen := make(map[string]struct{}, len(s.unitEntities[unitID]))
	for _, entityID := range s.unitEntities[unitID] {
		if _, dup := seen[entityID]; dup {
			continue
		}
		seen[entityID] = struct{}{}
		s.decrementEntityMentionLocked(entityID)
	}
}

// decrementEntityMentionLocked gives back one mention from an entity, floored
// at zero. Expects s.mu held.
func (s *MemoryStore) decrementEntityMentionLocked(entityID string) {
	entity, ok := s.entities[entityID]
	if !ok {
		return
	}
	if entity.MentionCount > 0 {
		entity.MentionCount--
	}
	s.entities[entityID] = entity
}

// incrementEntityMentionLocked records one new mention on an entity and stamps
// last_seen, the counterpart to the postgres UpsertUnitEntity update. Expects
// s.mu held.
func (s *MemoryStore) incrementEntityMentionLocked(entityID string, now time.Time) {
	entity, ok := s.entities[entityID]
	if !ok {
		return
	}
	entity.MentionCount++
	stamp := now
	entity.LastSeen = &stamp
	s.entities[entityID] = entity
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

// PutDocumentChunks upserts one document's chunks in memory and reports the
// identifier each index resolves to.
//
// An identifier that names an existing row of another bank is refused rather
// than overwritten, which is the same refusal the PostgreSQL backend raises for
// a row whose identifier collides across banks.
func (s *MemoryStore) PutDocumentChunks(ctx context.Context, bankID, documentID string, chunks []DocumentChunk, storeText bool) (map[int]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byIndex := make(map[int]string, len(chunks))
	owned := make([]string, 0)
	for _, chunk := range chunks {
		id := BuildChunkID(bankID, documentID, chunk.ChunkIndex)
		byIndex[chunk.ChunkIndex] = id
		if at := s.chunkAtLocked(id); at >= 0 && s.chunks[at].BankID != bankID {
			owned = append(owned, id)
		}
	}
	if len(owned) > 0 {
		return nil, fmt.Errorf("chunk id(s) %v already belong to another bank", owned)
	}
	for _, chunk := range chunks {
		text := chunk.ChunkText
		if !storeText {
			text = ""
		}
		s.putChunkLocked(Chunk{
			ChunkID:     BuildChunkID(bankID, documentID, chunk.ChunkIndex),
			BankID:      bankID,
			DocumentID:  documentID,
			ChunkIndex:  chunk.ChunkIndex,
			ChunkText:   text,
			ContentHash: ContentHash(chunk.ChunkText),
		})
	}
	return byIndex, nil
}

// chunkAtLocked reports where a chunk identifier's row sits, or -1 when no row
// carries it. The caller holds the lock.
func (s *MemoryStore) chunkAtLocked(chunkID string) int {
	for i := range s.chunks {
		if s.chunks[i].ChunkID == chunkID {
			return i
		}
	}
	return -1
}

// putChunkLocked writes a chunk row, replacing the row with its identifier when
// one exists so a replay overwrites rather than duplicates. The caller holds
// the write lock.
func (s *MemoryStore) putChunkLocked(chunk Chunk) {
	if at := s.chunkAtLocked(chunk.ChunkID); at >= 0 {
		// The write stamps no new creation time, matching the column the
		// PostgreSQL upsert leaves alone.
		chunk.CreatedAt = s.chunks[at].CreatedAt
		s.chunks[at] = chunk
		return
	}
	s.chunks = append(s.chunks, chunk)
}

// putItemPartsLocked stores one item's content blocks — one per content dict the
// caller sent, or the fold of several — starting at baseIndex so a second block
// continues the document's chunk numbering instead of colliding with the first.
// Each block is cut on its own boundaries (see PostgresStore.putItemParts).
func (s *MemoryStore) putItemPartsLocked(bankID, docID string, parts []string, storeText bool, sizes SegmentOptions, baseIndex int, existing map[string]bool) map[int]string {
	bySegment := make(map[int]string)
	next := baseIndex
	for _, content := range parts {
		start := next
		next = s.putPartChunksLocked(bankID, docID, content, storeText, sizes, start, existing, bySegment)
	}
	return bySegment
}

// putPartChunksLocked stores one content block's chunks from next onwards and
// returns the index that follows them.
func (s *MemoryStore) putPartChunksLocked(bankID, docID, content string, storeText bool, sizes SegmentOptions, next int, existing map[string]bool, bySegment map[int]string) int {
	chunks := segmentsPerContent(content, sizes)
	// The block is keyed by the index its first stored chunk is written at,
	// plus how many chunks of it were written before this one, so a position
	// stays tied to the chunk index extraction later names even when a chunk the
	// document already held is skipped.
	start := next
	written := 0
	for _, chunk := range chunks {
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
		bySegment[start+written] = chunkID
		written++
		next++
	}
	return next
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
// replayable settings the write was given, and nothing else, so a document read
// reports the set a reprocess replays. The set matches the reference's, which
// records every content-dict key except its own not-replayed five and any key
// whose value is absent.
func retainParamsFor(item RetainItem) map[string]any {
	// Only the values the caller actually supplied are replayable, and only the
	// replayable ones are recorded. The reference omits a key whose value is
	// absent (None) and never records `update_mode` at all — it is in its own
	// not-replayed set — so an item that names no strategy keeps retain_params
	// without a `strategy` key, and the empty case is an empty object.
	params := map[string]any{}
	if item.Strategy != "" {
		params["strategy"] = item.Strategy
	}
	if item.Context != "" {
		params["context"] = item.Context
	}
	if item.Timestamp != nil {
		// The reference stores the instant in Python's isoformat form, and a
		// document's retain_params are echoed back verbatim on the read surface,
		// so the persisted text is part of the contract.
		params["event_date"] = timefmt.FormatTimestamp(*item.Timestamp)
	}
	// A file upload always carries a metadata dict — the reference fills in
	// `file_meta.metadata or {}`, and an item with none is `{}` rather than
	// absent — so the key is recorded even when empty. An inline retain item
	// that names no metadata then differs from a file upload exactly as the two
	// do in the reference.
	if item.Metadata != nil {
		params["metadata"] = item.Metadata
	}
	if len(item.ObservationScopes) > 0 {
		var scopes any
		if err := json.Unmarshal(item.ObservationScopes, &scopes); err == nil {
			params["observation_scopes"] = scopes
		}
	}
	// The names the caller gave this item's attachments ride the document's
	// parameters, which is what lets a reprocess restate them: the document's
	// record is rewritten by every write, so a name that is not replayed is a
	// name the document loses.
	if len(item.AttachmentFilenames) > 0 {
		params["attachment_filenames"] = item.AttachmentFilenames
	}
	// The reference records the supplied entities and the resolution switch
	// together, and only when entities were supplied: a document retained with
	// no explicit entities carries neither key, so its reprocess falls back to
	// the bank default rather than to a stale list.
	if len(item.Entities) > 0 {
		entities := make([]map[string]any, 0, len(item.Entities))
		for _, entity := range item.Entities {
			entities = append(entities, map[string]any{"text": entity.Text, "type": entity.Kind})
		}
		params["entities"] = entities
		params["resolve_entities"] = item.ResolveEntities
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
		ContextAbsent:   unit.ContextAbsent,
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

// GetDocument returns one stored document by identifier, with the count of the
// memory units written from it (the units whose document_id names it; an
// observation carries no document id) already filled in.
func (s *MemoryStore) GetDocument(ctx context.Context, bankID, docID string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.documents[docID]
	if !ok || doc.BankID != bankID {
		return Document{}, ErrDocumentNotFound
	}
	for _, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			doc.MemoryUnitCount++
		}
	}
	// document_metadata is sourced from retain_params.metadata on every read,
	// the same member the database-backed store derives it from, so a document
	// imported with its parameters carries the same value on either backend.
	doc.Metadata = DocumentMetadata(doc.RetainParams)
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
	// A document has no metadata column in the reference: the value lives inside
	// retain_params.metadata, so a caller-supplied one is merged into that
	// member before the document is stored, exactly as the database-backed
	// write merges it.
	if metadata := DocumentMetadataValue(doc.Metadata); metadata != nil {
		params := copyAnyMap(doc.RetainParams)
		if params == nil {
			params = map[string]any{}
		}
		params["metadata"] = metadata
		doc.RetainParams = params
	}
	now := time.Now().UTC()
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}
	// The documents row carries updated_at on every write: the insert defaults
	// it to the write's clock and the reference's update stamps
	// `updated_at = now()` beside the fields it sets (documents.py:386-400), so
	// an upsert — a retag among them — always re-stamps it.
	doc.UpdatedAt = now
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

// RetagDocumentMemories carries a document's new tags and observation scopes
// onto its units, then removes the observations built over the units whose
// resolved scopes that moves and requeues those units. It mirrors the
// database-backed store: the units' tags and scopes move together, the
// stale-observation sweep that follows requeues the surviving co-sources, and a
// unit whose scopes stay put keeps its observations.
func (s *MemoryStore) RetagDocumentMemories(
	ctx context.Context, bankID, docID string, change DocumentScopeChange,
) (RetagOutcome, error) {
	if err := ctx.Err(); err != nil {
		return RetagOutcome{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return RetagOutcome{}, s.writeErr
	}
	units := make([]DocumentUnitScope, 0)
	for id, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			units = append(units, DocumentUnitScope{
				ID: id, Tags: append([]string(nil), unit.Tags...), Scopes: unit.ObservationScopes,
				FactType: unit.FactType,
			})
		}
	}
	plans := planDocumentRetag(units, change)
	// A tag change stamps every unit the document owns — the reference's blanket
	// write carries `updated_at = now()` beside the tags — and a scope change
	// alone stamps only the units that moved: an in-flight consolidation that
	// read them under the old scopes must see the edit and drop its result
	// (documents.py:486-498).
	now := time.Now().UTC()
	srcIDs := movedSourceUnits(plans)
	moved := make(map[string]bool, len(srcIDs))
	for _, id := range srcIDs {
		moved[id] = true
	}
	for _, plan := range plans {
		unit := s.units[plan.unit.ID]
		if plan.newScopes != nil {
			unit.ObservationScopes = plan.newScopes
		}
		if change.NewTags != nil {
			unit.Tags = plan.finalTags
		}
		if change.NewTags != nil || moved[plan.unit.ID] {
			stamp := now
			unit.UpdatedAt = &stamp
		}
		s.units[plan.unit.ID] = unit
	}
	if len(srcIDs) == 0 {
		return RetagOutcome{}, nil
	}
	// Only the units that moved are requeued — whether or not they had an
	// observation, since a unit consolidated under scopes it no longer writes to
	// must be tried in its new scope — and a co-source of a swept observation
	// that stayed put is left alone.
	removed := s.deleteObservationsOnLocked(bankID, srcIDs)
	return RetagOutcome{
		InvalidatedObservations: len(removed), RequeuedUnits: len(srcIDs), Removed: removed,
	}, nil
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
	s.upsertEntityCooccurrenceLocked(bankID, entityID1, entityID2)
	return nil
}

// UpsertUnitCooccurrences records one co-occurrence for every unordered pair of
// the entities a memory unit mentions. The unit's seen time is not stored: the
// reader derives each pair's last-co-occurrence from the units that witness it,
// so a pair written here carries the same recency an imported unit's own dates
// imply.
func (s *MemoryStore) UpsertUnitCooccurrences(ctx context.Context, bankID string, entityIDs []string, seenAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertUnitCooccurrencesLocked(bankID, entityIDs)
	return nil
}

// upsertUnitCooccurrencesLocked counts the unordered pairs of one unit's entity
// set, which is the shape the entity graph reads. The caller holds the write
// lock and passes the entities de-duplicated by identity.
func (s *MemoryStore) upsertUnitCooccurrencesLocked(bankID string, entityIDs []string) {
	for i := range len(entityIDs) {
		for j := i + 1; j < len(entityIDs); j++ {
			s.upsertEntityCooccurrenceLocked(bankID, entityIDs[i], entityIDs[j])
		}
	}
}

// upsertEntityCooccurrenceLocked increments one unordered pair's counter. One
// row per pair, smaller id first, keyed by bank so reads stay bank-scoped — the
// same shape the reader in EntityGraph parses.
func (s *MemoryStore) upsertEntityCooccurrenceLocked(bankID, entityID1, entityID2 string) {
	if entityID1 > entityID2 {
		entityID1, entityID2 = entityID2, entityID1
	}
	key := bankID + "\x00" + entityID1 + "\x00" + entityID2
	s.cooccurrences[key]++
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

// temporalLinksLocked builds the temporal links a freshly written item gets,
// with the reference's two shapes (`memories/pg/links.py:457-566`): each new
// dated experience/world unit takes its nearest same-fact-type neighbours in
// the bank inside the window, and the batch's own units link to each other
// bidirectionally, nearest successors first. Both are bounded by the per-unit
// cap, and the weight floors at the reference's 0.3.
func (s *MemoryStore) temporalLinksLocked(bankID string, unitIDs []string) []MemoryLink {
	type dated struct {
		id        string
		factType  MemoryUnitType
		eventDate time.Time
	}
	fresh := make([]dated, 0, len(unitIDs))
	freshIDs := make(map[string]bool, len(unitIDs))
	for _, id := range unitIDs {
		unit, ok := s.units[id]
		if !ok || unit.EventDate == nil || !temporalEligibleFactType(unit.FactType) {
			continue
		}
		fresh = append(fresh, dated{id: id, factType: unit.FactType, eventDate: *unit.EventDate})
		freshIDs[id] = true
	}
	if len(fresh) == 0 {
		return nil
	}

	// The batch's units against the rest of the bank: nearest first, same fact
	// type, inside the window, capped per unit.
	existing := make([]dated, 0, len(s.units))
	for id, unit := range s.units {
		if freshIDs[id] || unit.BankID != bankID || unit.EventDate == nil || !temporalEligibleFactType(unit.FactType) {
			continue
		}
		existing = append(existing, dated{id: id, factType: unit.FactType, eventDate: *unit.EventDate})
	}

	out := make([]MemoryLink, 0, len(fresh)*maxTemporalLinksPerUnit)
	link := func(from, to string, hours float64) {
		out = append(out, MemoryLink{
			BankID:     bankID,
			FromUnitID: from,
			ToUnitID:   to,
			LinkType:   LinkTemporal,
			Weight:     temporalLinkWeight(hours),
		})
	}
	for _, unit := range fresh {
		type neighbour struct {
			id    string
			hours float64
		}
		neighbours := make([]neighbour, 0, len(existing))
		for _, other := range existing {
			if other.factType != unit.factType {
				continue
			}
			neighbours = append(neighbours, neighbour{
				id:    other.id,
				hours: math.Abs(other.eventDate.Sub(unit.eventDate).Hours()),
			})
		}
		sort.Slice(neighbours, func(a, b int) bool {
			if neighbours[a].hours != neighbours[b].hours {
				return neighbours[a].hours < neighbours[b].hours
			}
			return neighbours[a].id < neighbours[b].id
		})
		if len(neighbours) > maxTemporalLinksPerUnit {
			neighbours = neighbours[:maxTemporalLinksPerUnit]
		}
		for _, near := range neighbours {
			if near.hours > temporalLinkWindowHours {
				break
			}
			link(unit.id, near.id, near.hours)
		}
	}

	// The batch against itself: every pair inside a fact-type group, both
	// directions, which is the peak the per-unit cap then trims.
	for i, unit := range fresh {
		for _, other := range fresh[i+1:] {
			if other.factType != unit.factType {
				continue
			}
			hours := math.Abs(other.eventDate.Sub(unit.eventDate).Hours())
			if hours > temporalLinkWindowHours {
				continue
			}
			link(unit.id, other.id, hours)
			link(other.id, unit.id, hours)
		}
	}
	return out
}

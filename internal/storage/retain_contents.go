package storage

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// joinDocumentParts is the one definition of how a document's content parts
// become its stored body: two JSON arrays merge element-wise, so a conversation
// stays one array, and anything else is newline-joined.
func joinDocumentParts(existing, incoming string) string {
	if merged, ok := mergeJSONArrayParts(existing, incoming); ok {
		return merged
	}
	return existing + "\n" + incoming
}

// mergeJSONArrayParts concatenates JSON array bodies into one array, preserving
// each element as it was written. It reports false unless every part is a JSON
// array, because an append that is not array-to-array must not reshape the body.
func mergeJSONArrayParts(parts ...string) (string, bool) {
	combined := make([]json.RawMessage, 0)
	for _, part := range parts {
		var array []json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(part)), &array); err != nil {
			return "", false
		}
		combined = append(combined, array...)
	}
	encoded, err := json.Marshal(combined)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// itemContent is one memory unit a write is about to store, with the entity
// mentions that belong to it carried alongside so the caller does not have to
// re-derive them.
//
// The unit's identifier is left empty: each backend assigns it, because the
// memory backend keys its maps by the identifier while the PostgreSQL backend
// lets the insert own it.
type itemContent struct {
	unit     MemoryUnit
	entities []RetainEntity
	// causedByIndexes names the earlier units within the same item that caused
	// this one, by their position in the item's unit list. The position is
	// resolved to a stored unit once identifiers exist, which is why it is
	// carried rather than turned into a link here.
	causedByIndexes []int
}

// itemContents builds the units one item produces, given the bank, the chunk
// identifier stored for each segment the item's text was split into, and the
// tags that apply.
//
// When a model was asked and produced facts, each fact becomes its own unit
// carrying the fact's entities, dates and confidence, anchored to the chunk it
// came from. When no model was configured — or the bank asked for chunks mode,
// which stores text without asking — every stored segment of the item becomes a
// unit. A model that was asked and found nothing produces no unit at all: the
// text is still stored on the document, but the bank remembers nothing of it.
//
// chunkBySegment maps a segment's position in the item to the chunk identifier
// stored for it; a segment absent from the map was not stored (an append whose
// text the document already held) and contributes no unit.
func itemContents(bankID, docID string, chunkBySegment map[int]string, item RetainItem, facts []ExtractedFact, tags []string, sizes SegmentOptions, extractionAttempted bool) []itemContent {
	if len(facts) > 0 {
		return factContents(bankID, docID, chunkBySegment, item, facts, tags)
	}
	if extractionAttempted {
		// The model ran and named nothing: the item yields evidence only when
		// the model says it does, so an empty answer stores no unit.
		return nil
	}
	return verbatimContents(bankID, docID, chunkBySegment, item, tags, sizes)
}

// verbatimContents stores each segment of the item's text as a unit. The
// boundaries are the request's, so a bank that configured a segment size gets
// its own rather than the package default.
func verbatimContents(bankID, docID string, chunkBySegment map[int]string, item RetainItem, tags []string, sizes SegmentOptions) []itemContent {
	chunks := SegmentWith(item.Content, sizes)
	out := make([]itemContent, 0, len(chunks))
	for index, chunk := range chunks {
		chunkID, ok := chunkBySegment[index]
		if !ok {
			// The segment was not stored — a re-sent append turn the document
			// already held — so it contributes no unit.
			continue
		}
		unit := MemoryUnit{
			BankID:        bankID,
			Text:          chunk,
			Context:       item.Context,
			FactType:      TypeWorld,
			Metadata:      item.Metadata,
			DocumentID:    docID,
			ChunkID:       chunkID,
			Tags:          tags,
			AttachmentIDs: item.AttachmentIDs,
			// A verbatim segment inherits the item's observation scoping so the
			// consolidation pass folds it at the scope the caller asked for.
			// The value is stored verbatim because it is raw JSON: the caller
			// may send a mode name ("per_tag") or a list of tag groups, which
			// are different shapes and must survive to the resolving pass.
			ObservationScopes: item.ObservationScopes,
		}
		applyItemDates(&unit, item)
		unit.TextSignals = buildTextSignals(item.Entities, unit.OccurredStart, unit.OccurredEnd)
		out = append(out, itemContent{unit: unit, entities: item.Entities})
	}
	return out
}

// factContents stores one unit per extracted fact, each anchored to the chunk
// of the document its text came from.
func factContents(bankID, docID string, chunkBySegment map[int]string, item RetainItem, facts []ExtractedFact, tags []string) []itemContent {
	out := make([]itemContent, 0, len(facts))
	for _, fact := range facts {
		chunkID, ok := chunkBySegment[fact.ChunkIndex]
		if !ok {
			// The fact came from a segment the document already held (a re-sent
			// append turn), so it is not re-stored.
			continue
		}
		// The item's scoping rides with each fact: extraction stamps it onto
		// the fact so a fact that carries its own scopes is the authority for
		// its unit, and a fact that does not falls back to the item. This is
		// what keeps consolidation from folding across the scopes the caller
		// asked to keep apart. Stored verbatim because the raw JSON may be a
		// mode name or a list of tag groups, which are different shapes.
		scopes := fact.ObservationScopes
		if len(scopes) == 0 {
			scopes = item.ObservationScopes
		}
		unit := MemoryUnit{
			BankID:        bankID,
			Text:          fact.Text,
			Context:       item.Context,
			FactType:      factTypeOrDefault(fact.Type, fact.Kind),
			Metadata:      item.Metadata,
			DocumentID:    docID,
			ChunkID:       chunkID,
			Tags:          appendTagLabels(tags, fact.LabelTags),
			AttachmentIDs: mergeAttachmentIDs(item.AttachmentIDs, factAttachmentIDs(item.AttachmentIDs, fact.FromAttachments)),
			// See above: the scoping the fact folds under survives to storage.
			ObservationScopes: scopes,
		}
		applyItemDates(&unit, item)
		applyFactDates(&unit, fact)
		// A label is stored as an entity named key:value, through the same
		// resolution path as any other entity. Labels are appended after the
		// named entities so a caller reading the unit back sees them last.
		entities := append([]RetainEntity{}, item.Entities...)
		entities = append(entities, fact.Entities...)
		for _, label := range fact.Labels {
			if strings.TrimSpace(label) == "" {
				continue
			}
			entities = append(entities, RetainEntity{Text: label})
		}
		// The unit's searched vector is enriched with the names of exactly the
		// entities it mentions, so they are collected before the signals are built.
		unit.TextSignals = buildTextSignals(entities, unit.OccurredStart, unit.OccurredEnd)
		out = append(out, itemContent{
			unit:            unit,
			entities:        entities,
			causedByIndexes: fact.CausedByIndexes,
		})
	}
	return out
}

// appendTagLabels unions a unit's caller-supplied tags with the label-derived
// tags from one fact, preserving order and dropping repeats.
func appendTagLabels(tags, labelTags []string) []string {
	if len(labelTags) == 0 {
		return tags
	}
	out := make([]string, 0, len(tags)+len(labelTags))
	seen := map[string]bool{}
	for _, tag := range append(append([]string{}, tags...), labelTags...) {
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// factTypeOrDefault maps an extracted fact type onto the stored unit type.
//
// The reference's rule: "assistant" and "experience" both mean the agent did
// something, and "world" means an objective fact. An unexpected value falls
// back to the fact kind before defaulting to world, which is what keeps a model
// that answered with "assistant" under fact_kind from losing the distinction.
func factTypeOrDefault(factType, factKind string) MemoryUnitType {
	switch factType {
	case string(TypeExperience), "assistant":
		return TypeExperience
	case "world":
		return TypeWorld
	default:
		if factKind == "assistant" {
			return TypeExperience
		}
		return TypeWorld
	}
}

// factAttachmentIDs resolves a fact's from_attachments onto the item's
// attachment identifiers.
//
// The model answers with attachment numbers, which index the item's
// attachments from one; an entry that is already an identifier is taken as one,
// so a model that echoed the id instead of the number still links.
func factAttachmentIDs(itemAttachmentIDs []string, fromAttachments []string) []string {
	if len(fromAttachments) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(fromAttachments))
	seen := map[string]bool{}
	for _, entry := range fromAttachments {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id := entry
		if position, err := strconv.Atoi(entry); err == nil {
			if position < 1 || position > len(itemAttachmentIDs) {
				continue
			}
			id = itemAttachmentIDs[position-1]
		} else if !containsString(itemAttachmentIDs, entry) {
			// An id the item does not carry cannot resolve to a stored
			// attachment, so it is dropped rather than linked blindly.
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		resolved = append(resolved, id)
	}
	return resolved
}

// mergeAttachmentIDs unions two attachment-identifier lists, preserving order
// and dropping repeats.
func mergeAttachmentIDs(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, base...), extra...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// buildTextSignals renders the per-unit text_signals the reference folds into
// search_vector: the names of the entities the unit mentions, then the unit's
// spelled-out occurred dates.
//
// Entity names come first so an entity-only query still matches even when the
// prose never repeats the name; the dates follow in the same "Month D YYYY"
// shape the reference writes, so a query for a month or year reaches the unit.
// The end date is added only when it differs from the start, so a point event
// contributes one stamp rather than the same one twice. A unit with neither
// entities nor event dates has no signals, and the caller stores NULL.
func buildTextSignals(entities []RetainEntity, start, end *time.Time) string {
	parts := make([]string, 0, len(entities)+2)
	for _, entity := range entities {
		if entity.Text != "" {
			parts = append(parts, entity.Text)
		}
	}
	if start != nil {
		parts = append(parts, textSignalDate(*start))
	}
	if end != nil && (start == nil || !end.Equal(*start)) {
		parts = append(parts, textSignalDate(*end))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// textSignalDate formats an event date the way the reference spells it into
// text_signals: the full month name, the day without a leading zero, and the
// four-digit year ("October 5 2026").
func textSignalDate(stamp time.Time) string {
	return strings.ReplaceAll(stamp.Format("January 02 2006"), " 0", " ")
}

// applyItemDates sets the unit's ingest clock from the item's timestamp, and
// its event date as a fallback for a fact that carries none.
//
// An item that supplied an explicit timestamp uses it for both; an item that
// omitted one defaults to the write time; an item that asked for no timestamp
// ("unset") carries no date at all. `occurred_start`/`occurred_end` are never
// taken from the item: they are the extracted fact's account of when the event
// happened, and an item that stated no event must not be dated to ingest
// (which is why `occurred_start` is None for an undated fact while
// `mentioned_at` is always set).
func applyItemDates(unit *MemoryUnit, item RetainItem) {
	switch {
	case item.Timestamp != nil:
		unit.EventDate = item.Timestamp
		unit.MentionedAt = item.Timestamp
	case item.HasTimestamp:
		// Timestamped as "none": the item carries no date by request.
	default:
		now := time.Now().UTC()
		unit.EventDate = &now
		unit.MentionedAt = &now
	}
}

// applyFactDates overrides the unit's dates with the fact's own, when the fact
// carried them. An extracted date is more specific than the item's, so it wins.
func applyFactDates(unit *MemoryUnit, fact ExtractedFact) {
	if start := parseFactTime(fact.OccurredStart); start != nil {
		unit.EventDate = start
		unit.OccurredStart = start
	}
	if end := parseFactTime(fact.OccurredEnd); end != nil {
		unit.OccurredEnd = end
	}
}

// parseFactTime reads a fact timestamp, returning nil for an absent or
// unparseable value rather than failing the write over a model's bad format.
func parseFactTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

// causalLinksFor returns the caused_by links joining an item's units to the
// earlier units that caused them.
//
// The link points from the effect to its cause, which is the direction the
// contract fixed: a fact records what caused it. An index outside the item's
// unit list is dropped, because it names a unit this write did not store.
func causalLinksFor(bankID string, unitIDs []string, contents []itemContent) []MemoryLink {
	links := make([]MemoryLink, 0)
	for index, content := range contents {
		if index >= len(unitIDs) {
			break
		}
		for _, target := range content.causedByIndexes {
			if target < 0 || target >= len(unitIDs) || target == index {
				continue
			}
			links = append(links, MemoryLink{
				BankID:     bankID,
				FromUnitID: unitIDs[index],
				ToUnitID:   unitIDs[target],
				LinkType:   LinkCausedBy,
				Weight:     1,
			})
		}
	}
	return links
}

// linkTuplesFor returns the temporal links joining consecutive units one item
// produced, so a backend can persist them in its own way. The units come from
// one document and are usually close in time, so the link type is temporal;
// embedding-similar pairs get their own semantic links.
func linkTuplesFor(bankID string, unitIDs []string) []MemoryLink {
	links := make([]MemoryLink, 0, maxInt(len(unitIDs)-1, 0))
	for i := 1; i < len(unitIDs); i++ {
		links = append(links, MemoryLink{
			BankID:     bankID,
			FromUnitID: unitIDs[i-1],
			ToUnitID:   unitIDs[i],
			LinkType:   LinkTemporal,
			Weight:     1,
		})
	}
	return links
}

// SemanticLinkMinSimilarity is the cosine floor above which two units are
// linked as semantically related. It mirrors the reference's semantic link
// threshold.
const SemanticLinkMinSimilarity = 0.75

// semanticLinksFor returns the semantic links joining each unit to the units
// whose embedding is within the similarity floor, capped at semanticLinkFanOut
// per unit.
func semanticLinksFor(bankID string, units []MemoryUnit) []MemoryLink {
	const fanOut = 5
	links := make([]MemoryLink, 0)
	for i := range units {
		if len(units[i].Embedding) == 0 {
			continue
		}
		type scored struct {
			id         string
			similarity float64
		}
		neighbours := make([]scored, 0)
		for j := range units {
			if i == j || len(units[j].Embedding) == 0 {
				continue
			}
			similarity := cosineBetween(units[i].Embedding, units[j].Embedding)
			if similarity >= SemanticLinkMinSimilarity {
				neighbours = append(neighbours, scored{id: units[j].ID, similarity: similarity})
			}
		}
		sort.SliceStable(neighbours, func(a, b int) bool {
			if neighbours[a].similarity != neighbours[b].similarity {
				return neighbours[a].similarity > neighbours[b].similarity
			}
			return neighbours[a].id < neighbours[b].id
		})
		if len(neighbours) > fanOut {
			neighbours = neighbours[:fanOut]
		}
		for _, neighbour := range neighbours {
			links = append(links, MemoryLink{
				BankID:     bankID,
				FromUnitID: units[i].ID,
				ToUnitID:   neighbour.id,
				LinkType:   LinkSemantic,
				Weight:     neighbour.similarity,
			})
		}
	}
	return links
}

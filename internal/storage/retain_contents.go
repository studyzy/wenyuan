package storage

import "time"

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

// itemContents builds the units one item produces, given the bank, the
// identifiers of the chunks the item's text was split into, and the tags that
// apply.
//
// When extraction produced facts, each fact becomes its own unit carrying the
// fact's entities, dates and confidence. When it did not — because no model is
// configured, or the model found nothing — every segment of the item is stored
// verbatim, which is the behaviour the service has always had.
func itemContents(bankID, docID string, chunkIDs []string, item RetainItem, facts []ExtractedFact, tags []string) []itemContent {
	if len(facts) > 0 {
		return factContents(bankID, docID, chunkIDs, item, facts, tags)
	}
	return verbatimContents(bankID, docID, item, tags)
}

// verbatimContents stores each segment of the item's text as a unit.
func verbatimContents(bankID, docID string, item RetainItem, tags []string) []itemContent {
	chunks := Segment(item.Content)
	out := make([]itemContent, 0, len(chunks))
	for index, chunk := range chunks {
		_ = index
		unit := MemoryUnit{
			BankID:        bankID,
			Text:          chunk,
			Type:          TypeWorld,
			Metadata:      item.Metadata,
			DocumentID:    docID,
			Tags:          tags,
			AttachmentIDs: item.AttachmentIDs,
		}
		applyItemDates(&unit, item)
		out = append(out, itemContent{unit: unit, entities: item.Entities})
	}
	return out
}

// factContents stores one unit per extracted fact, each anchored to the chunk
// of the document its text came from.
func factContents(bankID, docID string, chunkIDs []string, item RetainItem, facts []ExtractedFact, tags []string) []itemContent {
	out := make([]itemContent, 0, len(facts))
	for _, fact := range facts {
		unit := MemoryUnit{
			BankID:        bankID,
			Text:          fact.Text,
			Type:          TypeWorld,
			Confidence:    fact.Confidence,
			Metadata:      item.Metadata,
			DocumentID:    docID,
			ChunkID:       anchorChunkID(chunkIDs),
			Tags:          tags,
			AttachmentIDs: item.AttachmentIDs,
		}
		applyItemDates(&unit, item)
		applyFactDates(&unit, fact)
		out = append(out, itemContent{
			unit:            unit,
			entities:        append(append([]RetainEntity{}, item.Entities...), fact.Entities...),
			causedByIndexes: fact.CausedByIndexes,
		})
	}
	return out
}

// applyItemDates sets the unit's dates from the item's timestamp. An item that
// supplied an explicit timestamp uses it; an item that omitted one defaults to
// the write time; an item that asked for no timestamp (null or the unset
// sentinel) leaves the dates unset, which is what distinguishes it from an
// omission.
func applyItemDates(unit *MemoryUnit, item RetainItem) {
	switch {
	case item.Timestamp != nil:
		unit.EventDate = item.Timestamp
		unit.OccurredStart = item.Timestamp
	case item.HasTimestamp:
		// Timestamped as "none": the item carries no date by request.
	default:
		now := time.Now().UTC()
		unit.EventDate = &now
		unit.OccurredStart = &now
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

// anchorChunkID names the chunk a fact is anchored to. A fact is extracted from
// the item as a whole rather than from a named segment, so it is anchored to
// the item's first chunk; that is what makes the fact reachable from the
// document it was extracted from.
func anchorChunkID(chunkIDs []string) string {
	if len(chunkIDs) == 0 {
		return ""
	}
	return chunkIDs[0]
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
				ID:       newID(),
				BankID:   bankID,
				FromID:   unitIDs[index],
				ToID:     unitIDs[target],
				LinkType: LinkCausedBy,
			})
		}
	}
	return links
}

// linkTuplesFor returns the semantic links joining consecutive units one item
// produced, so a backend can persist them in its own way.
func linkTuplesFor(bankID string, unitIDs []string) []MemoryLink {
	links := make([]MemoryLink, 0, maxInt(len(unitIDs)-1, 0))
	for i := 1; i < len(unitIDs); i++ {
		links = append(links, MemoryLink{
			ID:       newID(),
			BankID:   bankID,
			FromID:   unitIDs[i-1],
			ToID:     unitIDs[i],
			LinkType: LinkSemantic,
		})
	}
	return links
}

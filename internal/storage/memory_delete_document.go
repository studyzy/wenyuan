package storage

import (
	"context"
	"strings"
)

// DeleteDocument removes a document and the units extracted from it, returning
// the number of memory units removed.
func (s *MemoryStore) DeleteDocument(ctx context.Context, bankID, docID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// An observation resting on this document's facts loses a source when they
	// go, so it is swept first. The ids are read under the lock, released, and
	// the sweep re-locks, because the sweep takes the same lock this method
	// holds for the rest of its body.
	s.mu.Lock()
	doomed := make([]string, 0)
	for id, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			doomed = append(doomed, id)
		}
	}
	s.mu.Unlock()
	if len(doomed) > 0 {
		if _, err := s.DeleteStaleObservations(ctx, bankID, doomed); err != nil {
			return 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	if _, ok := s.documents[docID]; !ok {
		return 0, ErrDocumentNotFound
	}
	delete(s.documents, docID)

	// Units whose source is gone would otherwise stay retrievable.
	removed := map[string]bool{}
	for id, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			delete(s.units, id)
			delete(s.unitEntities, id)
			removed[id] = true
		}
	}
	// Chunks and links that point at the removed units go with them.
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
	// The document's own attachment rows go with it: a shared file's bytes may
	// outlive this document, but the row naming THIS document does not.
	for key, attachment := range s.attachments {
		if attachment.BankID == bankID && attachment.DocumentID == docID {
			delete(s.attachments, key)
		}
	}
	// An entity only this document ever named now has no mention left, so it is
	// pruned with the units that carried it, and the co-occurrence edges that
	// named it go too.
	pruned := map[string]bool{}
	for id, entity := range s.entities {
		if entity.BankID != bankID {
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
	return len(removed), nil
}

// entityMentioned reports whether any surviving unit still mentions the entity.
func entityMentioned(entityID string, unitEntities map[string][]string) bool {
	for _, ids := range unitEntities {
		if containsString(ids, entityID) {
			return true
		}
	}
	return false
}

// cooccurrenceTouches reports whether a co-occurrence key names a pruned entity.
// The key is bankID NUL entityID1 NUL entityID2, the shape the writer builds.
func cooccurrenceTouches(key string, pruned map[string]bool) bool {
	parts := strings.Split(key, "\x00")
	if len(parts) != 3 {
		return false
	}
	return pruned[parts[1]] || pruned[parts[2]]
}

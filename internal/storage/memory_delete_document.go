package storage

import "context"

// DeleteDocument removes a document and the units extracted from it.
func (s *MemoryStore) DeleteDocument(ctx context.Context, bankID, docID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if _, ok := s.documents[docID]; !ok {
		return ErrDocumentNotFound
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
		if removed[link.FromID] || removed[link.ToID] {
			continue
		}
		links = append(links, link)
	}
	s.links = links
	return nil
}

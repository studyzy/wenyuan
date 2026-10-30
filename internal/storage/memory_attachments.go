package storage

import (
	"context"
	"sort"
)

// attachmentKey identifies one document's copy of an attachment.
func attachmentKey(bankID, documentID, hash string) string {
	return bankID + "\x00" + documentID + "\x00" + hash
}

// PutAttachment records one document's copy of an attachment in the in-memory
// backend. The write is idempotent on the document plus the content hash.
func (s *MemoryStore) PutAttachment(ctx context.Context, attachment Attachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	key := attachmentKey(attachment.BankID, attachment.DocumentID, attachment.AttachmentHash)
	if _, exists := s.attachments[key]; exists {
		return nil
	}
	s.attachments[key] = attachment
	return nil
}

// ListAttachments returns the attachments a document carries, ordered by
// content hash so provenance resolves deterministically.
func (s *MemoryStore) ListAttachments(ctx context.Context, bankID, documentID string) ([]Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	var attachments []Attachment
	for _, attachment := range s.attachments {
		if attachment.BankID == bankID && attachment.DocumentID == documentID {
			attachments = append(attachments, attachment)
		}
	}
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].AttachmentHash < attachments[j].AttachmentHash
	})
	return attachments, nil
}

// ChunkAttachments returns each chunk's attachments, keyed by chunk id: the
// attachments the chunk's own text references, in first-appearance order.
//
// Provenance is the chunk text, not the units drawn from it: a chunk's text is
// where the inline placeholder stands, so an attachment is listed on the chunk
// that carries it whether or not any fact was extracted from that part of the
// text (and never on a sibling chunk that does not).
func (s *MemoryStore) ChunkAttachments(ctx context.Context, bankID, documentID string) (map[string][]Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	byShortID := map[string]Attachment{}
	for _, attachment := range s.attachments {
		if attachment.BankID == bankID && attachment.DocumentID == documentID {
			byShortID[attachment.ShortID] = attachment
		}
	}
	chunks := make([]Chunk, 0)
	for _, chunk := range s.chunks {
		if chunk.BankID == bankID && chunk.DocumentID == documentID {
			chunks = append(chunks, chunk)
		}
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})
	byChunk := map[string][]Attachment{}
	for _, chunk := range chunks {
		seen := map[string]bool{}
		for _, shortID := range referencedAttachmentIDs(chunk.ChunkText) {
			if seen[shortID] {
				continue
			}
			seen[shortID] = true
			if attachment, ok := byShortID[shortID]; ok {
				byChunk[chunk.ChunkID] = append(byChunk[chunk.ChunkID], attachment)
			}
		}
	}
	return byChunk, nil
}

// GetAttachment returns one attachment by its short id.
//
// The short id is what a placeholder token and an attachment URL carry, so it is
// the only handle a reader has; the row is where the storage key lives, which is
// what the bytes are actually read by.
func (s *MemoryStore) GetAttachment(ctx context.Context, bankID, attachmentID string) (Attachment, error) {
	if err := ctx.Err(); err != nil {
		return Attachment{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return Attachment{}, s.readErr
	}
	for _, attachment := range s.attachments {
		if attachment.BankID == bankID && attachment.ShortID == attachmentID {
			return attachment, nil
		}
	}
	return Attachment{}, ErrAttachmentNotFound
}

// SyncDocumentAttachments drops a document's attachment rows whose short id the
// document's current text no longer references, mirroring the PostgreSQL backend.
func (s *MemoryStore) SyncDocumentAttachments(ctx context.Context, bankID, documentID, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncDocumentAttachmentsLocked(bankID, documentID, text)
	return nil
}

// syncDocumentAttachmentsLocked drops a document's attachment rows whose short
// id its text no longer references. The caller holds the write lock.
// KindUploadedFile marks the attachment row that records a document's uploaded
// source file, as opposed to content embedded inside the document. The row is
// what the attachment route resolves when a caller downloads the original, so a
// re-retain must not sweep it away the way an unreferenced inline attachment is
// swept.
const KindUploadedFile = "file"

func (s *MemoryStore) syncDocumentAttachmentsLocked(bankID, documentID, text string) {
	keep := map[string]bool{}
	for _, id := range referencedAttachmentIDs(text) {
		keep[id] = true
	}
	for key, attachment := range s.attachments {
		if attachment.BankID != bankID || attachment.DocumentID != documentID {
			continue
		}
		// Only the attachments an inline placeholder names are managed here: a
		// converted upload's own row has no placeholder in the text (it is the
		// document's source file, not content inside it) and would otherwise be
		// swept away by an unrelated re-retain.
		if attachment.Kind == KindUploadedFile {
			continue
		}
		if !keep[attachment.ShortID] {
			delete(s.attachments, key)
		}
	}
}

// DeleteDocumentAttachments removes one document's attachment rows.
func (s *MemoryStore) DeleteDocumentAttachments(ctx context.Context, bankID, documentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, attachment := range s.attachments {
		if attachment.BankID == bankID && attachment.DocumentID == documentID {
			delete(s.attachments, key)
		}
	}
	return nil
}

// RemoveAttachmentRows drops the named attachment rows of one document, which is
// what a refused retain's recall does with the rows it stored at ingress
// (engine/memory_engine.py:8440-8455). Only the short ids named are removed, so
// an attachment the document already carried survives.
func (s *MemoryStore) RemoveAttachmentRows(ctx context.Context, bankID, documentID string, shortIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(shortIDs) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, id := range shortIDs {
		wanted[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, attachment := range s.attachments {
		if attachment.BankID != bankID || attachment.DocumentID != documentID {
			continue
		}
		if wanted[attachment.ShortID] {
			delete(s.attachments, key)
		}
	}
	return nil
}

// DeleteBankAttachments removes every attachment row a bank holds.
func (s *MemoryStore) DeleteBankAttachments(ctx context.Context, bankID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, attachment := range s.attachments {
		if attachment.BankID == bankID {
			delete(s.attachments, key)
		}
	}
	return nil
}

// AttachmentsByShortIDs resolves short attachment ids to a bank's attachment
// rows, keyed by short id, mirroring the PostgreSQL backend.
func (s *MemoryStore) AttachmentsByShortIDs(ctx context.Context, bankID string, ids []string) (map[string]Attachment, error) {
	resolved := map[string]Attachment{}
	if len(ids) == 0 {
		return resolved, nil
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	for _, attachment := range s.attachments {
		if attachment.BankID != bankID || !wanted[attachment.ShortID] {
			continue
		}
		if _, seen := resolved[attachment.ShortID]; seen {
			continue
		}
		resolved[attachment.ShortID] = attachment
	}
	return resolved, nil
}

// enrichRecallAttachments fills each match's attachment side values from the
// units it names, mirroring the PostgreSQL backend. An observation with no
// attachment ids of its own inherits its source facts'. The caller holds the
// read lock, as with the other side channels.
func (s *MemoryStore) enrichRecallAttachments(bankID string, matches []RecallMatch) {
	for index := range matches {
		ids := s.units[matches[index].ID].AttachmentIDs
		if len(ids) == 0 {
			ids = s.sourceAttachmentIDs(matches[index].ID)
		}
		if len(ids) == 0 {
			continue
		}
		attachments := make([]RecallAttachment, 0, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			var found *Attachment
			for key := range s.attachments {
				candidate := s.attachments[key]
				if candidate.BankID == bankID && candidate.ShortID == id {
					found = &candidate
					break
				}
			}
			kind, hash, mediaType, filename := "", "", "", ""
			var byteSize int64
			if found != nil {
				kind, hash, mediaType, filename, byteSize = found.Kind, found.AttachmentHash, found.MediaType, found.Filename, found.ByteSize
			}
			attachments = append(attachments, recallAttachment(bankID, id, kind, hash, mediaType, byteSize, filename))
		}
		matches[index].Attachments = attachments
	}
}

// sourceAttachmentIDs unions the attachment ids of a unit's source facts, in
// source order. Caller holds the read lock.
func (s *MemoryStore) sourceAttachmentIDs(unitID string) []string {
	var out []string
	seen := map[string]bool{}
	for _, sourceID := range s.units[unitID].SourceMemoryIDs {
		for _, id := range s.units[sourceID].AttachmentIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

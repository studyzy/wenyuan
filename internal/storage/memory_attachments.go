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

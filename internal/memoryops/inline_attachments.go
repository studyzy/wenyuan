package memoryops

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// toStorageAttachments maps the decoded inline attachments onto the storage
// shape the extractor reads.
func toStorageAttachments(attachments []RetainAttachment) []storage.RetainAttachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]storage.RetainAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, storage.RetainAttachment{
			ShortID:   attachment.ShortID,
			MediaType: attachment.MediaType,
			Kind:      attachment.Kind,
			Data:      attachment.Data,
		})
	}
	return out
}

// storeInlineAttachments decodes and persists the inline image blocks a retain
// carries, before the write is submitted.
//
// The bytes are content-addressed and written per document, so re-retaining the
// same document rewrites the same key and a later failure leaves a blob the next
// retain reuses. Each item's placeholder text is what reaches extraction; the
// decoded bytes ride alongside on the item so the extractor can put the image
// into the model prompt where its placeholder stands.
//
// A document id the caller did not name is minted here rather than later, because
// an attachment row keys on the document that owns it and the id must be decided
// before the bytes are stored.
func (h *Handlers) storeInlineAttachments(ctx context.Context, bankID string, req *RetainRequest) error {
	for index := range req.Items {
		item := &req.Items[index]
		inline := item.Content.InlineAttachments()
		if len(inline) == 0 {
			continue
		}
		// The bank row is created first because the attachment table references
		// it; the retain path lazily creates the same bank moments later.
		if err := h.Store.EnsureBank(ctx, bankID); err != nil {
			return err
		}
		documentID := item.DocumentID
		if documentID == "" {
			documentID = storage.NewDocumentID()
		}
		ids := make([]string, 0, len(inline))
		attachments := make([]RetainAttachment, 0, len(inline))
		for _, attachment := range inline {
			hash := attachmentHashOf(attachment.Data)
			key := storage.AttachmentStorageKey(bankID, documentID, hash)
			if err := h.Store.PutStoredFile(ctx, storage.StoredFile{
				Key: key, ContentType: attachment.MediaType, Data: attachment.Data,
			}); err != nil {
				return err
			}
			if err := h.Store.PutAttachment(ctx, storage.Attachment{
				BankID:         bankID,
				DocumentID:     documentID,
				AttachmentHash: hash,
				ShortID:        attachment.ShortID,
				MediaType:      attachment.MediaType,
				ByteSize:       int64(len(attachment.Data)),
				StorageKey:     key,
				Kind:           attachment.Kind,
			}); err != nil {
				return err
			}
			ids = append(ids, attachment.ShortID)
			attachments = append(attachments, RetainAttachment{
				ShortID:   attachment.ShortID,
				MediaType: attachment.MediaType,
				Kind:      attachment.Kind,
				Data:      attachment.Data,
			})
		}
		item.DocumentID = documentID
		item.AttachmentIDs = ids
		item.Attachments = attachments
	}
	return nil
}

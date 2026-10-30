package memoryops

import (
	"context"
	"log/slog"

	"github.com/studyzy/wenyuan/internal/storage"
)

// retainDocumentIDPrefix is what the reference puts in front of the uuid it mints
// for a retain item that carries attachments but named no document
// (api/http.py:10346-10349: `f"retain_{uuid.uuid4()}"`).
const retainDocumentIDPrefix = "retain_"

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
//
// It returns the short ids this call actually wrote, per document — the record a
// refusal below has to take back out, as opposed to what the document already
// held (engine/memory_engine.py:8966-8970).
func (h *Handlers) storeInlineAttachments(ctx context.Context, bankID string, req *RetainRequest) (map[string][]string, error) {
	written := map[string][]string{}
	for index := range req.Items {
		item := &req.Items[index]
		inline := item.Content.InlineAttachments()
		if len(inline) == 0 {
			continue
		}
		if !h.VisionEnabled {
			// The model cannot read images, so the canonicalizer drops the
			// blocks and the item keeps only its prose. Storing bytes no write
			// could ever resolve would leave an orphan attachment row behind.
			item.attachmentsDeclined = true
			continue
		}
		// The bank row is created first because the attachment table references
		// it; the retain path lazily creates the same bank moments later.
		if err := h.Store.EnsureBank(ctx, bankID); err != nil {
			return nil, err
		}
		documentID := item.DocumentID
		if documentID == "" {
			// The reference mints this id at ingress as `retain_<uuid4>`
			// (api/http.py:10346-10349), and it is the id the document, its
			// attachment rows and the retain.completed event carry from then on.
			documentID = retainDocumentIDPrefix + storage.NewDocumentID()
		}
		ids := make([]string, 0, len(inline))
		attachments := make([]RetainAttachment, 0, len(inline))
		// The names the caller gave the item's attachments ride the document's
		// replayable parameters, so a reprocess restates them instead of losing
		// them to the rewrite.
		filenames := map[string]string{}
		for _, attachment := range inline {
			hash := attachmentHashOf(attachment.Data)
			key := storage.AttachmentStorageKey(bankID, documentID, hash)
			if err := h.Store.PutStoredFile(ctx, storage.StoredFile{
				Key: key, ContentType: attachment.MediaType, Data: attachment.Data,
			}); err != nil {
				return nil, err
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
				Filename:       attachment.Filename,
			}); err != nil {
				return nil, err
			}
			ids = append(ids, attachment.ShortID)
			if attachment.Filename != "" {
				filenames[attachment.ShortID] = attachment.Filename
			}
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
		if len(filenames) > 0 {
			item.AttachmentFilenames = filenames
		}
		written[documentID] = append(written[documentID], ids...)
	}
	return written, nil
}

// attachmentRowRemover is the optional store capability a refused retain's
// recall needs: drop named attachment rows from one document. Both backends
// have it, but it is not part of the Store interface because no route any
// other handler serves needs it.
type attachmentRowRemover interface {
	RemoveAttachmentRows(ctx context.Context, bankID, documentID string, shortIDs []string) error
}

// discardIngressAttachments takes back the attachments a refused write stored at
// ingress — the recall of the reference's `_discard_unreferenced_attachments`
// (engine/memory_engine.py:8417-8462).
//
// Deliberately scoped to what the ingress actually wrote, per document, and to
// the documents of the refused call: a retain that adds a screenshot to a
// document that already carries one must not, on refusal, take away the one the
// document already had, and a refusal must not reach a document the request did
// not name.
//
// A failure to reclaim is logged rather than answered: the refusal is the
// caller's answer, and a blob left behind is wasted bytes, not a wrong result.
func (h *Handlers) discardIngressAttachments(ctx context.Context, bankID string, written map[string][]string, req RetainRequest) {
	remover, ok := h.Store.(attachmentRowRemover)
	if !ok {
		return
	}
	refused := map[string]bool{}
	for _, item := range req.Items {
		if item.DocumentID != "" {
			refused[item.DocumentID] = true
		}
	}
	for documentID, ids := range written {
		if !refused[documentID] || len(ids) == 0 {
			continue
		}
		// The keys are read before the rows go: the row is the only record of
		// where its bytes live.
		keys := make([]string, 0, len(ids))
		for _, shortID := range ids {
			attachment, err := h.Store.GetAttachment(ctx, bankID, shortID)
			if err == nil {
				keys = append(keys, attachment.StorageKey)
			}
		}
		if err := remover.RemoveAttachmentRows(ctx, bankID, documentID, ids); err != nil {
			slog.Default().Warn("could not take back the attachments of a refused retain",
				"bank_id", bankID, "document_id", documentID, "error", err)
			continue
		}
		for _, key := range keys {
			if key == "" {
				continue
			}
			if err := h.Store.DeleteStoredFile(ctx, key); err != nil {
				slog.Default().Warn("could not delete the blob of a refused retain",
					"bank_id", bankID, "storage_key", key, "error", err)
			}
		}
	}
}

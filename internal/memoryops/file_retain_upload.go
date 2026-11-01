package memoryops

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/fileparse"
	"github.com/studyzy/wenyuan/internal/storage"
)

// This file holds the parts of the file-retain route that run after the
// request has been parsed: overlaying the per-file metadata onto the retain
// item, reading one uploaded part, and the upload size ceiling with the
// message a refusal reports.

// pythonStringList renders a string list the way the reference's f-string does,
// so the refusal text carries the same names and quoting.
func pythonStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// applyFileMetadata overlays per-file metadata onto the retain item.
func applyFileMetadata(item *RetainItem, meta FileRetainMeta) {
	if meta.DocumentID != "" {
		item.DocumentID = meta.DocumentID
	}
	if meta.Context != "" {
		item.Context = meta.Context
	}
	if meta.Metadata != nil {
		item.Metadata = meta.Metadata
	}
	if len(meta.Tags) > 0 {
		item.Tags = TagList(meta.Tags)
	}
	if meta.Strategy != "" {
		item.Strategy = meta.Strategy
	}
	if meta.Timestamp != "" {
		// The reference takes the value as it validated it — `datetime.fromisoformat`,
		// which admits a date-only or naive datetime — and carries it into the
		// document's event_date unchanged (api/http.py FileRetainMetadata →
		// memory_engine.py:23625-23632 → :4556-4560). Parsing with the strict
		// RFC 3339 form would drop a date-only value the request layer already
		// accepted, and silently date the document by its ingest time.
		if parsed, err := parseISODate(meta.Timestamp); err == nil {
			item.Timestamp = TimestampField{Set: true, Value: &parsed}
		}
	}
}

// itemFromUpload reads one uploaded file, converts it to markdown through the
// requested parser chain, records the original bytes content-addressed under
// the owning bank and document, and returns the retain item that carries the
// converted text. The write is idempotent on the content hash, so re-uploading
// an unchanged file rewrites nothing observable. A retain that later fails
// leaves a blob cached by key, which the next upload of the same content
// reuses rather than duplicating.
func (h *Handlers) itemFromUpload(r *http.Request, bankID, documentID string, header *multipart.FileHeader, parserChain []string) (RetainItem, error) {
	// The reference bounds only the whole batch on this route (the conversion
	// batch size and byte total, enforced by the caller); it places no per-file
	// size cap, so a single large upload is accepted as long as the batch bound
	// holds.
	file, err := header.Open()
	if err != nil {
		return RetainItem{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return RetainItem{}, err
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	image := isImageUpload(header.Filename, contentType)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	shortID := hash[:attachmentShortIDLength]
	// An image is stored as bytes and referenced from the retained text by its
	// placeholder; anything else is converted to markdown by the parser chain.
	// A format no parser can convert is refused with a 4xx naming the format,
	// rather than storing binary noise as memory text.
	var text string
	if image {
		text = AttachmentPlaceholder(shortID)
	} else {
		registry, err := h.parserRegistry()
		if err != nil {
			return RetainItem{}, err
		}
		result, err := registry.ConvertWithFallback(parserChain, data, header.Filename, contentType)
		if err != nil {
			return RetainItem{}, unsupportedUploadError{
				filename: header.Filename,
				format:   fileparse.FormatName(header.Filename),
			}
		}
		text = result.Content
	}

	key := storage.AttachmentStorageKey(bankID, documentID, hash)
	// The reference deletes the original file from file storage once it has been
	// converted and retained (WENYUAN_API_FILE_DELETE_AFTER_RETAIN), keeping
	// only the retained text; with the setting on this build never writes the
	// bytes in the first place, which reaches the same state without a
	// store-then-delete round trip. An image's bytes are its content, so they
	// are always kept.
	if !h.DeleteAfterRetain || image {
		if err := h.Store.PutStoredFile(r.Context(), storage.StoredFile{
			Key: key, ContentType: contentType, Data: data,
		}); err != nil {
			return RetainItem{}, err
		}
	}
	if image && !h.VisionEnabled {
		// The model cannot read images, so the canonical text is the prose the
		// picture stood beside — none here — and no attachment row is written:
		// bytes no write could ever resolve would be an orphan.
		return RetainItem{
			Content:         Content{Text: ""},
			DocumentID:      documentID,
			textIsCanonical: true,
		}, nil
	}
	// A converted document keeps no attachment row: its text is the content, and
	// the reference stores no bytes and no attachment for one, so the document
	// read reports `attachments: null` for it. An image is the exception — its
	// bytes are the attachment the intended text refers to.
	if !image {
		return RetainItem{
			Content:         Content{Text: text},
			DocumentID:      documentID,
			textIsCanonical: true,
		}, nil
	}
	if err := h.Store.PutAttachment(r.Context(), storage.Attachment{
		BankID:         bankID,
		DocumentID:     documentID,
		AttachmentHash: hash,
		ShortID:        shortID,
		MediaType:      contentType,
		ByteSize:       int64(len(data)),
		StorageKey:     key,
		Kind:           "file",
		Filename:       header.Filename,
	}); err != nil {
		return RetainItem{}, err
	}
	return RetainItem{
		Content:         Content{Text: text},
		DocumentID:      documentID,
		AttachmentIDs:   []string{shortID},
		textIsCanonical: true,
	}, nil
}

// uploadBodyLimit bounds one multipart request body: the configured attachment
// count at the configured size, plus a margin for the surrounding form fields
// and multipart framing. A zero limit leaves the body unbounded, which is what
// an unconfigured deployment had before the bound existed.
func (h *Handlers) uploadBodyLimit() int64 {
	if h.MaxAttachments <= 0 || h.MaxAttachmentBytes <= 0 {
		return 0
	}
	return h.MaxAttachmentBytes*int64(h.MaxAttachments) + multipartOverheadBytes
}

// multipartOverheadBytes is the margin added to the body bound for the form
// fields, boundaries and headers around the files.
const multipartOverheadBytes = 1 << 20

// uploadTooLargeMessage is the refusal a body over the bound gets.
func (h *Handlers) uploadTooLargeMessage() string {
	return fmt.Sprintf("the upload exceeds the %d-file limit of %s per file.",
		h.MaxAttachments, humanBytes(h.MaxAttachmentBytes))
}

// humanBytes renders a byte count in the units the limit was configured in.
func humanBytes(size int64) string {
	const mega = 1 << 20
	if size%mega == 0 {
		return fmt.Sprintf("%dMB", size/mega)
	}
	return fmt.Sprintf("%.1fMB", float64(size)/mega)
}

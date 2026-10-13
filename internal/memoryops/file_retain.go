package memoryops

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// attachmentShortIDLength is how much of the content digest identifies an
// attachment inside document text. It matches the reference, so a placeholder
// written by one side resolves on the other.
const attachmentShortIDLength = 12

// FileRetain serves the file-to-memory write route. The route always processes
// asynchronously, rejects per-file fields placed at the request level, and
// rejects an update mode.
func (h *Handlers) FileRetain(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body must be a multipart upload"))
		return
	}
	if err := rejectRequestLevelFileFields(r); err != nil {
		httpx.WriteValidationFor(w, r, err)
		return
	}
	files := r.MultipartForm.File["files"]
	items := make([]RetainItem, 0, len(files))
	for _, header := range files {
		item, err := h.itemFromUpload(r, bankID, header)
		if err != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
			return
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
			Loc: []string{"body", "files"}, Msg: "at least one file is required",
			Type: "value_error.missing",
		}}})
		return
	}
	req := RetainRequest{Items: items, Async: true}
	// Reject an update mode placed per file, which the file route does not accept.
	if field := updateModeInItems(items); field != "" {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
			Loc: []string{"body", "files", "update_mode"}, Msg: "update mode is not accepted on the file route",
			Type: "value_error.mode",
		}}})
		return
	}
	result, err := h.Store.RetainWrite(r.Context(), toStorageRetain(bankID, req))
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// rejectRequestLevelFileFields rejects per-file fields placed at the request
// level instead of nested per file.
func rejectRequestLevelFileFields(r *http.Request) *httpx.ValidationFailure {
	for _, name := range []string{"content", "document_id", "timestamp", "metadata"} {
		if r.FormValue(name) != "" {
			return &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
				Loc: []string{"body", name}, Msg: "per-file fields must be nested per file",
				Type: "value_error.placement",
			}}}
		}
	}
	return nil
}

func updateModeInItems(items []RetainItem) string {
	for _, item := range items {
		if item.UpdateMode != "" {
			return item.UpdateMode
		}
	}
	return ""
}

// itemFromUpload reads one uploaded file, records its bytes content-addressed
// under the owning bank and document, and returns the retain item that carries
// the attachment. The write is idempotent on the content hash, so re-uploading
// an unchanged file rewrites nothing observable. A retain that later fails
// leaves a blob cached by key, which the next upload of the same content
// reuses rather than duplicating.
func (h *Handlers) itemFromUpload(r *http.Request, bankID string, header *multipart.FileHeader) (RetainItem, error) {
	file, err := header.Open()
	if err != nil {
		return RetainItem{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return RetainItem{}, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	shortID := hash[:attachmentShortIDLength]
	documentID := header.Filename

	key := storage.AttachmentStorageKey(bankID, documentID, hash)
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := h.Store.PutStoredFile(r.Context(), storage.StoredFile{
		Key: key, ContentType: contentType, Data: data,
	}); err != nil {
		return RetainItem{}, err
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
		Content:       Content{Text: string(data)},
		DocumentID:    documentID,
		AttachmentIDs: []string{shortID},
	}, nil
}

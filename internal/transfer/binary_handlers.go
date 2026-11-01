package transfer

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// feedPathPrefix is the path the router strips before the download key, so the
// key is what remains.
const feedPathPrefix = "/v1/default/files/download/"

// ServeAttachment reads one attachment's bytes for a bank. A missing
// attachment and an invisible bank both answer with the SAME not-found body,
// so the route cannot be used to probe which banks exist or what one holds.
func (h *Handlers) ServeAttachment(w http.ResponseWriter, r *http.Request) {
	bankID := bankIDFromRequest(r)
	attachmentID := strings.TrimSpace(r.PathValue("attachment_id"))
	if bankID == "" || attachmentID == "" {
		attachmentNotFound(w, r)
		return
	}
	// The bank is checked here rather than through the shared guard, because
	// the guard reports a bank-specific message that would distinguish an
	// unknown bank from a missing resource.
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		attachmentNotFound(w, r)
		return
	}
	// The id in the URL is the attachment's short id, so the row is what names
	// the bytes: a retained attachment's storage key is per document and per
	// content hash, and guessing it from the id alone never matches one.
	attachment, err := h.Store.GetAttachment(r.Context(), bankID, attachmentID)
	if err != nil {
		attachmentNotFound(w, r)
		return
	}
	// The declared media type wins over whatever the blob record carries: the
	// caller declared it at retain time, and this route's contract is to serve
	// exactly that.
	h.serveBinary(w, r, attachment.StorageKey, binaryWrite{
		ContentType:    attachment.MediaType,
		Disposition:    "",
		NotFoundDetail: detailAttachmentNotFound,
		Nosniff:        true,
		CacheControl:   "private, max-age=31536000, immutable",
	})
}

// ServeDownload reads a stored archive by its full key. The key is the only
// route in the family that is not bank-scoped, so the owning bank is recovered
// from the key itself and authorized before any bytes are read. The route is
// gated on the document-export switch like the export that produces its keys.
func (h *Handlers) ServeDownload(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentExportEnabled {
		gateDisabled(w, r, detailDisabledDocumentExport)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, feedPathPrefix)

	// A parent-directory segment anywhere in the key, or the empty first
	// segment a leading or doubled slash leaves behind, is rejected as
	// not-found, indistinguishable from a missing file, and no read occurs.
	// The key is used exactly as received: the reference splits the raw path
	// remainder, so a slash-padded key never resolves.
	if err := storage.ValidateStoredFileKey(key); err != nil {
		writeNotFound(w, r, detailFileNotFound)
		return
	}
	bankID, ok := storage.BankIDFromStoredFileKey(key)
	if !ok {
		writeNotFound(w, r, detailFileNotFound)
		return
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		writeNotFound(w, r, detailFileNotFound)
		return
	}
	// The archive is always streamed through the API, never replaced by a
	// redirect to a signed object-store URL: the route's contract is a 200 ZIP
	// response whose media type, disposition and content length the caller can
	// read directly. A backend that can sign a URL therefore still serves the
	// bytes here; signing is only ever a way for that backend to reach its own
	// storage, not a response the route hands out.
	h.serveBinary(w, r, key, binaryWrite{
		ContentType:    "application/zip",
		Disposition:    downloadFilename(bankID),
		NotFoundDetail: detailFileNotFound,
	})
}

// binaryWrite describes how a stored payload is served: the media type and
// attachment disposition it carries, the not-found detail a miss reports, and
// the optional hardening headers a route declares. A route that declares none
// (the download route does not) serves exactly what the reference serves.
type binaryWrite struct {
	ContentType    string
	Disposition    string
	NotFoundDetail string
	Nosniff        bool
	CacheControl   string
}

// serveBinary writes a stored payload with the headers the caller declared.
// When the store offers a streaming read the payload is handed to the response
// chunk by chunk, so the first byte is written before the whole payload is in
// memory; otherwise the payload is loaded and written as before.
func (h *Handlers) serveBinary(w http.ResponseWriter, r *http.Request, key string, write binaryWrite) {
	if streamer, ok := h.Store.(storage.StoredFileStreamer); ok {
		stream, err := streamer.OpenStoredFile(r.Context(), key)
		if err != nil {
			writeNotFound(w, r, write.NotFoundDetail)
			return
		}
		defer func() { _ = stream.Reader.Close() }()
		contentType := write.ContentType
		if contentType == "" {
			contentType = stream.ContentType
		}
		write.typeHeaders(w, contentType, stream.Size)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, stream.Reader)
		return
	}
	file, err := h.Store.GetStoredFile(r.Context(), key)
	if err != nil {
		writeNotFound(w, r, write.NotFoundDetail)
		return
	}
	contentType := write.ContentType
	if contentType == "" {
		contentType = file.ContentType
	}
	write.typeHeaders(w, contentType, int64(len(file.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Data)
}

// typeHeaders writes the response headers a payload carries: its media type and
// attachment disposition, plus the no-sniff and cache directives the route
// declares. A non-negative size is declared as Content-Length; a negative one —
// an unknown stream length — is left out so the transport uses chunked framing.
func (write binaryWrite) typeHeaders(w http.ResponseWriter, contentType string, size int64) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if write.Nosniff {
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	if write.CacheControl != "" {
		w.Header().Set("Cache-Control", write.CacheControl)
	}
	if write.Disposition != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+write.Disposition+`"`)
	}
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
}

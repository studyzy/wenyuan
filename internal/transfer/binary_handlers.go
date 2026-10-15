package transfer

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	h.serveBinary(w, r, attachment.StorageKey, attachment.MediaType, "", true)
}

// ServeDownload reads a stored archive by its full key. The key is the only
// route in the family that is not bank-scoped, so the owning bank is recovered
// from the key itself and authorized before any bytes are read.
func (h *Handlers) ServeDownload(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, feedPathPrefix)
	key = strings.Trim(key, "/")

	// A parent-directory segment anywhere in the key is rejected as
	// not-found, indistinguishable from a missing file, and no read occurs.
	if err := storage.ValidateStoredFileKey(key); err != nil {
		attachmentNotFound(w, r)
		return
	}
	bankID, ok := storage.BankIDFromStoredFileKey(key)
	if !ok {
		attachmentNotFound(w, r)
		return
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		attachmentNotFound(w, r)
		return
	}
	// A store that can sign a time-limited URL hands it out and lets the object
	// store serve the bytes, so an archive download is not proxied through the
	// API. A store that cannot sign — the row backend, or an anonymous bucket —
	// falls through to the proxy path below, and so does a signing failure,
	// which must not turn an available archive into an error.
	if signer, ok := h.Store.(storage.StoredFileDownloadURLer); ok {
		direct, err := signer.StoredFileDownloadURL(r.Context(), key, downloadURLTTL)
		if err == nil && direct != "" {
			http.Redirect(w, r, direct, http.StatusFound)
			return
		}
	}
	h.serveBinary(w, r, key, "application/zip", downloadFilename(bankID), false)
}

// downloadURLTTL is the lifetime of the direct URL an object-store download
// hands out. It is the reference's default for a signed download URL.
const downloadURLTTL = time.Hour

// serveBinary writes a stored payload with the headers the family promises.
// When the store offers a streaming read the payload is handed to the response
// chunk by chunk, so the first byte is written before the whole payload is in
// memory; otherwise the payload is loaded and written as before. A
// contentTypeHint overrides the media type the store reports, which the
// download route uses because an archive's type is declared by the route
// rather than inferred from the payload.
func (h *Handlers) serveBinary(w http.ResponseWriter, r *http.Request, key, contentTypeHint, disposition string, inline bool) {
	if streamer, ok := h.Store.(storage.StoredFileStreamer); ok {
		stream, err := streamer.OpenStoredFile(r.Context(), key)
		if err != nil {
			attachmentNotFound(w, r)
			return
		}
		defer func() { _ = stream.Reader.Close() }()
		contentType := contentTypeHint
		if contentType == "" {
			contentType = stream.ContentType
		}
		writeBinaryStream(w, contentType, disposition, stream.Reader, stream.Size, inline)
		return
	}
	file, err := h.Store.GetStoredFile(r.Context(), key)
	if err != nil {
		attachmentNotFound(w, r)
		return
	}
	contentType := contentTypeHint
	if contentType == "" {
		contentType = file.ContentType
	}
	writeBinary(w, contentType, disposition, file.Data, inline)
}

// writeBinary writes a byte payload with the headers the family promises: the
// declared media type, an optional attachment disposition, a no-sniff
// directive, and a private immutable cache directive. The explicit binary
// class prevents a JSON media type from being used for a byte response.
func writeBinary(w http.ResponseWriter, contentType, disposition string, data []byte, inline bool) {
	setBinaryHeaders(w, contentType, disposition, int64(len(data)), inline)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// writeBinaryStream writes a payload from a reader with the same headers as
// writeBinary, so a streamed response is indistinguishable from a buffered one
// on the wire. Content-Length is declared only when the size is known, because
// declaring a wrong one would truncate the response.
func writeBinaryStream(w http.ResponseWriter, contentType, disposition string, r io.Reader, size int64, inline bool) {
	setBinaryHeaders(w, contentType, disposition, size, inline)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, r)
}

// setBinaryHeaders writes the response headers both payload paths share. A
// non-negative size is declared as Content-Length; a negative one — an unknown
// stream length — is left out so the transport uses chunked framing.
func setBinaryHeaders(w http.ResponseWriter, contentType, disposition string, size int64, inline bool) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if inline {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if disposition != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+disposition+`"`)
	}
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
}

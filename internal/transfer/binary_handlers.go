package transfer

import (
	"net/http"
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
	key := storage.StoredFileKeyForBank(bankID, attachmentID)
	file, err := h.Store.GetStoredFile(r.Context(), key)
	if err != nil {
		attachmentNotFound(w, r)
		return
	}
	writeBinary(w, file.ContentType, "", file.Data, true)
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
	file, err := h.Store.GetStoredFile(r.Context(), key)
	if err != nil {
		attachmentNotFound(w, r)
		return
	}
	writeBinary(w, "application/zip", downloadFilename(bankID), file.Data, false)
}

// writeBinary writes a byte payload with the headers the family promises: the
// declared media type, an optional attachment disposition, a no-sniff
// directive, and a private immutable cache directive. The explicit binary
// class prevents a JSON media type from being used for a byte response.
func writeBinary(w http.ResponseWriter, contentType, disposition string, data []byte, inline bool) {
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
	w.Header().Set("Content-Length", itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

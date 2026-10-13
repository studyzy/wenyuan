package transfer

import (
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// bankIDFromRequest returns the {bank_id} path parameter.
func bankIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.PathValue("bank_id"))
}

// requireBank reports a not-found caller error when the bank is unknown, so
// every bank-scoped route in the family authorizes the same way.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := bankIDFromRequest(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	return bankID, true
}

// gateDisabled reports a not-found caller error for a route whose operator
// gate is off, so a disabled route is indistinguishable from an absent one.
func gateDisabled(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "route not available"))
}

// ExportTemplate serves the whole-bank archive export.
func (h *Handlers) Export(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if err := validateExportSelection(r); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	data, err := exportBank(r.Context(), h.Store, bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+downloadFilename(bankID)+`"`)
	w.Header().Set("Content-Length", itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// SubmitDocumentExport serves the asynchronous document export.
func (h *Handlers) SubmitDocumentExport(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentExportEnabled {
		gateDisabled(w, r)
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if err := validateExportSelection(r); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	payload := map[string]any{"document_ids": repeatingValues(r, "document_id")}
	if len(payload["document_ids"].([]string)) == 0 {
		h.submitRejected(w, r, "at least one document_id is required")
		return
	}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeDocumentExport, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// SubmitDocumentImport serves the asynchronous document import.
func (h *Handlers) SubmitDocumentImport(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentImportEnabled {
		gateDisabled(w, r)
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	data, err := readMultipartArchive(r)
	if err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	// The archive is validated synchronously, so a malformed payload or an
	// unsupported version fails now rather than in the queue.
	if _, err := validateArchive(data); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	key, err := h.stashUpload(r.Context(), bankID, "document-import.zip", data)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	payload := map[string]any{storage.OperationPayloadKeyStoredFile: key}
	if conflict := strings.TrimSpace(r.URL.Query().Get("on_conflict")); conflict != "" {
		payload["on_conflict"] = conflict
	}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeDocumentImport, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// SubmitDocumentExportArchive serves the asynchronous document export that
// produces an archive.
func (h *Handlers) SubmitDocumentTransferExport(w http.ResponseWriter, r *http.Request) {
	h.SubmitDocumentExport(w, r)
}

// SubmitTransferExport serves the asynchronous whole-bank export.
func (h *Handlers) SubmitTransferExport(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if err := validateExportSelection(r); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeTransferExport, scopePayload(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// SubmitTransferImport serves the asynchronous whole-bank restore.
func (h *Handlers) SubmitTransferImport(w http.ResponseWriter, r *http.Request) {
	h.SubmitImport(w, r, false)
}

// SubmitImportRoute adapts the import handler to the route signature.
func (h *Handlers) SubmitImportRoute(w http.ResponseWriter, r *http.Request) {
	h.SubmitImport(w, r, false)
}

// SubmitImport serves the asynchronous archive import. It is create-only for a
// restore and takes the archive's documents only for a merge.
func (h *Handlers) SubmitImport(w http.ResponseWriter, r *http.Request, templateJSON bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	data, present, err := readOptionalMultipartArchive(r)
	if err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	if !present {
		h.submitRejected(w, r, "multipart upload must carry a file field")
		return
	}
	requested, err := h.validateImportSelection(r, data, bankID)
	if err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	key, err := h.stashUpload(r.Context(), bankID, "transfer-import.zip", data)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	payload := scopePayload(r)
	payload[storage.OperationPayloadKeyStoredFile] = key
	for name, values := range requested {
		payload[name] = values
	}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeTransferImport, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// SubmitClone serves the asynchronous clone, which exports and imports back to
// back under a new identifier.
func (h *Handlers) SubmitClone(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target_bank_id"))
	if target == "" {
		h.submitRejected(w, r, "target_bank_id is required")
		return
	}
	if target == bankID {
		h.submitRejected(w, r, "a bank cannot be cloned onto itself")
		return
	}
	if _, err := h.Store.GetBank(r.Context(), target); err == nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict, "the target bank already exists"))
		return
	}
	payload := map[string]any{"target_bank_id": target}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeClone, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// attachmentNotFound is the single not-found answer for the binary reads, so a
// caller cannot distinguish an absent resource from an invisible bank.
func attachmentNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "not found"))
}

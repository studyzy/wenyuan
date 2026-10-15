package transfer

import (
	"fmt"
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// gateDisabled reports a not-found caller error for a route whose operator
// gate is off, so a disabled route is indistinguishable from an absent one.
func gateDisabled(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "route not available"))
}

// SubmitDocumentExport serves the asynchronous document export.
//
// Omitting document_id exports the whole bank, which is what the reference
// treats as the ordinary case; naming documents narrows the archive to that
// subset. The bank-level sections are only coherent with a whole-bank export,
// so combining them with a subset is refused rather than silently ignored.
func (h *Handlers) SubmitDocumentExport(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentExportEnabled {
		gateDisabled(w, r)
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if err := validateDocumentExportSelection(r); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	documents := repeatingValues(r, "document_id")
	payload := map[string]any{
		"document_ids":           documents,
		"include_observations":   isTruthy(httpx.Query(r, "include_observations")),
		"include_knowledge_base": isTruthy(httpx.Query(r, "include_knowledge_base")),
	}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeDocumentExport, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// SubmitDocumentImport serves the asynchronous document import.
//
// An import merges into the named bank, creating it when it does not exist yet,
// so a first import into a fresh identifier works exactly as the reference's
// does.
func (h *Handlers) SubmitDocumentImport(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentImportEnabled {
		gateDisabled(w, r)
		return
	}
	bankID := bankIDFromRequest(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
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
	payload := map[string]any{
		storage.OperationPayloadKeyStoredFile: key,
		"mode":                                string(ImportMerge),
	}
	conflict := strings.TrimSpace(httpx.Query(r, "on_conflict"))
	if conflict == "" {
		conflict = ConflictSkip
	}
	if !validConflict(conflict) {
		h.submitRejected(w, r, fmt.Sprintf("Invalid on_conflict '%s' (expected skip|replace|new-id)", conflict))
		return
	}
	payload["document_conflict"] = conflict
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
	payload := scopePayload(r)
	payload["document_ids"] = repeatingValues(r, "document_id")
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeTransferExport, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// SubmitTransferImport serves the asynchronous whole-bank restore or merge.
// It is gated the same way the document import route is, because it is the
// unified successor to that route: a disabled import surface answers not-found
// rather than being absent.
//
// A restore writes a whole bank into a fresh identifier and so refuses a target
// that already exists — the caller is told immediately, not by a failed
// background operation. A merge folds the archive's documents into the named
// bank, creating it when it does not exist.
func (h *Handlers) SubmitTransferImport(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentImportEnabled {
		gateDisabled(w, r)
		return
	}
	bankID := bankIDFromRequest(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
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
	// The operation is recorded against the bank in the path in either mode —
	// a restore says so explicitly, because the bank it writes into does not
	// exist yet — so the row is created when absent rather than refusing a
	// first import into a fresh identifier.
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	key, err := h.stashUpload(r.Context(), bankID, "transfer-import.zip", data)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	payload := map[string]any{storage.OperationPayloadKeyStoredFile: key}
	for name, value := range requested {
		payload[name] = value
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
//
// A clone is the transfer path with both halves on this instance, so it is
// gated on both flags: with either half disabled the operator has turned off
// bulk bank copying.
func (h *Handlers) SubmitClone(w http.ResponseWriter, r *http.Request) {
	if !h.DocumentExportEnabled || !h.DocumentImportEnabled {
		gateDisabled(w, r)
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(httpx.Query(r, "target_bank_id"))
	if target == "" {
		h.submitRejected(w, r, "target_bank_id is required")
		return
	}
	if target == bankID {
		h.submitRejected(w, r, "a bank cannot be cloned onto itself; choose a different target_bank_id")
		return
	}
	if _, err := h.Store.GetBank(r.Context(), target); err == nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict, "the target bank already exists"))
		return
	}
	payload := scopePayload(r)
	payload["target_bank_id"] = target
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

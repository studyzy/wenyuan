package transfer

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

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
// gate is off. The detail names the flag that would enable the route, matching
// the reference's own wording, so a caller can act on it rather than guess.
func gateDisabled(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, detail))
}

// rejectUploadError renders the reference's answer for a request whose required
// archive is absent — a request-validation failure naming the missing `file`
// field — or whose multipart body the parser could not read, which the
// reference reports as a plain caller error.
func (h *Handlers) rejectUploadError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errUploadFileMissing):
		writeValidationFailure(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body", "file"}, nil),
		}})
	case errors.Is(err, errMultipartUnparseable):
		h.submitRejected(w, r, err.Error())
	default:
		httpx.WriteErrorFor(w, r, asHTTPError(err))
	}
}

// The disabled-route details, one per gated route, naming the environment flag
// that turns the route back on.
const (
	detailDisabledDocumentExport = "Document export API is disabled. Set HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API=true to enable."
	detailDisabledBankExport     = "Bank export API is disabled. Set HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API=true to enable."
	detailDisabledDocumentImport = "Document import API is disabled. Set HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API=true to enable."
	detailDisabledBankImport     = "Bank import API is disabled. Set HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API=true to enable."
	detailDisabledClone          = "Bank clone API is disabled. It requires both HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API and HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API."
)

// writeValidationFailure reports a request-validation failure (the reference's
// 422 envelope with structured items).
func writeValidationFailure(w http.ResponseWriter, r *http.Request, failure *httpx.ValidationFailure) {
	httpx.WriteErrorFor(w, r, failure)
}

// writeNotFound reports a not-found caller error with an explicit detail, so
// the binary reads can name the resource that was missing.
func writeNotFound(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, detail))
}

// validateNewBankID refuses a bank identifier that must not be created, using
// the reference's own wording and status. Existing banks are never re-checked.
func validateNewBankID(id string) error {
	if id == "" {
		return wireMessage("Bank id must not be empty")
	}
	if len(id) > storage.BankIDMaxBytes {
		return wireMessage(fmt.Sprintf("Bank id is too long: at most %d bytes of UTF-8 are allowed", storage.BankIDMaxBytes))
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return wireMessage("Bank id must not contain control characters")
		}
	}
	return nil
}

// SubmitDocumentExport serves the asynchronous document export.
//
// Omitting document_id exports the whole bank, which is what the reference
// treats as the ordinary case; naming documents narrows the archive to that
// subset. The bank-level sections are only coherent with a whole-bank export,
// so combining them with a subset is refused rather than silently ignored.
func (h *Handlers) SubmitDocumentExport(w http.ResponseWriter, r *http.Request) {
	includeObservations, _, failure := queryBool(r, "include_observations")
	if failure != nil {
		writeValidationFailure(w, r, failure)
		return
	}
	includeKnowledgeBase, _, failure := queryBool(r, "include_knowledge_base")
	if failure != nil {
		writeValidationFailure(w, r, failure)
		return
	}
	if !h.DocumentExportEnabled {
		gateDisabled(w, r, detailDisabledDocumentExport)
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// A document export reaches every document the selection names, which is
	// the whole bank when none is named, so a tag-scoped caller may not run it.
	if httpx.RefuseBankWideIfScoped(w, r, "export documents") {
		return
	}
	documents := repeatingValues(r, "document_id")
	if err := validateDocumentExportSelection(documents, includeObservations, includeKnowledgeBase); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	payload := map[string]any{
		"document_ids":           documents,
		"include_observations":   includeObservations,
		"include_knowledge_base": includeKnowledgeBase,
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
		gateDisabled(w, r, detailDisabledDocumentImport)
		return
	}
	// An import writes every document in the archive, so a tag-scoped caller
	// may not run it.
	if httpx.RefuseBankWideIfScoped(w, r, "import documents") {
		return
	}
	bankID := bankIDFromRequest(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	// The conflict vocabulary is validated before any body work, and the value
	// is taken verbatim: the reference's default applies only when the
	// parameter is absent, never to a present empty or padded one.
	conflict, ok := queryEnum(r, "on_conflict", ConflictSkip, ConflictSkip, ConflictReplace, ConflictNewID)
	if !ok {
		h.submitRejected(w, r, fmt.Sprintf("Invalid on_conflict '%s' (expected skip|replace|new-id)", conflict))
		return
	}
	data, err := readMultipartArchive(r)
	if err != nil {
		h.rejectUploadError(w, r, err)
		return
	}
	// The archive is validated synchronously, so a malformed payload or an
	// unsupported version fails now rather than in the queue. The bank is only
	// materialized once the archive is known to be acceptable, so a rejected
	// import never leaves an empty bank behind.
	if _, err := validateArchive(data, producedByDocumentExport); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	key, err := h.stashUpload(r.Context(), bankID, "document-import.zip", data)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// The persisted payload carries the archive key and the conflict policy,
	// exactly the members the reference records: the mode is a request-level
	// choice, not a queued one, so it is implied by the operation type.
	payload := map[string]any{
		storage.OperationPayloadKeyStoredFile: key,
		"on_conflict":                         conflict,
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

// SubmitTransferExport serves the asynchronous whole-bank export, or a
// document subset when document_id is named. A subset is not a bank, so it
// cannot carry the bank-level sections: the reference refuses that combination
// rather than silently ignoring it.
func (h *Handlers) SubmitTransferExport(w http.ResponseWriter, r *http.Request) {
	scope, failure := scopeFromQuery(r)
	if failure != nil {
		writeValidationFailure(w, r, failure)
		return
	}
	if !h.DocumentExportEnabled {
		gateDisabled(w, r, detailDisabledBankExport)
		return
	}
	data, bankConfig, history := scope.resolved()
	// A request that asks for nothing is refused before any bank lookup, as
	// the reference does.
	if !data && !bankConfig && !history {
		h.submitRejected(w, r, "Nothing to export: set at least one of include_data, include_bank_config, include_history")
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	documents := repeatingValues(r, "document_id")
	// A whole-bank export is the bank export; a subset is the document export
	// under this route's vocabulary. Either way it is bank-wide, so a
	// tag-scoped caller may not run it, and the reference names the operation
	// it would have run.
	exportWhat := "export the bank"
	if len(documents) > 0 {
		exportWhat = "export documents"
	}
	if httpx.RefuseBankWideIfScoped(w, r, exportWhat) {
		return
	}
	if len(documents) > 0 {
		if err := validateExportSelection(documents, bankConfig, history); err != nil {
			h.submitRejected(w, r, err.Error())
			return
		}
		// A subset export is the document export under this route's vocabulary,
		// so it records the document-export operation the reference does.
		op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeDocumentExport,
			map[string]any{"document_ids": documents})
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		writeAccepted(w, r, op)
		return
	}
	payload := scopePayload(scope)
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
// A restore writes a whole bank into a fresh identifier: the target must not
// already exist and the bank the operation is recorded against must, so the
// caller is told immediately rather than by a failed background operation. A
// merge folds the archive's documents into the named bank, creating it when it
// does not exist.
func (h *Handlers) SubmitTransferImport(w http.ResponseWriter, r *http.Request) {
	scope, failure := scopeFromQuery(r)
	if failure != nil {
		writeValidationFailure(w, r, failure)
		return
	}
	if !h.DocumentImportEnabled {
		gateDisabled(w, r, detailDisabledBankImport)
		return
	}
	bankID := bankIDFromRequest(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	// The mode and document-conflict vocabularies are validated verbatim before
	// any body work: a present empty or padded value is rejected rather than
	// defaulted, matching the reference.
	mode, ok := queryEnum(r, "mode", "restore", "restore", "merge")
	if !ok {
		h.submitRejected(w, r, fmt.Sprintf("Invalid mode '%s' (expected restore|merge)", mode))
		return
	}
	conflict, ok := queryEnum(r, "document_conflict", "skip", "skip", "replace", "new-id")
	if !ok {
		h.submitRejected(w, r, fmt.Sprintf("Invalid document_conflict '%s' (expected skip|replace|new-id)", conflict))
		return
	}
	// A merge is the document import; a restore is the bank import. Both are
	// bank-wide, so a tag-scoped caller may not run them, and the reference
	// names the operation the mode would have run.
	importWhat := "import into the bank"
	if mode == "merge" {
		importWhat = "import documents"
	}
	if httpx.RefuseBankWideIfScoped(w, r, importWhat) {
		return
	}
	data, _, err := readOptionalMultipartArchive(r)
	if err != nil {
		h.rejectUploadError(w, r, err)
		return
	}
	target, err := h.resolveImportSelection(r, data, mode)
	if err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}

	// A merge is the document import under this route's vocabulary: the
	// reference records it as an import_documents operation and creates the
	// bank when it does not exist yet. A restore records an import_bank
	// operation against {bank_id}, which must already exist — the operation row
	// has a foreign key to the bank, so a missing one is answered not-found
	// rather than creating the bank as a side effect of a rejected request.
	operationType := storage.OperationTypeTransferImport
	if mode == "merge" {
		operationType = storage.OperationTypeDocumentImport
		if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
	} else if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}

	key, err := h.stashUpload(r.Context(), bankID, "transfer-import.zip", data)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// The persisted payload carries exactly the members the reference records:
	// a merge keeps the archive key and the conflict policy, a restore keeps the
	// archive key, the resolved target bank and the requested scope booleans.
	// The mode is a request-level choice, implied by the operation type.
	payload := map[string]any{storage.OperationPayloadKeyStoredFile: key}
	if mode == "merge" {
		payload["on_conflict"] = conflict
	} else {
		includeData, includeBankConfig, includeHistory := scope.resolved()
		payload["target_bank_id"] = target
		payload["include_data"] = includeData
		payload["include_bank_config"] = includeBankConfig
		payload["include_history"] = includeHistory
	}
	op, err := h.recordOperation(r.Context(), bankID, operationType, payload)
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
	scope, failure := scopeFromQuery(r)
	if failure != nil {
		writeValidationFailure(w, r, failure)
		return
	}
	// target_bank_id is a required query parameter, so its absence is a
	// request-validation failure rather than a caller error.
	values, present := httpx.QueryPresent(r, "target_bank_id")
	if !present || len(values) == 0 {
		writeValidationFailure(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"query", "target_bank_id"}, nil),
		}})
		return
	}
	targetBankID := lastQueryValue(values)
	if !h.DocumentExportEnabled || !h.DocumentImportEnabled {
		gateDisabled(w, r, detailDisabledClone)
		return
	}
	data, bankConfig, history := scope.resolved()
	// A clone that inherits nothing is refused before any bank lookup, as the
	// reference does.
	if !data && !bankConfig && !history {
		h.submitRejected(w, r, "Nothing to clone: set at least one of include_data, include_bank_config, include_history")
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	// A clone copies the whole bank, so a tag-scoped caller may not run it.
	if httpx.RefuseBankWideIfScoped(w, r, "clone the bank") {
		return
	}
	if targetBankID == bankID {
		h.submitRejected(w, r, "A bank cannot be cloned onto itself; choose a different target_bank_id")
		return
	}
	if err := validateNewBankID(targetBankID); err != nil {
		h.submitRejected(w, r, err.Error())
		return
	}
	if _, err := h.Store.GetBank(r.Context(), targetBankID); err == nil {
		h.submitRejected(w, r, fmt.Sprintf(
			"Target bank '%s' already exists; a clone writes into a fresh bank (it is not a merge). Delete it first, or choose a different target bank id.",
			targetBankID))
		return
	}
	payload := scopePayload(scope)
	payload["target_bank_id"] = targetBankID
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeClone, payload)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	writeAccepted(w, r, op)
}

// Binary read not-found details. The reference names the resource that was
// missing; both are single messages so a missing resource and an invisible bank
// stay indistinguishable within each route.
const (
	detailAttachmentNotFound = "Attachment not found"
	detailFileNotFound       = "File not found"
)

// attachmentNotFound is the single not-found answer for the attachment read, so
// a caller cannot distinguish an absent attachment from an invisible bank.
func attachmentNotFound(w http.ResponseWriter, r *http.Request) {
	writeNotFound(w, r, detailAttachmentNotFound)
}

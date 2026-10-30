package transfer

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// The import gate: the deployment's chance to refuse a transfer-archive import
// before anything is written.
//
// A restore does not run extraction — it replays the archive — so a deployment
// that charges, meters or policy-checks per document otherwise has no say in
// what lands (the reference's #5137). The reference therefore runs two hooks at
// submission, in this order, and this file carries the second:
//
//   - the bank-write gate, which the shared middleware already applies to every
//     bank-scoped write;
//   - the validator's validate_import, over the archive's own documents
//     (engine/memory_engine.py:9076-9079 for a document import, :8244-8252 for
//     a restore of the data section, :9175-9196 for the document list).
//
// It runs after the target and bank checks — a request that fails those has
// nothing to gate — and before the bank is created, the archive is stored or
// the operation is queued, so a refusal leaves all three untouched. A clone is
// not gated: it is assembled in the worker from a bank this instance already
// holds, so no archive ever passes a submission (memory_engine.py:8295-8320).

// refuseImport checks the archive against the deployment's import gate. It
// answers true when the import may proceed, and has already written the
// refusal otherwise.
func (h *Handlers) refuseImport(w http.ResponseWriter, r *http.Request, bankID string, archive *transferarchive.Archive) bool {
	if h.ImportGate == nil || archive == nil {
		return true
	}
	gate := h.ImportGate()
	if gate == nil {
		return true
	}
	documents := make([]httpx.ImportDocument, 0, len(archive.Documents))
	for _, document := range archive.Documents {
		documents = append(documents, httpx.ImportDocument{
			DocumentID: document.ID,
			Content:    document.OriginalText,
		})
	}
	decision := gate.ValidateImport(r.Context(), bankID, documents)
	if decision.Allowed {
		return true
	}
	status := decision.Status
	if status == 0 {
		status = httpx.KindForbidden
	}
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(status, decision.Reason))
	return false
}

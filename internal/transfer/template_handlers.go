package transfer

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxTemplateBodyBytes bounds a JSON manifest import body.
const maxTemplateBodyBytes = 8 << 20

// templateManifest is the JSON manifest the template import endpoint accepts.
type templateManifest struct {
	BankID string `json:"bank_id"`
	Config struct {
		MentalModels []string `json:"mental_models"`
		Directives   []string `json:"directives"`
	} `json:"config"`
}

// templateImportResult is the observable result of a template import.
type templateImportResult struct {
	BankID          string   `json:"bank_id"`
	ConfigApplied   bool     `json:"config_applied"`
	CreatedModels   []string `json:"created_mental_models"`
	UpdatedModels   []string `json:"updated_mental_models"`
	CreatedDirectiv []string `json:"created_directives"`
	UpdatedDirectiv []string `json:"updated_directives"`
	OperationIDs    []string `json:"operation_ids"`
	DryRun          bool     `json:"dry_run"`
}

// ImportTemplate accepts a JSON manifest. A validate-only request returns the
// same response shape without applying changes.
func (h *Handlers) ImportTemplate(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var manifest templateManifest
	if err := decodeJSONBody(r, maxTemplateBodyBytes, &manifest); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if manifest.BankID != "" && manifest.BankID != bankID {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "manifest bank does not match the request bank"))
		return
	}
	dryRun := isTruthy(r.URL.Query().Get("dry_run"))
	result := templateImportResult{
		BankID:          bankID,
		CreatedModels:   append([]string{}, manifest.Config.MentalModels...),
		CreatedDirectiv: append([]string{}, manifest.Config.Directives...),
		UpdatedModels:   []string{},
		UpdatedDirectiv: []string{},
		OperationIDs:    []string{},
		DryRun:          dryRun,
	}
	if dryRun {
		httpx.WriteJSONFor(w, r, http.StatusOK, result)
		return
	}
	if !h.DocumentImportEnabled {
		gateDisabled(w, r)
		return
	}
	op, err := h.recordOperation(r.Context(), bankID, storage.OperationTypeTransferImport, map[string]any{
		"manifest_bank_id": bankID,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	result.ConfigApplied = true
	result.OperationIDs = []string{op.OperationID}
	httpx.WriteJSONFor(w, r, http.StatusOK, result)
}

// ExportTemplate renders the bank's configuration as a JSON manifest.
func (h *Handlers) ExportTemplate(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, templateManifest{BankID: bankID})
}

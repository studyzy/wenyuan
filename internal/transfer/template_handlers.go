package transfer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxTemplateBodyBytes bounds a JSON manifest import body.
const maxTemplateBodyBytes = 8 << 20

// BankTemplateImportResponse is the observable result of a template import.
type BankTemplateImportResponse struct {
	BankID              string   `json:"bank_id"`
	ConfigApplied       bool     `json:"config_applied"`
	MentalModelsCreated []string `json:"mental_models_created"`
	MentalModelsUpdated []string `json:"mental_models_updated"`
	DirectivesCreated   []string `json:"directives_created"`
	DirectivesUpdated   []string `json:"directives_updated"`
	OperationIDs        []string `json:"operation_ids"`
	DryRun              bool     `json:"dry_run"`
}

// ImportTemplate accepts a JSON bank template manifest. A validate-only
// request (dry_run=true) returns the same response shape without applying
// changes; a missing bank is created. The route is not gated by the document
// import switch, because it is a bank configuration surface rather than an
// archive transfer.
func (h *Handlers) ImportTemplate(w http.ResponseWriter, r *http.Request) {
	bankID := bankIDFromRequest(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	var manifest banktemplate.Manifest
	if err := decodeJSONBody(r, maxTemplateBodyBytes, &manifest); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "Template schema validation failed: "+err.Error()))
		return
	}
	if err := validateTemplateManifest(manifest); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	dryRun := isTruthy(httpx.Query(r, "dry_run"))
	if dryRun {
		httpx.WriteJSONFor(w, r, http.StatusOK, dryRunResponse(bankID, manifest))
		return
	}
	result, err := h.applyTemplateManifest(r.Context(), bankID, manifest)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, result)
}

// validateTemplateManifest enforces the rules the reference applies beyond JSON
// typing, and reports them the way the reference does: the structural failures
// under "Template schema validation failed", the semantic ones under
// "Template validation failed", each joining every failure it found rather than
// stopping at the first.
//
// The messages are served to the client verbatim, in the reference's own
// capitalization, which is why they are wireMessage values rather than Go-style
// error strings.
func validateTemplateManifest(manifest banktemplate.Manifest) error {
	structural, semantic := banktemplate.Validate(manifest)
	if len(structural) > 0 {
		return wireMessage("Template schema validation failed: " + strings.Join(structural, "; "))
	}
	if len(semantic) > 0 {
		return wireMessage("Template validation failed: " + strings.Join(semantic, "; "))
	}
	return nil
}

// wireMessage is a validation message served to the client verbatim, in the
// reference's own capitalization. It is a type so that the served text and Go's
// lowercase-error-string convention do not have to be traded against each other.
type wireMessage string

func (m wireMessage) Error() string { return string(m) }

// dryRunResponse reports what an import would create without touching storage.
func dryRunResponse(bankID string, manifest banktemplate.Manifest) BankTemplateImportResponse {
	result := emptyTemplateResponse(bankID)
	result.ConfigApplied = manifest.Bank != nil
	for _, model := range manifest.MentalModels {
		result.MentalModelsCreated = append(result.MentalModelsCreated, model.ID)
	}
	for _, directive := range manifest.Directives {
		result.DirectivesCreated = append(result.DirectivesCreated, directive.Name)
	}
	result.DryRun = true
	return result
}

// applyTemplateManifest creates the bank when it does not exist and applies the
// manifest's config, mental models and directives through the shared template
// engine, so the import route and the server-owned default template stay one
// implementation.
func (h *Handlers) applyTemplateManifest(ctx context.Context, bankID string, manifest banktemplate.Manifest) (BankTemplateImportResponse, error) {
	if _, err := h.Store.GetBank(ctx, bankID); err != nil {
		if _, createErr := h.Store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: bankID}); createErr != nil && !errors.Is(createErr, storage.ErrBankExists) {
			return BankTemplateImportResponse{}, createErr
		}
	}

	applied, err := banktemplate.Apply(ctx, h.Store, bankID, manifest)
	if err != nil {
		return BankTemplateImportResponse{}, err
	}

	result := emptyTemplateResponse(bankID)
	result.ConfigApplied = applied.ConfigApplied
	result.MentalModelsCreated = applied.MentalModelsCreated
	result.MentalModelsUpdated = applied.MentalModelsUpdated
	result.DirectivesCreated = applied.DirectivesCreated
	result.DirectivesUpdated = applied.DirectivesUpdated
	result.OperationIDs = applied.OperationIDs
	return result, nil
}

// ExportTemplate renders the bank's configuration, mental models and
// directives as a JSON manifest.
func (h *Handlers) ExportTemplate(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	bank, err := h.Store.GetBank(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	models, err := h.Store.ListMentalModels(r.Context(), bankID, "")
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	directives, err := h.Store.ListDirectives(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}

	manifest := banktemplate.Manifest{Version: banktemplate.CurrentTemplateVersion}
	if bankConfig := bankTemplateConfig(bank); len(bankConfig) > 0 {
		manifest.Bank = bankConfig
	}
	for _, model := range models {
		tags := model.Tags
		if tags == nil {
			tags = []string{}
		}
		trigger := model.Trigger
		if trigger == nil {
			trigger = map[string]any{}
		}
		maxTokens := model.MaxTokens
		if maxTokens == 0 {
			maxTokens = 2048
		}
		manifest.MentalModels = append(manifest.MentalModels, banktemplate.MentalModel{
			ID:          model.ID,
			Name:        model.Name,
			SourceQuery: model.SourceQuery,
			Tags:        tags,
			MaxTokens:   maxTokens,
			Trigger:     trigger,
		})
	}
	for _, directive := range directives {
		tags := directive.Tags
		if tags == nil {
			tags = []string{}
		}
		manifest.Directives = append(manifest.Directives, banktemplate.Directive{
			Name:     directive.Name,
			Content:  directive.Content,
			Priority: directive.Priority,
			IsActive: directive.IsActive,
			Tags:     tags,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, manifest)
}

// emptyTemplateResponse is the zero response with every list a non-nil empty
// slice, so a consumer never reads a null where the model declares an array.
func emptyTemplateResponse(bankID string) BankTemplateImportResponse {
	return BankTemplateImportResponse{
		BankID:              bankID,
		MentalModelsCreated: []string{},
		MentalModelsUpdated: []string{},
		DirectivesCreated:   []string{},
		DirectivesUpdated:   []string{},
		OperationIDs:        []string{},
	}
}

// bankTemplateConfig is the bank's own configuration as a template carries it:
// the stored configuration overrides, plus the disposition traits and reflect
// mission that live on the bank row. Only what the bank actually set is named,
// so an unset field stays absent rather than being materialized as its default.
func bankTemplateConfig(bank storage.Bank) map[string]any {
	out := map[string]any{}
	for key, value := range bank.Config {
		out[key] = value
	}
	for _, trait := range []struct{ field, key string }{
		{"disposition_skepticism", "skepticism"},
		{"disposition_literalism", "literalism"},
		{"disposition_empathy", "empathy"},
	} {
		if value, ok := bank.Disposition[trait.key]; ok {
			out[trait.field] = value
		}
	}
	if bank.Mission != "" {
		out["reflect_mission"] = bank.Mission
	}
	return out
}

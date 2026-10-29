package transfer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/bankapi"
	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
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
	// dry_run is a strict boolean query parameter: the reference validates it
	// before the handler body runs, so an unparseable value is a
	// request-validation failure rather than a silent non-dry run.
	dryRun, _, failure := queryBool(r, "dry_run")
	if failure != nil {
		writeValidationFailure(w, r, failure)
		return
	}
	var manifest banktemplate.Manifest
	if err := decodeJSONBody(r, maxTemplateBodyBytes, &manifest); err != nil {
		// A body that is not JSON (or is truncated) fails before the schema is
		// consulted, so the reference reports the parser's own message rather
		// than a schema-validation prefix.
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if err := validateTemplateManifest(manifest); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if dryRun {
		httpx.WriteJSON(w, http.StatusOK, dryRunResponse(bankID, manifest))
		return
	}
	// A template import changes the whole bank's config; the reference only
	// refuses a scoped caller on the applying path, not the dry run.
	if httpx.RefuseBankWideIfScoped(w, r, "import a bank template") {
		return
	}
	result, err := h.applyTemplateManifest(r.Context(), bankID, manifest)
	if err != nil {
		// A bank deleted between the import's authorization and its write is a
		// retryable conflict, exactly as the reference reports it, rather than a
		// not-found the caller would read as "no such bank".
		if errors.Is(err, storage.ErrBankNotFound) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindConflict, fmt.Sprintf(
				"Bank '%s' changed or was deleted during template import; retry the import.", bankID)))
			return
		}
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
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
	// The bank section is a configuration write: the reference persists it
	// through update_bank_config, whose validation refuses a key the
	// configuration endpoint would refuse, so a manifest cannot smuggle an
	// unknown field into the bank's config (http.py:9004-9045 routes the import
	// through validate_bank_config_updates). The dry run validates the same set
	// before it answers.
	if manifest.Bank != nil {
		if err := bankapi.ValidateConfigurableFields(manifest.Bank); err != nil {
			return err
		}
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
	// A template export carries the whole bank's config, an admin read the
	// reference refuses to a tag-scoped caller.
	if httpx.RefuseBankWideIfScoped(w, r, "export the bank template") {
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
	if bankConfig := bankTemplateConfig(bank); bankConfig != nil {
		manifest.Bank = bankConfig
	}
	for _, model := range models {
		// Knowledge pages are left out: a template carries no pages, and their
		// backing models would come back on import as standalone mental models
		// (the reference lists exclude_knowledge_pages=True, api/http.py:9106-9110).
		if model.IsKnowledgePage != nil && *model.IsKnowledgePage {
			continue
		}
		// The reference constructs its trigger model from the stored value on
		// every export, so a stored trigger that model rejects — a foreign-written
		// or otherwise corrupt row — fails the export with a server error rather
		// than being emitted unvalidated.
		if failure := mentalmodels.ValidateTriggerObject(model.Trigger); failure != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, failure.Error()))
			return
		}
		tags := model.Tags
		if tags == nil {
			tags = []string{}
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
			Trigger:     bankTemplateTrigger(model.Trigger),
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
	httpx.WriteJSON(w, http.StatusOK, manifest)
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

// bankTemplateFields are the configurable fields a template carries, in the
// reference model's declaration order. A stored field outside this set is not
// part of a template and is dropped on export. The names are the template
// model's own wire contract — the same fields internal/bankapi/config_keys.go
// lets a bank write.
var bankTemplateFields = []string{
	"reflect_mission",
	"retain_mission",
	"retain_extraction_mode",
	"retain_custom_instructions",
	"retain_chunk_size",
	"retain_structured_chunk_size",
	"enable_observations",
	"observations_mission",
	"enable_text_search",
	"enable_temporal_retrieval",
	"enable_graph_retrieval",
	"enable_reranking",
	"reflect_use_decision_model",
	"mental_model_refresh_use_decision_model",
	"mental_model_delta_use_decision_model",
	"disposition_skepticism",
	"disposition_literalism",
	"disposition_empathy",
	"entity_labels",
	"entities_allow_free_form",
	"retain_default_strategy",
	"retain_strategies",
	"retain_chunk_batch_size",
	"retain_max_attachments_per_chunk",
	"mcp_enabled_tools",
	"consolidation_llm_batch_size",
	"consolidation_source_facts_max_tokens",
	"consolidation_source_facts_max_tokens_per_observation",
	"max_observations_per_scope",
	"observation_scope_limits",
	"default_observation_scopes",
	"consolidation_strategies",
	"reflect_source_facts_max_tokens",
	"knowledge_page_default_trigger",
	"reflect_default_options",
	"mental_model_min_refresh_interval_seconds",
	"llm_gemini_safety_settings",
	"recall_budget_function",
	"recall_budget_fixed_low",
	"recall_budget_fixed_mid",
	"recall_budget_fixed_high",
	"recall_budget_adaptive_low",
	"recall_budget_adaptive_mid",
	"recall_budget_adaptive_high",
	"recall_budget_min",
	"recall_budget_max",
	"audit_log_enabled",
	"store_document_text",
	"enable_auto_consolidation",
	"consolidation_use_decision_model",
	"consolidation_max_memories_per_round",
	"consolidation_llm_parallelism",
	"recall_include_chunks",
	"recall_prefer_observations",
	"recall_max_tokens",
	"recall_chunks_max_tokens",
	"memory_defense",
}

// bankTemplateFieldSet is bankTemplateFields as a membership set.
var bankTemplateFieldSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(bankTemplateFields))
	for _, field := range bankTemplateFields {
		set[field] = struct{}{}
	}
	return set
}()

// triggerTemplateFields are the trigger members the reference model declares,
// in declaration order. Every one is exported — null where unset — because the
// reference dumps the whole model rather than only what was stored.
var triggerTemplateFields = []string{
	"budget",
	"mode",
	"refresh_after_consolidation",
	"refresh_cron",
	"full_refresh_cron",
	"min_refresh_interval_seconds",
	"fact_types",
	"exclude_mental_models",
	"exclude_mental_model_ids",
	"tags_match",
	"tag_groups",
	"include_chunks",
	"recall_max_tokens",
	"recall_chunks_max_tokens",
	"reflect_search_observations_max_tokens",
	"reflect_search_observations_include_entities",
	"response_schema",
	"keep_trace",
}

// triggerTemplateDefaults are the trigger members the reference model gives a
// non-null default. The rest default to null.
var triggerTemplateDefaults = map[string]any{
	"mode":                        "full",
	"refresh_after_consolidation": false,
	"exclude_mental_models":       false,
	"keep_trace":                  false,
}

// bankTemplateTrigger renders a stored trigger as the reference exports it: the
// full model, with the stored value where one exists and the model's default
// (or null) elsewhere. A stored member the model does not declare is dropped.
func bankTemplateTrigger(stored map[string]any) map[string]any {
	out := make(map[string]any, len(triggerTemplateFields))
	for _, field := range triggerTemplateFields {
		out[field] = nil
	}
	for key, value := range stored {
		if _, known := out[key]; known {
			// The reference rebuilds the trigger through MentalModelTrigger, whose
			// cron validator rewrites a whitespace-only member to null; every read
			// projects it that way, and the export does too.
			if (key == "refresh_cron" || key == "full_refresh_cron") && cronBlank(value) {
				continue
			}
			out[key] = value
		}
	}
	for key, value := range triggerTemplateDefaults {
		if out[key] == nil {
			out[key] = value
		}
	}
	return out
}

// bankTemplateConfig is the bank's own configuration as a template carries it:
// every template field, with the bank's stored value where it has one and null
// elsewhere. A bank with no stored configuration at all exports no config
// section (nil), matching the reference. A stored value the template model
// rejects — for example a disposition outside 1..5, written by an older version
// or another writer — is dropped rather than failing the whole export.
//
// The template reads the config COLUMN and nothing else, so the disposition
// traits are whatever the bank's stored config carries ('disposition_skepticism'
// and its siblings are config keys — config_resolver.py:988-994) and never the
// legacy banks.disposition column. The reference's export loads
// _load_bank_config, which selects `config` alone
// (config_resolver.py:415-425), so a bank whose legacy column holds the neutral
// triple exports `disposition_*: null` there. Reading the column here would
// invent three values the reference does not report, and would turn a bank with
// no config into a populated `bank` section where the reference sends null.
func bankTemplateConfig(bank storage.Bank) map[string]any {
	out := make(map[string]any, len(bankTemplateFields))
	for key, value := range bank.Config {
		if _, known := bankTemplateFieldSet[key]; !known {
			continue
		}
		if !templateValueAllowed(key, value) {
			continue
		}
		out[key] = value
	}
	if bank.Mission != "" {
		out["reflect_mission"] = bank.Mission
	}
	if len(out) == 0 {
		return nil
	}
	for _, field := range bankTemplateFields {
		if _, present := out[field]; !present {
			out[field] = nil
		}
	}
	return out
}

// templateValueAllowed reports whether a stored config value satisfies the
// bounds the template model declares for its field. Fields without a declared
// bound accept any value here; the bounds that exist are the disposition traits
// (1..5) and the minimum refresh interval (>= 0).
func templateValueAllowed(field string, value any) bool {
	switch field {
	case "disposition_skepticism", "disposition_literalism", "disposition_empathy":
		number, ok := wholeNumber(value)
		return ok && number >= 1 && number <= 5
	case "mental_model_min_refresh_interval_seconds":
		number, ok := wholeNumber(value)
		return ok && number >= 0
	default:
		return true
	}
}

// wholeNumber reports whether a decoded JSON value is an integer, returning it.
func wholeNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed == math.Trunc(typed) {
			return int64(typed), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// cronBlank reports a cron member the reference's validator would rewrite to
// null: a string that is empty or whitespace only.
func cronBlank(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(text) == ""
}

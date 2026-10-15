package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// currentTemplateVersion is the manifest schema version this build accepts. A
// manifest that names a newer version is refused rather than guessed at.
const currentTemplateVersion = "1"

// maxTemplateBodyBytes bounds a JSON manifest import body.
const maxTemplateBodyBytes = 8 << 20

// templateIDPattern is the mental-model identifier shape the manifest requires:
// alphanumeric lowercase with hyphens, starting with a letter or digit.
var templateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// templateManifest is the JSON bank template manifest: the body the import
// endpoint accepts and the document the export endpoint produces. The field
// names and nullability are public surface.
type templateManifest struct {
	Version      string                `json:"version"`
	Bank         map[string]any        `json:"bank"`
	MentalModels []templateMentalModel `json:"mental_models"`
	Directives   []templateDirective   `json:"directives"`
}

// templateMentalModel is one mental-model definition within a manifest.
type templateMentalModel struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	SourceQuery string         `json:"source_query"`
	Tags        []string       `json:"tags"`
	MaxTokens   int            `json:"max_tokens"`
	Trigger     map[string]any `json:"trigger"`
}

// templateDirective is one directive definition within a manifest. Directives
// are matched by name on re-import.
type templateDirective struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Priority int      `json:"priority"`
	IsActive bool     `json:"is_active"`
	Tags     []string `json:"tags"`
}

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
	var manifest templateManifest
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
func validateTemplateManifest(manifest templateManifest) error {
	var structural, semantic []string

	version := strings.TrimSpace(manifest.Version)
	parsed, err := strconv.Atoi(version)
	switch {
	case version == "":
		structural = append(structural, "version: Field required")
	case err != nil:
		structural = append(structural,
			fmt.Sprintf("version: version must be a numeric string, got '%s'", version))
	case parsed < 1:
		structural = append(structural, "version: version must be >= 1")
	case parsed > atoiTemplate(currentTemplateVersion):
		structural = append(structural, fmt.Sprintf(
			"version '%s' is not supported by this server (max supported: %s). Please upgrade Hindsight.",
			version, currentTemplateVersion))
	}

	seenModels := map[string]bool{}
	for i, model := range manifest.MentalModels {
		if !templateIDPattern.MatchString(model.ID) {
			structural = append(structural, fmt.Sprintf(
				"mental_models[%d].id: Mental model id '%s' must be alphanumeric lowercase with hyphens, starting with a letter or digit.",
				i, model.ID))
		}
		if model.MaxTokens != 0 && (model.MaxTokens < 256 || model.MaxTokens > 8192) {
			structural = append(structural, fmt.Sprintf(
				"mental_models[%d].max_tokens: must be between 256 and 8192", i))
		}
		if strings.TrimSpace(model.Name) == "" {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].name: must not be empty", i))
		}
		if strings.TrimSpace(model.SourceQuery) == "" {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].source_query: must not be empty", i))
		}
		if seenModels[model.ID] {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].id: duplicate id '%s'", i, model.ID))
		}
		seenModels[model.ID] = true
	}

	seenDirectives := map[string]bool{}
	for i, directive := range manifest.Directives {
		if strings.TrimSpace(directive.Name) == "" {
			semantic = append(semantic, fmt.Sprintf("directives[%d].name: must not be empty", i))
		}
		if strings.TrimSpace(directive.Content) == "" {
			semantic = append(semantic, fmt.Sprintf("directives[%d].content: must not be empty", i))
		}
		if seenDirectives[directive.Name] {
			semantic = append(semantic, fmt.Sprintf("directives[%d].name: duplicate name '%s'", i, directive.Name))
		}
		seenDirectives[directive.Name] = true
	}

	// The bank's own settings have to agree with each other before they are
	// written: an extraction mode is one of the modes the engine implements, and
	// custom instructions are only read in the mode that consumes them.
	if bank := manifest.Bank; bank != nil {
		mode, _ := bank["retain_extraction_mode"].(string)
		custom, _ := bank["retain_custom_instructions"].(string)
		if mode != "" && !retainExtractionMode(mode) {
			semantic = append(semantic, fmt.Sprintf(
				"bank.retain_extraction_mode: must be one of (%s), got '%s'",
				strings.Join(retainExtractionModes, ", "), mode))
		}
		if strings.TrimSpace(custom) != "" && mode != "custom" {
			semantic = append(semantic, "bank.retain_custom_instructions: requires retain_extraction_mode='custom'")
		}
	}

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

// retainExtractionModes are the modes the engine implements, which the reference
// accepts in a template's bank section.
var retainExtractionModes = []string{"concise", "verbose", "custom", "verbatim", "chunks"}

// retainExtractionMode reports whether the mode is one the engine implements.
func retainExtractionMode(mode string) bool {
	for _, known := range retainExtractionModes {
		if known == mode {
			return true
		}
	}
	return false
}

// dryRunResponse reports what an import would create without touching storage.
func dryRunResponse(bankID string, manifest templateManifest) BankTemplateImportResponse {
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

// applyTemplateManifest creates or updates the manifest's config, mental models
// and directives against the bank, creating the bank when it does not exist.
func (h *Handlers) applyTemplateManifest(ctx context.Context, bankID string, manifest templateManifest) (BankTemplateImportResponse, error) {
	if _, err := h.Store.GetBank(ctx, bankID); err != nil {
		if _, createErr := h.Store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: bankID}); createErr != nil && !errors.Is(createErr, storage.ErrBankExists) {
			return BankTemplateImportResponse{}, createErr
		}
	}

	result := emptyTemplateResponse(bankID)

	if len(nonNilValues(manifest.Bank)) > 0 {
		if _, err := h.Store.UpdateBank(ctx, bankID, templateBankPatch(manifest.Bank)); err != nil {
			return BankTemplateImportResponse{}, err
		}
		result.ConfigApplied = true
	}

	if len(manifest.MentalModels) > 0 {
		existing, err := h.Store.ListMentalModels(ctx, bankID, "")
		if err != nil {
			return BankTemplateImportResponse{}, err
		}
		byID := make(map[string]storage.MentalModel, len(existing))
		for _, model := range existing {
			byID[model.ID] = model
		}
		for _, model := range manifest.MentalModels {
			maxTokens := model.MaxTokens
			if maxTokens == 0 {
				maxTokens = 2048
			}
			if current, ok := byID[model.ID]; ok {
				if modelMatchesManifest(current, model, maxTokens) {
					// An unchanged definition is left alone and, crucially, not
					// re-queued: applying the same manifest again is a sync, not
					// a rewrite, and regenerating the model would spend a reflect
					// loop on a document that has not changed.
					continue
				}
				// A resource already on the bank is updated in place; the
				// manifest's id is both the match key and the model's key.
				patch := storage.MentalModelPatch{
					Name:        &model.Name,
					SourceQuery: &model.SourceQuery,
					Tags:        &model.Tags,
					MaxTokens:   &maxTokens,
				}
				if model.Trigger != nil {
					trigger := model.Trigger
					patch.Trigger = &trigger
				}
				if _, err := h.Store.UpdateMentalModel(ctx, bankID, model.ID, patch); err != nil {
					return BankTemplateImportResponse{}, err
				}
				result.MentalModelsUpdated = append(result.MentalModelsUpdated, model.ID)
			} else {
				if _, err := h.Store.PutMentalModel(ctx, storage.MentalModel{
					ID:          model.ID,
					BankID:      bankID,
					Subtype:     storage.SubtypePinned,
					Name:        model.Name,
					SourceQuery: model.SourceQuery,
					Tags:        model.Tags,
					MaxTokens:   maxTokens,
					Trigger:     model.Trigger,
				}); err != nil {
					return BankTemplateImportResponse{}, err
				}
				result.MentalModelsCreated = append(result.MentalModelsCreated, model.ID)
			}
			opID, err := h.enqueueTemplateModelRefresh(ctx, bankID, model.ID)
			if err != nil {
				return BankTemplateImportResponse{}, err
			}
			result.OperationIDs = append(result.OperationIDs, opID)
		}
	}

	if len(manifest.Directives) > 0 {
		existing, err := h.Store.ListDirectives(ctx, bankID)
		if err != nil {
			return BankTemplateImportResponse{}, err
		}
		byName := make(map[string]storage.Directive, len(existing))
		for _, directive := range existing {
			byName[directive.Name] = directive
		}
		for _, directive := range manifest.Directives {
			tags := directive.Tags
			if tags == nil {
				tags = []string{}
			}
			if current, ok := byName[directive.Name]; ok {
				if directiveMatchesManifest(current, directive, tags) {
					// Unchanged: nothing to write and nothing to report.
					continue
				}
				content, priority, active := directive.Content, directive.Priority, directive.IsActive
				if _, err := h.Store.UpdateDirective(ctx, bankID, current.ID, storage.DirectivePatch{
					Content: &content, Priority: &priority, IsActive: &active, Tags: &tags,
				}); err != nil {
					return BankTemplateImportResponse{}, err
				}
				result.DirectivesUpdated = append(result.DirectivesUpdated, directive.Name)
			} else {
				if _, err := h.Store.PutDirective(ctx, storage.Directive{
					ID:       newTemplateID(),
					BankID:   bankID,
					Name:     directive.Name,
					Content:  directive.Content,
					Priority: directive.Priority,
					IsActive: directive.IsActive,
					Tags:     tags,
				}); err != nil {
					return BankTemplateImportResponse{}, err
				}
				result.DirectivesCreated = append(result.DirectivesCreated, directive.Name)
			}
		}
	}

	return result, nil
}

// enqueueTemplateModelRefresh queues a regeneration of one imported mental
// model and returns the operation identifier the caller polls.
func (h *Handlers) enqueueTemplateModelRefresh(ctx context.Context, bankID, modelID string) (string, error) {
	result, err := h.Store.EnqueueOperation(ctx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          map[string]any{storage.MentalModelIDPayloadKey: modelID},
			SerializationKey: storage.MentalModelSerializationKey(modelID),
		},
		DedupeByPayloadKey: storage.MentalModelIDPayloadKey,
	})
	if err != nil {
		return "", err
	}
	return result.Operation.OperationID, nil
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

	manifest := templateManifest{Version: currentTemplateVersion}
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
		manifest.MentalModels = append(manifest.MentalModels, templateMentalModel{
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
		manifest.Directives = append(manifest.Directives, templateDirective{
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

// nonNilValues drops keys whose value is JSON null, mirroring the reference's
// "only explicitly set fields" projection.
func nonNilValues(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		if value != nil {
			out[key] = value
		}
	}
	return out
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

// templateBankPatch splits a manifest's bank section into the bank patch the
// import applies. The disposition traits and the reflect mission live on the
// bank row rather than in the override map, so they are routed to their own
// columns; every other key is a configuration override.
func templateBankPatch(updates map[string]any) storage.BankPatch {
	patch := storage.BankPatch{}
	traits := map[string]int{}
	config := map[string]any{}
	for key, value := range updates {
		switch key {
		case "disposition_skepticism", "disposition_literalism", "disposition_empathy":
			if raw, ok := asIntValue(value); ok {
				traits[strings.TrimPrefix(key, "disposition_")] = raw
			}
		case "reflect_mission":
			if text, ok := value.(string); ok {
				mission := text
				patch.Mission = &mission
			}
		default:
			config[key] = value
		}
	}
	if len(traits) > 0 {
		patch.Disposition = &traits
	}
	if len(config) > 0 {
		patch.Config = &config
	}
	return patch
}

// modelMatchesManifest reports whether a stored mental model already carries
// the definition a manifest names, so an unchanged apply can leave it alone.
func modelMatchesManifest(current storage.MentalModel, model templateMentalModel, maxTokens int) bool {
	if current.Name != model.Name || current.SourceQuery != model.SourceQuery {
		return false
	}
	if current.MaxTokens != maxTokens {
		return false
	}
	if !equalStringSlices(current.Tags, model.Tags) {
		return false
	}
	return sameTrigger(current.Trigger, model.Trigger)
}

// directiveMatchesManifest reports whether a stored directive already carries
// the definition a manifest names.
func directiveMatchesManifest(current storage.Directive, directive templateDirective, tags []string) bool {
	return current.Content == directive.Content &&
		current.Priority == directive.Priority &&
		current.IsActive == directive.IsActive &&
		equalStringSlices(current.Tags, tags)
}

// sameTrigger compares two trigger maps treating an absent map as empty, so a
// model stored without a trigger matches a manifest that names none.
func sameTrigger(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// equalStringSlices compares two string slices treating nil and empty as equal,
// since a stored empty list and an omitted one mean the same thing.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// asIntValue decodes a JSON number that arrived as float64 (or a Go int) into
// an int, reporting whether the value was numeric.
func asIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

// atoiTemplate parses a trusted constant version.
func atoiTemplate(raw string) int {
	value := 0
	for _, char := range raw {
		if char < '0' || char > '9' {
			break
		}
		value = value*10 + int(char-'0')
	}
	return value
}

// newTemplateID builds an opaque identifier for a directive the manifest did
// not name one for.
func newTemplateID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "tmpl-directive"
	}
	return hex.EncodeToString(raw[:])
}

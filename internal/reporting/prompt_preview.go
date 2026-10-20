package reporting

import (
	"embed"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/consolidation"
	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/extraction"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/reflect"
)

// promptPreviewOperations lists the operations the preview can render, in the
// order the reference reports them.
var promptPreviewOperations = []string{"retain", "consolidation", "reflect"}

// retainExtractionModes is the extraction-mode enum the preview reports as the
// retain_extraction_mode block's choices.
var retainExtractionModes = []string{"concise", "verbose", "custom", "verbatim", "chunks"}

// extractionResponseSchemas holds the retain extraction response's JSON schema,
// one file per variant the deployment config can select, exactly as the
// reference serves it. The reference derives the schema from its extraction
// response model's pydantic model_json_schema(), and the model it picks depends
// on the config (extraction mode, causal links, optional dimensions), not on the
// request — so a preview cannot build the document itself and a static copy per
// variant is the only way to answer with the same bytes.
//
// Generated with the reference's own interpreter, one invocation per
// (mode, causal, optional-dimensions) combination:
//
//	python3 -c 'from hindsight_api.engine.retain import fact_extraction as fe; \
//	  import json; print(json.dumps(fe.FactExtractionResponse.model_json_schema(), separators=(",",":")))'
//
// and, for the optional-dimensions variants, wrapping the fact class with
// fe._with_optional_dimensions exactly as fe._build_extraction_prompt_and_schema
// does. The files are compact (no insignificant whitespace), so the bytes on the
// wire equal the bytes on disk.
//
//go:embed extraction_schemas
var extractionResponseSchemas embed.FS

// retainResponseSchemaVariant names the embedded document for a resolved retain
// config, mirroring the reference's selection: verbatim mode declares the
// verbatim model and never carries causal relations; otherwise a causal-links
// deployment uses the verbose model in verbose mode and the default model
// everywhere else, while a causal-less deployment uses the no-causal model.
// Optional fact dimensions wrap whichever model was chosen.
func retainResponseSchemaVariant(mode string, causal, optionalDimensions bool) string {
	name := "retain"
	switch mode {
	case extraction.ModeVerbatim:
		name = "retain_verbatim"
	case extraction.ModeVerbose:
		if causal {
			name = "retain_verbose"
		} else {
			name = "retain_no_causal"
		}
	default:
		if !causal {
			name = "retain_no_causal"
		}
	}
	if optionalDimensions {
		name += "_optional_dimensions"
	}
	return name
}

// retainResponseSchema returns the committed schema document for a resolved
// retain config. A missing document is a programming error rather than a
// runtime condition, so it panics rather than serving a preview without one.
func retainResponseSchema(mode string, causal, optionalDimensions bool) json.RawMessage {
	name := retainResponseSchemaVariant(mode, causal, optionalDimensions)
	data, err := extractionResponseSchemas.ReadFile("extraction_schemas/" + name + ".json")
	if err != nil {
		panic("reporting: missing extraction response schema " + name + ": " + err.Error())
	}
	return json.RawMessage(data)
}

// chunksModeExplanation is the skipped_reason the reference returns when the
// bank's extraction mode stores chunks verbatim and sends no prompt.
const chunksModeExplanation = "Chunks mode stores each chunk verbatim as its own memory and never calls an LLM, " +
	"so retain sends no prompt at all. Entity labels, the mission and the custom instructions have no effect " +
	"in this mode. Switch to concise, verbose, verbatim or custom to see a prompt."

// previewConfigurableFields is the set of fields a bank may override — the
// reference's own config-layer allowlist. A block (or run setting) whose field
// is not here is report-only: a UI that offered to edit it would only collect a
// rejection.
var previewConfigurableFields = map[string]bool{
	"mcp_enabled_tools":                                     true,
	"audit_log_enabled":                                     true,
	"store_document_text":                                   true,
	"retain_chunk_size":                                     true,
	"retain_structured_chunk_size":                          true,
	"retain_extraction_mode":                                true,
	"retain_mission":                                        true,
	"retain_custom_instructions":                            true,
	"retain_default_strategy":                               true,
	"retain_strategies":                                     true,
	"retain_chunk_batch_size":                               true,
	"retain_max_attachments_per_chunk":                      true,
	"entity_labels":                                         true,
	"entities_allow_free_form":                              true,
	"enable_text_search":                                    true,
	"enable_temporal_retrieval":                             true,
	"enable_graph_retrieval":                                true,
	"enable_reranking":                                      true,
	"enable_observations":                                   true,
	"enable_auto_consolidation":                             true,
	"consolidation_llm_batch_size":                          true,
	"consolidation_llm_parallelism":                         true,
	"consolidation_max_memories_per_round":                  true,
	"consolidation_source_facts_max_tokens":                 true,
	"consolidation_source_facts_max_tokens_per_observation": true,
	"observations_mission":                                  true,
	"max_observations_per_scope":                            true,
	"observation_scope_limits":                              true,
	"consolidation_strategies":                              true,
	"mental_model_min_refresh_interval_seconds":             true,
	"knowledge_page_default_trigger":                        true,
	"reflect_mission":                                       true,
	"reflect_source_facts_max_tokens":                       true,
	"reflect_default_options":                               true,
	"recall_include_chunks":                                 true,
	"recall_max_tokens":                                     true,
	"recall_chunks_max_tokens":                              true,
	"recall_budget_function":                                true,
	"recall_budget_fixed_low":                               true,
	"recall_budget_fixed_mid":                               true,
	"recall_budget_fixed_high":                              true,
	"recall_budget_adaptive_low":                            true,
	"recall_budget_adaptive_mid":                            true,
	"recall_budget_adaptive_high":                           true,
	"recall_budget_min":                                     true,
	"recall_budget_max":                                     true,
	"disposition_skepticism":                                true,
	"disposition_literalism":                                true,
	"disposition_empathy":                                   true,
	"llm_gemini_safety_settings":                            true,
	"memory_defense":                                        true,
}

// promptHeadingRe matches a section heading: a line fenced by box-drawing rules
// (how the retain and consolidation prompts write them) or a markdown `##`/`###`
// heading (how the reflect prompt does).
var promptHeadingRe = regexp.MustCompile(`(?m)^(?:═+\s*\n([^\n═][^\n]*)\n═+\s*|#{2,3} +([^\n]+))$`)

// promptHeading extracts the section heading a block of prompt text carries, or
// "" if it has none. It is trimmed at the first dash or bracket and title-cased,
// since the fenced form is shouted.
func promptHeading(text string) string {
	match := promptHeadingRe.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	raw := match[1]
	if raw == "" {
		raw = match[2]
	}
	raw = strings.TrimSpace(raw)
	if cut := headingCut(raw); cut >= 0 {
		raw = strings.TrimSpace(raw[:cut])
	}
	if raw == "" {
		return ""
	}
	runes := []rune(raw)
	return strings.ToUpper(string(runes[0])) + strings.ToLower(string(runes[1:]))
}

// headingCut reports the byte offset of the first whitespace-followed-by-dash-
// or-bracket sequence, or -1.
func headingCut(raw string) int {
	runes := []rune(raw)
	for i := range len(runes) - 1 {
		if runes[i] != ' ' && runes[i] != '\t' {
			continue
		}
		switch runes[i+1] {
		case '-', '–', '(':
			return len(string(runes[:i]))
		}
	}
	return -1
}

// previewSetting builds a block produced by a setting. It is inactive when it
// contributes no text.
func previewSetting(field, text string, value *string, kind string, choices []string) PromptBlockModel {
	return PromptBlockModel{
		Text: text, Source: "config", Field: field, Active: text != "",
		Value: value, Kind: kind, Choices: choices,
	}
}

// previewNamed builds a built-in block the preview names itself, because no
// single field owns it.
func previewNamed(section, text string, active bool) PromptBlockModel {
	return PromptBlockModel{Text: text, Source: "builtin", Section: section, Active: active, Kind: "text"}
}

// previewLanguageBlocks reports whichever half of the output-language setting is
// actually present — the directive when a language is configured, the default
// rule otherwise, and nothing when neither exists.
func previewLanguageBlocks(outputLanguage, defaultRule string) []PromptBlockModel {
	language := strings.TrimSpace(outputLanguage)
	if language == "" && defaultRule == "" {
		return nil
	}
	if language != "" {
		value := language
		return []PromptBlockModel{previewSetting("llm_output_language",
			strings.TrimSpace(provider.OutputLanguageDirective(language)), &value, "text", nil)}
	}
	return []PromptBlockModel{previewSetting("llm_output_language", defaultRule, nil, "text", nil)}
}

// previewLabelsPieces splits the entity-labels section around the line the
// free-form flag decides, attributing head and tail to entity_labels and the
// line itself to entities_allow_free_form.
func previewLabelsPieces(section string, labelsValue *string, freeForm bool, freeFormLine string) []PromptBlockModel {
	labels := previewSetting("entity_labels", "", labelsValue, "complex", nil)
	if section == "" {
		return []PromptBlockModel{labels}
	}
	head, tail, found := strings.Cut(section, freeFormLine)
	if !found {
		labels.Text = section
		labels.Active = true
		return []PromptBlockModel{labels}
	}
	headBlock := labels
	headBlock.Text = head
	headBlock.Active = head != ""
	tailBlock := labels
	tailBlock.Text = tail
	tailBlock.Active = tail != ""
	value := strconv.FormatBool(freeForm)
	return []PromptBlockModel{
		headBlock,
		previewSetting("entities_allow_free_form", freeFormLine, &value, "boolean", nil),
		tailBlock,
	}
}

// partitionBlocks cuts text into blocks, one per piece, with the gaps kept as
// built-ins. The active blocks concatenate back to text exactly.
func partitionBlocks(text string, pieces []PromptBlockModel) []PromptBlockModel {
	type pending struct {
		anchor int
		block  PromptBlockModel
	}
	var matches []previewSpan
	var pend []pending
	cursor := 0
	for _, piece := range pieces {
		if piece.Text == "" {
			if piece.Field != "" || piece.Section != "" {
				pend = append(pend, pending{len(matches), piece})
			}
			continue
		}
		index := strings.Index(text[cursor:], piece.Text)
		if index == -1 {
			fallback := strings.Index(text, piece.Text)
			if fallback == -1 || overlapsAny(matches, fallback, fallback+len(piece.Text)) {
				continue
			}
			index = fallback
		} else {
			index += cursor
		}
		matches = append(matches, previewSpan{index, index + len(piece.Text), piece})
		cursor = index + len(piece.Text)
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].start < matches[j].start })

	var blocks []PromptBlockModel
	addGap := func(gap string) {
		if gap == "" {
			return
		}
		if strings.TrimSpace(gap) == "" && len(blocks) > 0 {
			blocks[len(blocks)-1].Text += gap
			return
		}
		blocks = append(blocks, PromptBlockModel{
			Text: gap, Source: "builtin", Active: true, Kind: "text", Heading: promptHeading(gap),
		})
	}
	cursor = 0
	for matched, m := range matches {
		addGap(text[cursor:m.start])
		blocks = append(blocks, m.block)
		cursor = m.end
		for _, p := range pend {
			if p.anchor == matched+1 {
				blocks = append(blocks, p.block)
			}
		}
	}
	addGap(text[cursor:])
	lead := make([]PromptBlockModel, 0, len(blocks))
	for _, p := range pend {
		if p.anchor == 0 {
			lead = append(lead, p.block)
		}
	}
	return append(lead, blocks...)
}

// previewSpan is one located piece of a partitioned message.
type previewSpan struct {
	start, end int
	block      PromptBlockModel
}

// overlapsAny reports whether [start,end) overlaps any recorded match.
func overlapsAny(matches []previewSpan, start, end int) bool {
	for _, m := range matches {
		if start < m.end && m.start < end {
			return true
		}
	}
	return false
}

// resolvePreviewConfig layers the bank's overrides over the tenant and process
// layers, the way the configuration read resolves them: the result carries the
// effective value of every configurable field.
func (h *Handlers) resolvePreviewConfig(r *http.Request, bankID string) map[string]any {
	overrides, err := h.Store.BankConfig(r.Context(), bankID)
	if err != nil {
		overrides = nil
	}
	return config.ResolveLayers(overrides, httpx.TenantOverridesFrom(r), h.ProcessDefaults)
}

// applyRetainStrategy applies a named retain strategy's overrides on top of a
// resolved config, filtered to the fields a bank may override. An unknown
// strategy name leaves the config unchanged.
func applyRetainStrategy(resolved map[string]any, name string) map[string]any {
	strategies, ok := resolved["retain_strategies"].(map[string]any)
	if !ok {
		return resolved
	}
	overrides, ok := strategies[name].(map[string]any)
	if !ok {
		return resolved
	}
	filtered := make(map[string]any, len(overrides))
	for key, value := range overrides {
		if previewConfigurableFields[key] {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return resolved
	}
	out := make(map[string]any, len(resolved)+len(filtered))
	for key, value := range resolved {
		out[key] = value
	}
	for key, value := range filtered {
		out[key] = value
	}
	return out
}

// renderPromptPreview renders one operation's messages from the same builders
// the real operation calls, then partitions each message into per-setting blocks
// the way the reference reports them.
func (h *Handlers) renderPromptPreview(r *http.Request, bankID, operation, strategy string) PromptPreviewResponse {
	outputLanguage := strings.TrimSpace(h.LLMOutputLanguage)
	config := h.resolvePreviewConfig(r, bankID)

	switch operation {
	case "retain":
		return h.renderRetainPreview(config, outputLanguage, strategy)
	case "consolidation":
		return h.renderConsolidationPreview(config, outputLanguage)
	default:
		return h.renderReflectPreview(r, bankID, config, outputLanguage)
	}
}

// renderRetainPreview renders the retain messages under the effective strategy.
func (h *Handlers) renderRetainPreview(bankConfig map[string]any, outputLanguage, strategy string) PromptPreviewResponse {
	effectiveStrategy := strategy
	if effectiveStrategy == "" {
		effectiveStrategy, _ = bankConfig["retain_default_strategy"].(string)
	}
	resolved := applyRetainStrategy(bankConfig, effectiveStrategy)

	mode := previewString(resolved, "retain_extraction_mode")
	if mode == "" {
		mode = "concise"
	}
	runSettings := []RunSettingModel{
		{Field: "retain_chunk_size", Value: previewValueText(resolved["retain_chunk_size"]), Kind: "text"},
		{Field: "retain_structured_chunk_size", Value: previewValueText(resolved["retain_structured_chunk_size"]), Kind: "text"},
	}
	if mode == "chunks" {
		reason := chunksModeExplanation
		messages := []PromptPreviewMessage{{
			Role:   "system",
			Blocks: []PromptBlockModel{previewSetting("retain_extraction_mode", "", &mode, "choice", retainExtractionModes)},
		}}
		return PromptPreviewResponse{
			Messages: messages, RunSettings: runSettings, SkippedReason: &reason,
			Strategies: previewStrategyNames(resolved), Strategy: previewStrategy(strategy, bankConfig),
		}
	}

	mission := previewString(resolved, "retain_mission")
	instructions := previewString(resolved, "retain_custom_instructions")
	optionalDimensions := previewBool(resolved, "retain_optional_fact_dimensions", h.RetainOptionalFactDimensions)
	freeForm := previewBool(resolved, "entities_allow_free_form", true)
	causal := true
	if h.RetainExtractCausalLinks != nil {
		causal = *h.RetainExtractCausalLinks
	}
	vocabulary, _ := entitylabels.Parse(resolved["entity_labels"])

	opts := extraction.Options{
		Mode:                   mode,
		Mission:                mission,
		Instructions:           instructions,
		OptionalFactDimensions: optionalDimensions,
		Vocabulary:             vocabulary,
		AllowFreeFormEntities:  &freeForm,
		ExtractCausalLinks:     &causal,
	}
	systemPrompt := extraction.PreviewSystemPrompt(opts, outputLanguage)
	userMessage := extraction.PreviewUserMessage(opts)
	// The reference reports its extraction response model's pydantic JSON schema
	// verbatim. A configured label vocabulary makes it build a bank-specific
	// dynamic model instead, which has no committed document; there the preview
	// falls back to the equivalent shape wenyuan renders from the vocabulary.
	var schema any
	if vocabulary.Empty() {
		schema = retainResponseSchema(mode, causal, optionalDimensions)
	} else {
		schema = extraction.PreviewSchemaFor(opts)
	}

	causalText := ""
	if causal {
		causalText = strings.TrimSpace(extraction.CausalSection)
	}
	optionalText := ""
	if optionalDimensions {
		optionalText = strings.TrimSpace(extraction.OptionalDimensionsSection)
	}
	systemPieces := append([]PromptBlockModel{}, previewLanguageBlocks(outputLanguage, extraction.DefaultLanguageRule)...)
	if mode == "custom" {
		systemPieces = append(systemPieces, previewSetting("retain_custom_instructions", instructions,
			optionalPreviewValue(resolved["retain_custom_instructions"]), "text", nil))
	}
	systemPieces = append(systemPieces,
		previewSetting("retain_extract_causal_links", causalText, previewValueText(strconv.FormatBool(causal)), "boolean", nil),
		previewSetting("retain_optional_fact_dimensions", optionalText, previewValueText(strconv.FormatBool(optionalDimensions)), "boolean", nil),
	)
	systemPieces = append(systemPieces, previewLabelsPieces(
		strings.TrimSpace(extraction.LabelsPromptSection(vocabulary, freeForm)),
		previewLabelsValue(resolved["entity_labels"]),
		freeForm,
		extraction.BuildFreeFormEntitiesInstruction(freeForm),
	)...)

	missionText := ""
	if strings.TrimSpace(mission) != "" {
		missionText = strings.TrimSpace(extraction.PreviewMissionPreamble(mission))
	}
	userPieces := []PromptBlockModel{
		previewSetting("retain_mission", missionText, optionalPreviewValue(resolved["retain_mission"]), "text", nil),
	}

	systemBlocks := partitionBlocks(systemPrompt, systemPieces)
	for i := range systemBlocks {
		if systemBlocks[i].Source == "builtin" {
			systemBlocks[i].Field = "retain_extraction_mode"
			systemBlocks[i].Value = &mode
			systemBlocks[i].Kind = "choice"
			systemBlocks[i].Choices = retainExtractionModes
		}
	}
	messages := []PromptPreviewMessage{
		{Role: "system", Blocks: systemBlocks},
		{Role: "user", Blocks: partitionBlocks(userMessage, userPieces)},
	}
	return PromptPreviewResponse{
		Messages:       messages,
		ResponseSchema: schema,
		RunSettings:    runSettings,
		Strategies:     previewStrategyNames(resolved),
		Strategy:       previewStrategy(strategy, bankConfig),
	}
}

// renderConsolidationPreview renders the consolidation messages from the bank's
// resolved settings.
func (h *Handlers) renderConsolidationPreview(bankConfig map[string]any, outputLanguage string) PromptPreviewResponse {
	mission := previewString(bankConfig, "observations_mission")
	systemPrompt := consolidation.PreviewSystemPrompt(outputLanguage)
	userMessage := consolidation.PreviewInput(mission)
	systemPieces := previewLanguageBlocks(outputLanguage, consolidation.DefaultLanguageRule)
	userPieces := []PromptBlockModel{
		previewSetting("observations_mission", consolidation.MissionSection(mission),
			optionalPreviewValue(bankConfig["observations_mission"]), "text", nil),
	}
	return PromptPreviewResponse{
		Messages: []PromptPreviewMessage{
			{Role: "system", Blocks: partitionBlocks(systemPrompt, systemPieces)},
			{Role: "user", Blocks: partitionBlocks(userMessage, userPieces)},
		},
		RunSettings: []RunSettingModel{},
		Strategies:  []string{},
	}
}

// renderReflectPreview renders the reflect messages from the bank's profile and
// its directives.
func (h *Handlers) renderReflectPreview(r *http.Request, bankID string, bankConfig map[string]any, outputLanguage string) PromptPreviewResponse {
	profile := reflect.PreviewProfile{}
	if bank, err := h.Store.GetBank(r.Context(), bankID); err == nil {
		profile.Name = bank.Name
		profile.Mission = bank.Mission
		profile.Disposition = bank.NormalizedDisposition()
	}
	overlayDispositionOverrides(profile.Disposition, bankConfig)
	if reflectMission := previewString(bankConfig, "reflect_mission"); reflectMission != "" {
		profile.Mission = reflectMission
	}
	directives := h.directivesFor(r.Context(), bankID)

	directivesText := ""
	if len(directives) > 0 {
		directivesText = strings.TrimSpace(reflect.DirectivesSection(directives))
	}
	systemPieces := []PromptBlockModel{
		previewNamed("bank_identity", reflect.BankNameLine(profile), true),
		previewNamed("disposition", reflect.BankDispositionLine(profile), reflect.BankDispositionLine(profile) != ""),
		previewNamed("directives", directivesText, len(directives) > 0),
		previewSetting("reflect_mission", strings.TrimSpace(profile.Mission),
			optionalPreviewValue(bankConfig["reflect_mission"]), "text", nil),
	}
	systemPieces = append(systemPieces, previewLanguageBlocks(outputLanguage, reflect.ToolsLanguageRule)...)

	userPieces := previewLanguageBlocks(outputLanguage, "")
	systemPrompt := reflect.PreviewSystemPrompt(profile, directives, outputLanguage)
	userPrompt := reflect.AgentUserPrompt(reflect.PlaceholderQuestion, outputLanguage)

	return PromptPreviewResponse{
		Messages: []PromptPreviewMessage{
			{Role: "system", Blocks: partitionBlocks(systemPrompt, systemPieces)},
			{Role: "user", Blocks: partitionBlocks(userPrompt, userPieces)},
		},
		RunSettings: []RunSettingModel{},
		Strategies:  []string{},
	}
}

// previewStrategy echoes the strategy that actually applied: the one the caller
// named, or the bank's default when none was given.
func previewStrategy(requested string, bankConfig map[string]any) *string {
	if requested != "" {
		value := requested
		return &value
	}
	if value, ok := bankConfig["retain_default_strategy"].(string); ok && value != "" {
		return &value
	}
	return nil
}

// previewStrategyNames lists the bank's configured retain strategy names, sorted.
func previewStrategyNames(bankConfig map[string]any) []string {
	strategies, ok := bankConfig["retain_strategies"].(map[string]any)
	if !ok || len(strategies) == 0 {
		return []string{}
	}
	names := make([]string, 0, len(strategies))
	for name := range strategies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// overlayDispositionOverrides applies the bank's configured disposition traits
// on top of the stored profile, the way the profile read resolves them.
func overlayDispositionOverrides(disposition map[string]int, bankConfig map[string]any) {
	if disposition == nil {
		return
	}
	for field, trait := range map[string]string{
		"disposition_skepticism": "skepticism",
		"disposition_literalism": "literalism",
		"disposition_empathy":    "empathy",
	} {
		if value, ok := previewInt(bankConfig[field]); ok {
			disposition[trait] = value
		}
	}
}

// previewInt reads a config number that may arrive as a JSON float or a Go int.
func previewInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}

// previewString reads a string setting, treating an absent or non-string value
// as empty.
func previewString(bankConfig map[string]any, key string) string {
	value, _ := bankConfig[key].(string)
	return strings.TrimSpace(value)
}

// previewBool reads a boolean setting, falling back when it is absent.
func previewBool(bankConfig map[string]any, key string, fallback bool) bool {
	switch value := bankConfig[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			return true
		case "false":
			return false
		}
	}
	return fallback
}

// previewValueText renders a setting's value the way the reference's str() does
// for the reported blocks, or nil when the value is absent.
func previewValueText(value any) *string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return &typed
	case bool:
		text := strconv.FormatBool(typed)
		return &text
	case int:
		text := strconv.Itoa(typed)
		return &text
	case int64:
		text := strconv.FormatInt(typed, 10)
		return &text
	case float64:
		if typed == float64(int64(typed)) {
			text := strconv.FormatInt(int64(typed), 10)
			return &text
		}
		text := strconv.FormatFloat(typed, 'g', -1, 64)
		return &text
	default:
		return nil
	}
}

// optionalPreviewValue renders a nullable setting's value, keeping an explicit
// null distinct from an empty string.
func optionalPreviewValue(value any) *string {
	if value == nil {
		return nil
	}
	return previewValueText(value)
}

// previewLabelsValue encodes the entity_labels setting as JSON the way the
// reference reports it, or nil when no labels are configured.
func previewLabelsValue(value any) *string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return &typed
	case []any:
		if len(typed) == 0 {
			return nil
		}
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	text := pythonStyleJSON(string(encoded))
	return &text
}

// pythonStyleJSON re-spaces a compact JSON document the way Python's json.dumps
// does by default (", " between items and ": " after a key), leaving string
// literals untouched.
func pythonStyleJSON(compact string) string {
	var builder strings.Builder
	builder.Grow(len(compact))
	inString, escaped := false, false
	for i := range len(compact) {
		ch := compact[i]
		builder.WriteByte(ch)
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case ':', ',':
			builder.WriteByte(' ')
		}
	}
	return builder.String()
}

// applyPreviewEditability marks every field on the config-layer allowlist as
// editable, so a UI can tell which blocks it may offer to change.
func applyPreviewEditability(response *PromptPreviewResponse) {
	for i := range response.Messages {
		for j := range response.Messages[i].Blocks {
			response.Messages[i].Blocks[j].Editable = previewConfigurableFields[response.Messages[i].Blocks[j].Field]
		}
	}
	for i := range response.RunSettings {
		response.RunSettings[i].Editable = previewConfigurableFields[response.RunSettings[i].Field]
	}
}

// previewOperation resolves the request's operation: an absent field takes the
// reference's default, and a present one must be a member of the Literal enum.
// It returns the operation and, on failure, the offending value to report.
func previewOperation(obj map[string]json.RawMessage) (string, any, bool) {
	raw, present := obj["operation"]
	if !present {
		return "retain", nil, true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		value = nil
	}
	text, ok := value.(string)
	if !ok {
		return "", value, false
	}
	for _, allowed := range promptPreviewOperations {
		if text == allowed {
			return text, nil, true
		}
	}
	return "", value, false
}

// previewStrategyValue extracts the optional strategy from the request body,
// keeping a non-string value out of the report.
func previewStrategyValue(obj map[string]json.RawMessage) string {
	raw, present := obj["strategy"]
	if !present {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// writeLiteralOperationFailure emits the reference's Literal rejection envelope.
func writeLiteralOperationFailure(w http.ResponseWriter, input any) {
	httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Literal([]string{"body", "operation"}, input, promptPreviewOperations),
	}})
}

// previewStrategyFailure reports the item the reference's model produces for a
// `strategy` that is neither absent, null, nor a string.
func previewStrategyFailure(obj map[string]json.RawMessage) *httpx.ValidationFailure {
	raw, present := obj["strategy"]
	if !present {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	switch value.(type) {
	case nil, string:
		return nil
	}
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValidationItem("string_type", []string{"body", "strategy"},
			"Input should be a valid string", value, nil),
	}}
}

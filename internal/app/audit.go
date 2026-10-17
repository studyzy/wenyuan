package app

import (
	"encoding/json"
	"net/http"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// NewAuditLogger builds the deployment's audit logger from configuration. The
// switch defaults off, matching the reference; a bank may override it.
func NewAuditLogger(cfg config.Config, store storage.Store) *audit.Logger {
	return audit.NewLogger(store, cfg.AuditLogEnabled, cfg.AuditLogActions)
}

// auditedRoute maps a matched route to its audit action and the declared
// request model the audit trail dumps a captured body through (nil when the
// route takes no body, which is also how "do not capture" is expressed).
type auditedRoute struct {
	action string
	sample httpx.BodySample
}

// httpAuditRoutes is the HTTP route table's mirror of the reference's
// @audited decorators: the same action names, keyed by the method and path
// template the router matches.
var httpAuditRoutes = buildHTTPAuditRoutes()

func buildHTTPAuditRoutes() map[string]auditedRoute {
	bankMember := httpx.BankMemberPath
	bank := httpx.BankPath

	routes := map[string]auditedRoute{}
	add := func(method, pattern, action string, sample httpx.BodySample) {
		routes[method+" "+pattern] = auditedRoute{action: action, sample: sample}
	}

	// Memory operations.
	add(http.MethodPost, bank(httpx.SuffixMemories), audit.ActionRetain, sampleRetain)
	add(http.MethodDelete, bank(httpx.SuffixMemories), audit.ActionClearMemories, nil)
	add(http.MethodPost, bank(httpx.SuffixFileRetain), audit.ActionFileRetain, nil)
	add(http.MethodPost, bank(httpx.SuffixRecall), audit.ActionRecall, sampleRecall)
	add(http.MethodPost, bank(httpx.SuffixReflect), audit.ActionReflect, sampleReflect)
	add(http.MethodPatch, bank(httpx.SuffixMemoryMember), audit.ActionUpdateMemory, sampleUpdateMemory)

	// Async operations.
	add(http.MethodDelete, bank(httpx.SuffixOperations)+"/{operation_id}", audit.ActionCancelOperation, nil)
	add(http.MethodDelete, bank(httpx.SuffixOperationMemberDelete), audit.ActionDeleteOperation, nil)
	add(http.MethodPost, bank(httpx.SuffixOperations)+"/{operation_id}/retry", audit.ActionRetryOperation, nil)

	// Bank lifecycle.
	add(http.MethodPut, bankMember(), audit.ActionCreateBank, sampleCreateBank)
	add(http.MethodPatch, bankMember(), audit.ActionUpdateBank, sampleCreateBank)
	add(http.MethodDelete, bankMember(), audit.ActionDeleteBank, nil)
	add(http.MethodPost, bank(httpx.SuffixAliases), audit.ActionCreateBankAlias, sampleCreateBankAlias)
	add(http.MethodPatch, bank(httpx.SuffixAliasMember), audit.ActionSetBankAliasPrimary, sampleSetBankAliasPrimary)
	add(http.MethodDelete, bank(httpx.SuffixAliasMember), audit.ActionDeleteBankAlias, nil)
	add(http.MethodPatch, bank(httpx.SuffixConfig), audit.ActionUpdateBankConfig, sampleBankConfigUpdate)
	add(http.MethodDelete, bank(httpx.SuffixConfig), audit.ActionResetBankConfig, nil)

	// Mental models.
	add(http.MethodPost, bank(httpx.SuffixMentalModels), audit.ActionCreateMentalModel, sampleCreateMentalModel)
	add(http.MethodPatch, bank(httpx.SuffixMentalModelMember), audit.ActionUpdateMentalModel, sampleUpdateMentalModel)
	add(http.MethodDelete, bank(httpx.SuffixMentalModelMember), audit.ActionDeleteMentalModel, nil)
	add(http.MethodPost, bank(httpx.SuffixMentalModelRefresh), audit.ActionRefreshMentalModel, nil)
	add(http.MethodPost, bank(httpx.SuffixMentalModelDryRun), audit.ActionDryRunRefreshMentalMod, nil)
	add(http.MethodPost, bank(httpx.SuffixMentalModelClear), audit.ActionClearMentalModel, nil)

	// Directives.
	add(http.MethodPost, bank(httpx.SuffixDirectives), audit.ActionCreateDirective, sampleCreateDirective)
	add(http.MethodPatch, bank(httpx.SuffixDirectiveMember), audit.ActionUpdateDirective, sampleUpdateDirective)
	add(http.MethodDelete, bank(httpx.SuffixDirectiveMember), audit.ActionDeleteDirective, nil)

	// Documents. The member subtree is registered as one pattern per method.
	add(http.MethodPatch, bank(httpx.SuffixDocumentMember), audit.ActionUpdateDocument, sampleUpdateDocument)
	add(http.MethodDelete, bank(httpx.SuffixDocumentMember), audit.ActionDeleteDocument, nil)
	add(http.MethodPost, bank(httpx.SuffixDocumentMember), audit.ActionReprocessDocument, nil)

	// Observations and consolidation.
	add(http.MethodDelete, bank(httpx.SuffixObservations), audit.ActionClearObservations, nil)
	add(http.MethodDelete, bank(httpx.SuffixMemoryObservations), audit.ActionClearMemoryObservations, nil)
	add(http.MethodPost, bank(httpx.SuffixConsolidateRecover), audit.ActionRecoverConsolidation, nil)
	add(http.MethodPost, bank(httpx.SuffixConsolidate), audit.ActionConsolidation, sampleConsolidation)

	// Webhooks.
	add(http.MethodPost, bank(httpx.SuffixWebhooks), audit.ActionCreateWebhook, sampleCreateWebhook)
	add(http.MethodPatch, bank(httpx.SuffixWebhookMember), audit.ActionUpdateWebhook, sampleUpdateWebhook)
	add(http.MethodDelete, bank(httpx.SuffixWebhookMember), audit.ActionDeleteWebhook, nil)

	// Archive transfer.
	add(http.MethodPost, bank(httpx.SuffixImport), audit.ActionImportBankTemplate, nil)
	add(http.MethodPost, bank(httpx.SuffixDocumentTransfer), audit.ActionImportDocuments, nil)
	add(http.MethodPost, bank(httpx.SuffixTransferImport), audit.ActionImportBankTransfer, nil)
	add(http.MethodPost, bank(httpx.SuffixClone), audit.ActionCloneBank, nil)

	return routes
}

// httpAuditAction resolves a matched route to its audit action and the declared
// request model its body is dumped through.
func httpAuditAction(method, pattern string) (string, bool, httpx.BodySample, bool) {
	route, ok := httpAuditRoutes[method+" "+pattern]
	if !ok {
		return "", false, nil, false
	}
	return route.action, route.sample != nil, route.sample, true
}

// Request models for the audit trail.
//
// The reference records the declared request model's dump for an audited body,
// not the bytes as sent: every field the model declares is present (an omitted
// one carrying its default, null for a nullable field) and any other field is
// dropped. Each sample below mirrors one route's request model, so decoding the
// captured body into it and marshalling it reproduces that dump. A nullable
// field maps to a pointer (or a nil slice, map, or json.RawMessage) so its zero
// value marshals as null; a field with a non-null default is preloaded by the
// route's sample constructor; and a nested model with a non-null default
// implements UnmarshalJSON to reload its defaults before decoding the caller's
// object, matching Pydantic's behaviour for a field the caller omitted.

// sampleRetain returns the model for POST .../memories.
func sampleRetain() any { return &retainSample{} }

// retainSample mirrors RetainRequest. Its dump key is "async_", the reference
// field's name, not the "async" alias its body accepts.
type retainSample struct {
	Items        []memoryItemSample `json:"items"`
	Async        bool               `json:"async_"`
	DocumentTags []string           `json:"document_tags"`
	OperationID  *string            `json:"operation_id"`
}

// memoryItemSample mirrors MemoryItem.
type memoryItemSample struct {
	Content           json.RawMessage     `json:"content"`
	Timestamp         *string             `json:"timestamp"`
	Context           *string             `json:"context"`
	Metadata          map[string]string   `json:"metadata"`
	DocumentID        *string             `json:"document_id"`
	Entities          []entityInputSample `json:"entities"`
	ResolveEntities   bool                `json:"resolve_entities"`
	Tags              []string            `json:"tags"`
	ObservationScopes json.RawMessage     `json:"observation_scopes"`
	Strategy          *string             `json:"strategy"`
	UpdateMode        *string             `json:"update_mode"`
}

// UnmarshalJSON reloads resolve_entities' true default before decoding.
func (s *memoryItemSample) UnmarshalJSON(data []byte) error {
	type plain memoryItemSample
	value := plain{ResolveEntities: true}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = memoryItemSample(value)
	return nil
}

// entityInputSample mirrors EntityInput.
type entityInputSample struct {
	Text string  `json:"text"`
	Type *string `json:"type"`
}

// sampleRecall returns the model for POST .../memories/recall.
func sampleRecall() any {
	return &recallSample{
		Budget:    "mid",
		MaxTokens: 4096,
		TagsMatch: "any",
		Include:   &includeSample{Entities: &entityIncludeSample{MaxTokens: 500}},
	}
}

// recallSample mirrors RecallRequest.
type recallSample struct {
	Query              string                `json:"query"`
	Types              []string              `json:"types"`
	PreferObservations bool                  `json:"prefer_observations"`
	Budget             string                `json:"budget"`
	MaxTokens          int                   `json:"max_tokens"`
	Trace              bool                  `json:"trace"`
	QueryTimestamp     *string               `json:"query_timestamp"`
	Include            *includeSample        `json:"include"`
	Tags               []string              `json:"tags"`
	TagsMatch          string                `json:"tags_match"`
	TagGroups          json.RawMessage       `json:"tag_groups"`
	MinScores          *minScoresSample      `json:"min_scores"`
	TemporalWindow     *temporalWindowSample `json:"temporal_window"`
}

// includeSample mirrors IncludeOptions, whose entities section defaults to a
// 500-token budget.
type includeSample struct {
	Entities    *entityIncludeSample      `json:"entities"`
	Chunks      *chunkIncludeSample       `json:"chunks"`
	SourceFacts *sourceFactsIncludeSample `json:"source_facts"`
}

// UnmarshalJSON reloads the entities default before decoding.
func (s *includeSample) UnmarshalJSON(data []byte) error {
	type plain includeSample
	value := plain{Entities: &entityIncludeSample{MaxTokens: 500}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = includeSample(value)
	return nil
}

// entityIncludeSample mirrors EntityIncludeOptions.
type entityIncludeSample struct {
	MaxTokens int `json:"max_tokens"`
}

// UnmarshalJSON reloads the 500-token default before decoding.
func (s *entityIncludeSample) UnmarshalJSON(data []byte) error {
	type plain entityIncludeSample
	value := plain{MaxTokens: 500}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = entityIncludeSample(value)
	return nil
}

// chunkIncludeSample mirrors ChunkIncludeOptions.
type chunkIncludeSample struct {
	MaxTokens int `json:"max_tokens"`
}

// UnmarshalJSON reloads the 8192-token default before decoding.
func (s *chunkIncludeSample) UnmarshalJSON(data []byte) error {
	type plain chunkIncludeSample
	value := plain{MaxTokens: 8192}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = chunkIncludeSample(value)
	return nil
}

// sourceFactsIncludeSample mirrors SourceFactsIncludeOptions.
type sourceFactsIncludeSample struct {
	MaxTokens               int `json:"max_tokens"`
	MaxTokensPerObservation int `json:"max_tokens_per_observation"`
}

// UnmarshalJSON reloads the reference's defaults before decoding.
func (s *sourceFactsIncludeSample) UnmarshalJSON(data []byte) error {
	type plain sourceFactsIncludeSample
	value := plain{MaxTokens: 4096, MaxTokensPerObservation: -1}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = sourceFactsIncludeSample(value)
	return nil
}

// minScoresSample mirrors MinScores.
type minScoresSample struct {
	Semantic *float64 `json:"semantic"`
	Keyword  *float64 `json:"keyword"`
	Reranker *float64 `json:"reranker"`
	Final    *float64 `json:"final"`
}

// temporalWindowSample mirrors TemporalWindow.
type temporalWindowSample struct {
	Start *string `json:"start"`
	End   *string `json:"end"`
}

// sampleReflect returns the model for POST .../reflect.
func sampleReflect() any {
	return &reflectSample{
		Budget:    "low",
		MaxTokens: 4096,
		TagsMatch: "any",
		Include:   &reflectIncludeSample{},
	}
}

// reflectSample mirrors ReflectRequest.
type reflectSample struct {
	ReflectSearchObservationsMaxTokens       *int                  `json:"reflect_search_observations_max_tokens"`
	ReflectSearchObservationsIncludeEntities *bool                 `json:"reflect_search_observations_include_entities"`
	Query                                    string                `json:"query"`
	Budget                                   string                `json:"budget"`
	Context                                  *string               `json:"context"`
	MaxTokens                                int                   `json:"max_tokens"`
	Include                                  *reflectIncludeSample `json:"include"`
	ResponseSchema                           map[string]any        `json:"response_schema"`
	Tags                                     []string              `json:"tags"`
	TagsMatch                                string                `json:"tags_match"`
	TagGroups                                json.RawMessage       `json:"tag_groups"`
	ApplyAllDirectives                       bool                  `json:"apply_all_directives"`
	FactTypes                                []string              `json:"fact_types"`
	ExcludeMentalModels                      bool                  `json:"exclude_mental_models"`
	ExcludeMentalModelIDs                    []string              `json:"exclude_mental_model_ids"`
}

// reflectIncludeSample mirrors ReflectIncludeOptions.
type reflectIncludeSample struct {
	Facts     *factsIncludeSample     `json:"facts"`
	ToolCalls *toolCallsIncludeSample `json:"tool_calls"`
}

// factsIncludeSample mirrors FactsIncludeOptions, which declares no fields.
type factsIncludeSample struct{}

// toolCallsIncludeSample mirrors ToolCallsIncludeOptions.
type toolCallsIncludeSample struct {
	Output bool `json:"output"`
}

// UnmarshalJSON reloads the true default before decoding.
func (s *toolCallsIncludeSample) UnmarshalJSON(data []byte) error {
	type plain toolCallsIncludeSample
	value := plain{Output: true}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = toolCallsIncludeSample(value)
	return nil
}

// sampleUpdateMemory returns the model for PATCH .../memories/{memory_id}.
func sampleUpdateMemory() any { return &updateMemorySample{ResolveEntities: true} }

// updateMemorySample mirrors UpdateMemoryRequest.
type updateMemorySample struct {
	Text            *string  `json:"text"`
	Context         *string  `json:"context"`
	OccurredStart   *string  `json:"occurred_start"`
	OccurredEnd     *string  `json:"occurred_end"`
	FactType        *string  `json:"fact_type"`
	Entities        []string `json:"entities"`
	ResolveEntities bool     `json:"resolve_entities"`
	State           *string  `json:"state"`
	Reason          *string  `json:"reason"`
}

// sampleCreateMentalModel returns the model for POST .../mental-models.
func sampleCreateMentalModel() any {
	return &createMentalModelSample{
		Tags:      []string{},
		MaxTokens: 2048,
		Trigger:   &mentalModelTriggerSample{Mode: "full"},
	}
}

// createMentalModelSample mirrors CreateMentalModelRequest.
type createMentalModelSample struct {
	ID          *string                   `json:"id"`
	Name        string                    `json:"name"`
	SourceQuery string                    `json:"source_query"`
	Tags        []string                  `json:"tags"`
	MaxTokens   int                       `json:"max_tokens"`
	Trigger     *mentalModelTriggerSample `json:"trigger"`
}

// mentalModelTriggerSample mirrors MentalModelTrigger.
type mentalModelTriggerSample struct {
	Budget                                   *string         `json:"budget"`
	Mode                                     string          `json:"mode"`
	RefreshAfterConsolidation                bool            `json:"refresh_after_consolidation"`
	RefreshCron                              *string         `json:"refresh_cron"`
	MinRefreshIntervalSeconds                *int            `json:"min_refresh_interval_seconds"`
	FactTypes                                []string        `json:"fact_types"`
	ExcludeMentalModels                      bool            `json:"exclude_mental_models"`
	ExcludeMentalModelIDs                    []string        `json:"exclude_mental_model_ids"`
	TagsMatch                                *string         `json:"tags_match"`
	TagGroups                                json.RawMessage `json:"tag_groups"`
	IncludeChunks                            *bool           `json:"include_chunks"`
	RecallMaxTokens                          *int            `json:"recall_max_tokens"`
	RecallChunksMaxTokens                    *int            `json:"recall_chunks_max_tokens"`
	ReflectSearchObservationsMaxTokens       *int            `json:"reflect_search_observations_max_tokens"`
	ReflectSearchObservationsIncludeEntities *bool           `json:"reflect_search_observations_include_entities"`
	ResponseSchema                           map[string]any  `json:"response_schema"`
	KeepTrace                                bool            `json:"keep_trace"`
}

// UnmarshalJSON reloads the "full" mode default before decoding.
func (s *mentalModelTriggerSample) UnmarshalJSON(data []byte) error {
	type plain mentalModelTriggerSample
	value := plain{Mode: "full"}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = mentalModelTriggerSample(value)
	return nil
}

// sampleUpdateMentalModel returns the model for PATCH .../mental-models/{id}.
func sampleUpdateMentalModel() any { return &updateMentalModelSample{} }

// updateMentalModelSample mirrors UpdateMentalModelRequest.
type updateMentalModelSample struct {
	Name        *string                   `json:"name"`
	SourceQuery *string                   `json:"source_query"`
	MaxTokens   *int                      `json:"max_tokens"`
	Tags        []string                  `json:"tags"`
	Trigger     *mentalModelTriggerSample `json:"trigger"`
}

// sampleCreateDirective returns the model for POST .../directives.
func sampleCreateDirective() any {
	return &createDirectiveSample{IsActive: true, Tags: []string{}}
}

// createDirectiveSample mirrors CreateDirectiveRequest.
type createDirectiveSample struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Priority int      `json:"priority"`
	IsActive bool     `json:"is_active"`
	Tags     []string `json:"tags"`
}

// sampleUpdateDirective returns the model for PATCH .../directives/{id}.
func sampleUpdateDirective() any { return &updateDirectiveSample{} }

// updateDirectiveSample mirrors UpdateDirectiveRequest.
type updateDirectiveSample struct {
	Name     *string  `json:"name"`
	Content  *string  `json:"content"`
	Priority *int     `json:"priority"`
	IsActive *bool    `json:"is_active"`
	Tags     []string `json:"tags"`
}

// sampleUpdateDocument returns the model for PATCH .../documents/{document_id}.
func sampleUpdateDocument() any { return &updateDocumentSample{} }

// updateDocumentSample mirrors UpdateDocumentRequest.
type updateDocumentSample struct {
	Tags []string `json:"tags"`
}

// sampleCreateBank returns the model for PUT and PATCH of a bank. Both routes
// declare the same request body.
func sampleCreateBank() any { return &createBankSample{} }

// createBankSample mirrors CreateBankRequest, whose every field is nullable.
type createBankSample struct {
	Name                         *string                  `json:"name"`
	Disposition                  *dispositionTraitsSample `json:"disposition"`
	DispositionSkepticism        *int                     `json:"disposition_skepticism"`
	DispositionLiteralism        *int                     `json:"disposition_literalism"`
	DispositionEmpathy           *int                     `json:"disposition_empathy"`
	Mission                      *string                  `json:"mission"`
	Background                   *string                  `json:"background"`
	ReflectMission               *string                  `json:"reflect_mission"`
	RetainMission                *string                  `json:"retain_mission"`
	RetainExtractionMode         *string                  `json:"retain_extraction_mode"`
	RetainCustomInstructions     *string                  `json:"retain_custom_instructions"`
	RetainChunkSize              *int                     `json:"retain_chunk_size"`
	RetainStructuredChunkSize    *int                     `json:"retain_structured_chunk_size"`
	RetainMaxAttachmentsPerChunk *int                     `json:"retain_max_attachments_per_chunk"`
	EnableObservations           *bool                    `json:"enable_observations"`
	ObservationsMission          *string                  `json:"observations_mission"`
	EnableTextSearch             *bool                    `json:"enable_text_search"`
	EnableTemporalRetrieval      *bool                    `json:"enable_temporal_retrieval"`
	EnableGraphRetrieval         *bool                    `json:"enable_graph_retrieval"`
	EnableReranking              *bool                    `json:"enable_reranking"`
}

// dispositionTraitsSample mirrors DispositionTraits, whose three fields are all
// required.
type dispositionTraitsSample struct {
	Skepticism int `json:"skepticism"`
	Literalism int `json:"literalism"`
	Empathy    int `json:"empathy"`
}

// sampleCreateBankAlias returns the model for POST .../aliases.
func sampleCreateBankAlias() any { return &createBankAliasSample{} }

// createBankAliasSample mirrors CreateBankAliasRequest.
type createBankAliasSample struct {
	Alias   string `json:"alias"`
	Primary bool   `json:"primary"`
}

// sampleSetBankAliasPrimary returns the model for PATCH .../aliases/{alias}.
func sampleSetBankAliasPrimary() any { return &setBankAliasPrimarySample{} }

// setBankAliasPrimarySample mirrors SetBankAliasPrimaryRequest.
type setBankAliasPrimarySample struct {
	Primary bool `json:"primary"`
}

// sampleBankConfigUpdate returns the model for PATCH .../config.
func sampleBankConfigUpdate() any { return &bankConfigUpdateSample{} }

// bankConfigUpdateSample mirrors BankConfigUpdate.
type bankConfigUpdateSample struct {
	Updates map[string]any `json:"updates"`
}

// sampleConsolidation returns the model for POST .../consolidate.
func sampleConsolidation() any { return &consolidationSample{} }

// consolidationSample mirrors ConsolidationRequest. observation_scopes carries
// the reference's union of scope names and explicit tag groups, passed through
// as decoded.
type consolidationSample struct {
	ObservationScopes json.RawMessage `json:"observation_scopes"`
}

// sampleCreateWebhook returns the model for POST .../webhooks.
func sampleCreateWebhook() any {
	return &createWebhookSample{
		EventTypes: []string{"consolidation.completed"},
		Enabled:    true,
		HTTPConfig: &webhookHTTPConfigSample{
			Method:         "POST",
			TimeoutSeconds: 30,
			Headers:        map[string]string{},
			Params:         map[string]string{},
		},
	}
}

// createWebhookSample mirrors CreateWebhookRequest.
type createWebhookSample struct {
	URL        string                   `json:"url"`
	Secret     *string                  `json:"secret"`
	EventTypes []string                 `json:"event_types"`
	Enabled    bool                     `json:"enabled"`
	HTTPConfig *webhookHTTPConfigSample `json:"http_config"`
}

// sampleUpdateWebhook returns the model for PATCH .../webhooks/{webhook_id}.
func sampleUpdateWebhook() any { return &updateWebhookSample{} }

// updateWebhookSample mirrors UpdateWebhookRequest.
type updateWebhookSample struct {
	URL        *string                  `json:"url"`
	Secret     *string                  `json:"secret"`
	EventTypes []string                 `json:"event_types"`
	Enabled    *bool                    `json:"enabled"`
	HTTPConfig *webhookHTTPConfigSample `json:"http_config"`
}

// webhookHTTPConfigSample mirrors WebhookHttpConfig.
type webhookHTTPConfigSample struct {
	Method         string            `json:"method"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Headers        map[string]string `json:"headers"`
	Params         map[string]string `json:"params"`
}

// UnmarshalJSON reloads the reference's defaults before decoding.
func (s *webhookHTTPConfigSample) UnmarshalJSON(data []byte) error {
	type plain webhookHTTPConfigSample
	value := plain{
		Method:         "POST",
		TimeoutSeconds: 30,
		Headers:        map[string]string{},
		Params:         map[string]string{},
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = webhookHTTPConfigSample(value)
	return nil
}

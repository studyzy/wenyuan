package httpx

// APIVersion is the API version string advertised by the version endpoint. It
// is the reference release whose surface this service serves: a client that
// gates behavior on the version asks what API it is talking to, not which build
// answered. A test pins it to the served contract document's own version.
const APIVersion = ReferenceAPIVersion

// Route namespace and path constants that form the public URL contract.
const (
	NamespaceExt = "/ext"
	NamespaceMCP = "/mcp"

	PathHealth     = "/health"
	PathHealthLive = "/health/live"
	PathReady      = "/health/ready"
	PathVersion    = "/version"
	PathMetrics    = "/metrics"

	PrefixBanksV1 = "/v1/default/banks"
)

// BankRouteSuffixes builds the bank-scoped sub-paths. Kept in the router area
// so no other area hard-codes URL shapes.
const (
	SuffixMemories    = "/memories"
	SuffixRecall      = "/memories/recall"
	SuffixReflect     = "/reflect"
	SuffixAliases     = "/aliases"
	SuffixAliasMember = "/aliases/{alias}"
	SuffixConfig      = "/config"
	SuffixProfile     = "/profile"
	SuffixBackground  = "/background"
	SuffixOperations  = "/operations"

	// Read surface: documents, chunks, tags, entities, graph.
	SuffixDocuments = "/documents"
	// Document member routes are a subtree rather than a pattern: the reference
	// declares the document id as a path-style parameter, so it may span
	// segments, and the routed handler splits the remainder.
	SuffixDocumentMember = "/documents/"
	// SuffixDocumentHistory is the document's change history. The identifier is
	// the path-style parameter, so the routed handler splits "/history" off the
	// remainder rather than the pattern naming the suffix.
	SuffixDocumentHistory  = "/history"
	SuffixTags             = "/tags"
	SuffixGraph            = "/graph"
	SuffixEntities         = "/entities"
	SuffixEntitiesGraph    = "/entities/graph"
	SuffixEntityMember     = "/entities/{entity_id}"
	SuffixEntityRegenerate = "/entities/{entity_id}/regenerate"

	// PrefixChunksV1 is the non-bank-scoped chunk namespace, because a chunk
	// identifier is already globally unique.
	PrefixChunksV1 = "/v1/default/chunks/"

	// Webhooks.
	SuffixWebhooks          = "/webhooks"
	SuffixWebhookMember     = "/webhooks/{webhook_id}"
	SuffixWebhookDeliveries = "/webhooks/{webhook_id}/deliveries"

	// Memories that are not one of the CRUD sub-routes.
	SuffixFileRetain            = "/files/retain"
	SuffixMemoriesDryRunExtract = "/memories/dry-run-extract"
	SuffixOperationMemberDelete = "/operations/{operation_id}/delete"

	// Memory sub-routes, knowledge base, observability and consolidation.
	SuffixMemoriesList        = "/memories/list"
	SuffixMemoryMember        = "/memories/{memory_id}"
	SuffixMemoryHistory       = "/memories/{memory_id}/history"
	SuffixMemoryObservations  = "/memories/{memory_id}/observations"
	SuffixPromptsPreview      = "/prompts/preview"
	SuffixKnowledgeTree       = "/knowledge-base/tree"
	SuffixKnowledgeFolders    = "/knowledge-base/folders"
	SuffixKnowledgePages      = "/knowledge-base/pages"
	SuffixKnowledgePageMember = "/knowledge-base/pages/{page_id}"
	SuffixKnowledgeExport     = "/knowledge-base/export"
	SuffixKnowledgeSearch     = "/knowledge-base/search"
	SuffixKnowledgeNodeMember = "/knowledge-base/nodes/{node_id}"
	SuffixStats               = "/stats"
	SuffixStatsTimeseries     = "/stats/memories-timeseries"
	SuffixHealthLLM           = "/health/llm"
	SuffixAuditLogs           = "/audit-logs"
	SuffixAuditLogMember      = "/audit-logs/{log_id}"
	SuffixAuditLogsStats      = "/audit-logs/stats"
	SuffixLLMRequests         = "/llm-requests"
	SuffixLLMRequestsStats    = "/llm-requests/stats"
	SuffixObservations        = "/observations"
	SuffixObservationScopes   = "/observations/scopes"
	SuffixConsolidatePreview  = "/consolidation-strategies/preview"
	SuffixConsolidateRecover  = "/consolidation/recover"
	SuffixConsolidate         = "/consolidate"
	SuffixBankTemplateSchema  = "/v1/bank-template-schema"
	SuffixVersionV1           = "/v1/version"

	// Mental models and directives.
	SuffixMentalModels       = "/mental-models"
	SuffixMentalModelMember  = "/mental-models/{mental_model_id}"
	SuffixMentalModelHistory = "/mental-models/{mental_model_id}/history"
	SuffixMentalModelRefresh = "/mental-models/{mental_model_id}/refresh"
	SuffixMentalModelDryRun  = "/mental-models/{mental_model_id}/dry-run-refresh"
	SuffixMentalModelClear   = "/mental-models/{mental_model_id}/clear"
	SuffixDirectives         = "/directives"
	SuffixDirectiveMember    = "/directives/{directive_id}"

	// Archive transfer family.
	SuffixImport           = "/import"
	SuffixExport           = "/export"
	SuffixDocumentTransfer = "/document-transfer"
	SuffixTransferExport   = "/transfer/export"
	SuffixTransferImport   = "/transfer/import"
	SuffixClone            = "/clone"
	SuffixAttachmentMember = "/attachments/{attachment_id}"

	// PrefixDefaultV1 is the tenant namespace for routes that are not
	// bank-scoped. The file download route is the only member of the transfer
	// family without a bank path segment.
	PrefixDefaultV1 = "/v1/default"

	// PrefixFilesDownload builds the non-bank-scoped download route. The key
	// is a multi-segment wildcard, so it is matched with the trailing-slash
	// pattern form rather than a single path parameter.
	PrefixFilesDownload = PrefixDefaultV1 + "/files/download/"
)

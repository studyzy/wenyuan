package httpx

// APIVersion is the API version string advertised by the version endpoint.
const APIVersion = "1.0.0"

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
	SuffixDocuments         = "/documents"
	SuffixDocumentMember    = "/documents/{document_id}"
	SuffixDocumentChunks    = "/documents/{document_id}/chunks"
	SuffixDocumentReprocess = "/documents/{document_id}/reprocess"
	SuffixTags              = "/tags"
	SuffixGraph             = "/graph"
	SuffixEntities          = "/entities"
	SuffixEntitiesGraph     = "/entities/graph"
	SuffixEntityMember      = "/entities/{entity_id}"
	SuffixEntityRegenerate  = "/entities/{entity_id}/regenerate"

	// PrefixChunksV1 is the non-bank-scoped chunk namespace, because a chunk
	// identifier is already globally unique.
	PrefixChunksV1 = "/v1/default/chunks/"

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

// TransferAcceptedQueryParams lists the query parameters the archive transfer
// routes accept. They are registered with the query filter so a caller sees
// only genuinely unknown parameters in the ignored-parameter header.
var transferAcceptedQueryParams = []string{
	"subtype",
	"tag",
	"kind",
	"fact_type",
	"mode",
	"target_bank_id",
	"on_conflict",
	"document_conflict",
	"include_data",
	"include_bank_config",
	"include_history",
	"include_observations",
	"include_knowledge_base",
	"document_id",
	"dry_run",
}

// TransferQueryFilter returns the accepted-parameter filter for the transfer
// route family, combined with the standard set.
func TransferQueryFilter() QueryFilter {
	return NewQueryFilter(append(append([]string{}, defaultAcceptedQueryParams...), transferAcceptedQueryParams...)...)
}

// IgnoredQueryParamsAccepted lists the query parameters the bank-scoped routes
// accept. Every other parameter is ignored and reported.
var defaultAcceptedQueryParams = []string{"limit", "offset", "q", "type", "tags", "tags_match", "since", "until"}

// DefaultQueryFilter returns the standard accepted-parameter filter.
func DefaultQueryFilter() QueryFilter {
	return NewQueryFilter(defaultAcceptedQueryParams...)
}

package storage

import "sort"

// MemoryDomainTables are the tables owned exclusively by this storage area.
// No other package may address them directly; the boundary check enforces it.
var MemoryDomainTables = []string{
	"banks",
	"bank_aliases",
	"documents",
	"chunks",
	"entities",
	"memory_units",
	"unit_entities",
	"memory_links",
	"entity_cooccurrences",
	"async_operations",
	"file_storage",
	"mental_models",
	"directives",
	"audit_log",
	"llm_requests",
	"observation_sources",
	"knowledge_nodes",
	"webhooks",
	"webhook_deliveries",
	"attachments",
	"bank_stats_cache",
}

// TableName returns the table name owned by the storage area.
type TableName string

// Memory-domain table names as declared by the compatibility contract.
const (
	TableBanks               TableName = "banks"
	TableBankAliases         TableName = "bank_aliases"
	TableDocuments           TableName = "documents"
	TableChunks              TableName = "chunks"
	TableEntities            TableName = "entities"
	TableMemoryUnits         TableName = "memory_units"
	TableUnitEntities        TableName = "unit_entities"
	TableMemoryLinks         TableName = "memory_links"
	TableEntityCooccurrences TableName = "entity_cooccurrences"
	TableAsyncOperations     TableName = "async_operations"
)

// BanksColumns are the bank identity, disposition, mission and watermark
// columns.
var BanksColumns = []string{
	"id",
	"name",
	"disposition_skepticism",
	"disposition_literalism",
	"disposition_empathy",
	"reflect_mission",
	"created_at",
	"updated_at",
	"last_write_at",
	"last_document_at",
}

// BankAliasesColumns are the persisted alias contract columns.
var BankAliasesColumns = []string{
	"alias",
	"bank_id",
	"is_primary",
	"created_at",
}

// BankAliasIndexes are the named indexes over the alias table.
var BankAliasIndexes = []string{
	"idx_bank_aliases_bank",
	"idx_bank_aliases_one_primary",
}

// MemoryUnitsColumns are the unit text/type,timestamp/metadata columns.
var MemoryUnitsColumns = []string{
	"id",
	"bank_id",
	"text",
	"type",
	"occurred_start",
	"occurred_end",
	"mentioned_at",
	"metadata",
	"document_id",
	"chunk_id",
	"tags",
	"source_fact_ids",
	"context",
	"event_date",
	"confidence_score",
	"access_count",
	"embedding",
	"source_id",
	"observation_scopes",
	"consolidated_at",
	"consolidation_failed_at",
	"attachment_ids",
}

// DocumentsColumns are the document text/hash/metadata/parameter columns.
var DocumentsColumns = []string{
	"id",
	"bank_id",
	"original_text",
	"content_hash",
	"metadata",
	"retain_params",
	"tags",
	"created_at",
}

// ChunksColumns are the chunk identity/index/text columns.
var ChunksColumns = []string{
	"id",
	"bank_id",
	"document_id",
	"index",
	"text",
	"content_hash",
}

// EntitiesColumns are the entity identity/kind/count columns.
var EntitiesColumns = []string{
	"id",
	"bank_id",
	"name",
	"kind",
	"metadata",
	"first_seen",
	"last_seen",
	"mention_count",
}

// AsyncOperationsColumns are the operation identity/status/timestamp columns.
var AsyncOperationsColumns = []string{
	"id",
	"bank_id",
	"operation_type",
	"status",
	"created_at",
	"updated_at",
	"completed_at",
	"error_message",
	"result_metadata",
	"retry_count",
	"next_retry_at",
	"progress",
	"worker_id",
	"claimed_at",
	"task_payload",
}

// FileStorageColumns are the columns of the stored-file table. Payload bytes
// are addressed by an opaque storage key.
var FileStorageColumns = []string{
	"storage_key",
	"data",
}

// AttachmentsColumns are the columns of the inline retain attachments. An
// attachment belongs to the document that carries it, so the identity is the
// document plus the content hash; the bytes live behind the storage key.
var AttachmentsColumns = []string{
	"bank_id",
	"document_id",
	"attachment_hash",
	"short_id",
	"media_type",
	"byte_size",
	"storage_key",
	"kind",
	"filename",
	"created_at",
}

// BankStatsCacheColumns are the columns of the cross-process bank-statistics
// cache. A row holds one bank's rendered summary and when it was computed.
var BankStatsCacheColumns = []string{
	"bank_id",
	"payload",
	"computed_at",
}

// MentalModelsColumns are the columns of the mental-model table. A mental model
// is a named reusable note; its subtype selects which kind it is.
var MentalModelsColumns = []string{
	"id",
	"bank_id",
	"subtype",
	"name",
	"description",
	"source_query",
	"content",
	"embedding",
	"entity_id",
	"observations",
	"links",
	"tags",
	"max_tokens",
	"trigger",
	"structured_content",
	"reflect_response",
	"history",
	"last_refreshed_source_query",
	"last_refreshed_at",
	// last_memory_seen_at is the newest memory write a refresh actually saw.
	// Staleness compares against it rather than against the refresh time,
	// because a refresh that found nothing new still moved its clock.
	"last_memory_seen_at",
	// last_refresh_failed_at parks an automatic refresh after a failure, so a
	// schedule that cannot succeed stops spending a model call per tick.
	"last_refresh_failed_at",
	"last_updated",
	"created_at",
}

// WebhooksColumns are the columns of the webhook registrations.
var WebhooksColumns = []string{
	"id",
	"bank_id",
	"url",
	"secret",
	"event_types",
	"enabled",
	"http_config",
	"created_at",
	"updated_at",
}

// WebhookDeliveriesColumns are the columns of the delivery records.
var WebhookDeliveriesColumns = []string{
	"id",
	"webhook_id",
	"bank_id",
	"event_type",
	"status",
	"status_code",
	"error",
	"attempt",
	"delivered_at",
}

// KnowledgeNodesColumns are the columns of the knowledge tree.
var KnowledgeNodesColumns = []string{
	"id",
	"bank_id",
	"parent_id",
	"kind",
	"title",
	"text",
	"tags",
	"created_at",
	"updated_at",
}

// AuditLogColumns are the columns of the audit trail.
var AuditLogColumns = []string{
	"id",
	"action",
	"transport",
	"bank_id",
	"started_at",
	"ended_at",
	"request",
	"response",
	"metadata",
}

// LLMRequestsColumns are the columns of the model-request trace.
var LLMRequestsColumns = []string{
	"id",
	"bank_id",
	"operation",
	"scope",
	"trace_id",
	"span_id",
	"parent_span_id",
	"provider",
	"model",
	"status",
	"started_at",
	"ended_at",
	"duration_ms",
	"input_tokens",
	"output_tokens",
	"cached_tokens",
	"thoughts_tokens",
	"total_tokens",
	"input",
	"output",
	"error",
	"llm_info",
	"metadata",
}

// ObservationSourcesColumns are the columns tying an observation to its
// evidence.
var ObservationSourcesColumns = []string{
	"observation_id",
	"source_id",
}

// DirectivesColumns are the columns of the directive table. A directive is a
// standing instruction the reflection loop follows.
var DirectivesColumns = []string{
	"id",
	"bank_id",
	"name",
	"content",
	"priority",
	"is_active",
	"tags",
	"created_at",
	"updated_at",
}

// SortedMemoryDomainTables returns the owned table names in stable order.
func SortedMemoryDomainTables() []string {
	out := append([]string(nil), MemoryDomainTables...)
	sort.Strings(out)
	return out
}

// OwnsTable reports whether a table name belongs to the memory domain.
func OwnsTable(name string) bool {
	for _, table := range MemoryDomainTables {
		if table == name {
			return true
		}
	}
	return false
}

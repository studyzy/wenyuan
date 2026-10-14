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
	"invalidated_memory_units",
	"unit_entities",
	"memory_links",
	"entity_cooccurrences",
	"entity_maintenance_queue",
	"graph_maintenance_queue",
	"async_operations",
	"file_storage",
	"mental_models",
	"mental_model_history",
	"observation_history",
	"directives",
	"audit_log",
	"llm_requests",
	"knowledge_pages",
	"webhooks",
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

// BanksColumns are the bank identity, disposition, mission and configuration
// columns. The activity watermarks a bank listing shows are not stored: they
// are read from the newest document and the newest memory unit the bank holds.
var BanksColumns = []string{
	"bank_id",
	"name",
	"disposition",
	"created_at",
	"updated_at",
	"mission",
	"last_consolidated_at",
	"mission_changed_at",
	"config",
	"internal_id",
}

// BankAliasesColumns are the persisted alias contract columns.
var BankAliasesColumns = []string{
	"alias",
	"bank_id",
	"created_at",
	"is_primary",
}

// BankAliasIndexes are the named indexes over the alias table.
var BankAliasIndexes = []string{
	"idx_bank_aliases_bank",
	"idx_bank_aliases_one_primary",
}

// MemoryUnitsColumns are the unit text/fact-type/timestamp/metadata columns.
var MemoryUnitsColumns = []string{
	"id",
	"bank_id",
	"document_id",
	"text",
	"embedding",
	"context",
	"event_date",
	"occurred_start",
	"occurred_end",
	"mentioned_at",
	"fact_type",
	"metadata",
	"created_at",
	"updated_at",
	"chunk_id",
	"tags",
	"proof_count",
	"source_memory_ids",
	"consolidated_at",
	"observation_scopes",
	"text_signals",
	"search_vector",
	"consolidation_failed_at",
	"edited_at",
	"attachment_ids",
}

// DocumentsColumns are the document text/hash/metadata/parameter columns.
var DocumentsColumns = []string{
	"id",
	"bank_id",
	"original_text",
	"content_hash",
	"created_at",
	"updated_at",
	"retain_params",
	"tags",
	"file_storage_key",
	"file_original_name",
	"file_content_type",
}

// ChunksColumns are the chunk identity/index/text columns.
var ChunksColumns = []string{
	"chunk_id",
	"document_id",
	"bank_id",
	"chunk_index",
	"chunk_text",
	"created_at",
	"content_hash",
}

// EntitiesColumns are the entity identity/kind/count columns.
var EntitiesColumns = []string{
	"id",
	"canonical_name",
	"bank_id",
	"metadata",
	"first_seen",
	"last_seen",
	"mention_count",
	"entity_kind",
}

// MemoryLinksColumns are the columns of a directed memory link. The reference
// identifies a link by its endpoints and type rather than a synthetic row id,
// so there is no id column.
var MemoryLinksColumns = []string{
	"from_unit_id",
	"to_unit_id",
	"link_type",
	"entity_id",
	"weight",
	"created_at",
	"bank_id",
}

// AsyncOperationsColumns are the operation identity/status/timestamp columns.
var AsyncOperationsColumns = []string{
	"operation_id",
	"bank_id",
	"operation_type",
	"status",
	"created_at",
	"updated_at",
	"completed_at",
	"error_message",
	"result_metadata",
	"worker_id",
	"claimed_at",
	"retry_count",
	"task_payload",
	"next_retry_at",
	"serialization_key",
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
	"attachment_hash",
	"short_id",
	"media_type",
	"byte_size",
	"storage_key",
	"kind",
	"created_at",
	"document_id",
	"filename",
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
	"name",
	"source_query",
	"content",
	"embedding",
	"tags",
	"last_refreshed_at",
	"created_at",
	"search_vector",
	"reflect_response",
	"max_tokens",
	"trigger",
	"last_refreshed_source_query",
	"structured_content",
	"subtype",
	"description",
	"entity_id",
	"observations",
	"links",
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

// KnowledgePagesColumns are the columns of the knowledge tree. A row carries
// the name and the position; a page's text, tags and description live on the
// mental model the row points at.
var KnowledgePagesColumns = []string{
	"id",
	"bank_id",
	"parent_id",
	"kind",
	"name",
	"mental_model_id",
	"sort_order",
	"managed",
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
	"total_tokens",
	"input",
	"output",
	"error",
	"llm_info",
	"metadata",
	"thoughts_tokens",
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

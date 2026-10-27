package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Environment variable names that form the destination configuration contract.
const (
	EnvHost                   = "WENYUAN_API_HOST"
	EnvPort                   = "WENYUAN_API_PORT"
	EnvDatabaseURL            = "WENYUAN_API_DATABASE_URL"
	EnvDatabaseSchema         = "WENYUAN_API_DATABASE_SCHEMA"
	EnvVectorExtension        = "WENYUAN_API_VECTOR_EXTENSION"
	EnvTextSearchExtension    = "WENYUAN_API_TEXT_SEARCH_EXTENSION"
	EnvRunMigrationsOnStartup = "WENYUAN_API_RUN_MIGRATIONS_ON_STARTUP"
	EnvMCPEnabled             = "WENYUAN_API_MCP_ENABLED"
	EnvEnableBankConfigAPI    = "WENYUAN_API_ENABLE_BANK_CONFIG_API"
	// EnvEnableBankLLMHealth gates the per-bank /health/llm route. It is off by
	// default, matching the reference.
	EnvEnableBankLLMHealth = "WENYUAN_API_ENABLE_BANK_LLM_HEALTH"
	// EnvEnableFileUploadAPI gates the file retain route. It is on by default,
	// matching the reference.
	EnvEnableFileUploadAPI = "WENYUAN_API_ENABLE_FILE_UPLOAD_API"
	// EnvEnableDocumentExportAPI gates the document export routes of the
	// archive transfer family.
	EnvEnableDocumentExportAPI = "WENYUAN_API_ENABLE_DOCUMENT_EXPORT_API"
	// EnvEnableDocumentImportAPI gates the document import routes of the
	// archive transfer family.
	EnvEnableDocumentImportAPI = "WENYUAN_API_ENABLE_DOCUMENT_IMPORT_API"

	// EnvWebhookAllowInsecure permits a plain-HTTP webhook target. Off by
	// default so a delivery cannot be downgraded to cleartext.
	EnvWebhookAllowInsecure = "WENYUAN_API_WEBHOOK_ALLOW_INSECURE"
	// EnvWebhookAllowPrivateHosts permits a webhook target inside the private
	// network. Off by default, because a registered URL is fetched by the
	// server and an unchecked one is a server-side request forgery.
	EnvWebhookAllowPrivateHosts = "WENYUAN_API_WEBHOOK_ALLOW_PRIVATE_HOSTS"
	// EnvWebhookAllowedHosts narrows webhook targets to an explicit host set.
	EnvWebhookAllowedHosts = "WENYUAN_API_WEBHOOK_ALLOWED_HOSTS"

	// EnvWorkerID identifies this queue consumer among consumers sharing one
	// queue, so a claim can be traced back to its holder.
	EnvWorkerID = "WENYUAN_API_WORKER_ID"
	// EnvWorkerEnabled controls whether the API process also runs the queue
	// consumer and the maintenance loop. It defaults to true, matching the
	// reference's standalone mode where one process serves and drains.
	EnvWorkerEnabled = "WENYUAN_API_WORKER_ENABLED"
	// EnvWorkerMaxRetries is the attempt budget beyond the first try.
	EnvWorkerMaxRetries = "WENYUAN_API_WORKER_MAX_RETRIES"
	// EnvWorkerConcurrency is how many operations one consumer processes at once.
	EnvWorkerConcurrency = "WENYUAN_API_WORKER_CONCURRENCY"
	// EnvWorkerLeaseSeconds bounds how long a claim stays valid before another
	// consumer may reclaim the work.
	EnvWorkerLeaseSeconds = "WENYUAN_API_WORKER_LEASE_SECONDS"
	// EnvWorkerPollSeconds is how long to wait between empty claims.
	EnvWorkerPollSeconds = "WENYUAN_API_WORKER_POLL_SECONDS"
	// EnvWorkerHTTPPort is the port the worker's health/metrics surface binds.
	EnvWorkerHTTPPort = "WENYUAN_API_WORKER_HTTP_PORT"
	// The following are the reference's worker key names, accepted as aliases
	// alongside this project's own spellings so an existing deployment's env
	// is honoured.
	EnvWorkerPollIntervalMS       = "WENYUAN_API_WORKER_POLL_INTERVAL_MS"
	EnvWorkerMaxSlots             = "WENYUAN_API_WORKER_MAX_SLOTS"
	EnvWorkerTaskRetryBackoffSecs = "WENYUAN_API_WORKER_TASK_RETRY_BACKOFF_SECONDS"
	// Per-operation wall ceilings. A value of zero means the claim's lease is
	// the only bound.
	EnvWorkerRetainWallSeconds        = "WENYUAN_API_WORKER_RETAIN_WALL_TIMEOUT_SECONDS"
	EnvWorkerConsolidationWallSeconds = "WENYUAN_API_WORKER_CONSOLIDATION_WALL_TIMEOUT_SECONDS"
	// EnvWorkerConsolidationReservedSlots floors the concurrency reserved for
	// consolidation, so a bank mid-bulk-ingest cannot starve observation
	// folding. Default 2.
	EnvWorkerConsolidationReservedSlots = "WENYUAN_API_WORKER_CONSOLIDATION_RESERVED_SLOTS"
	// EnvDBPoolMaxSize bounds simultaneous database connections. The reference
	// defaults it to 100; zero leaves the driver's own default.
	EnvDBPoolMaxSize = "WENYUAN_API_DB_POOL_MAX_SIZE"
	// EnvDBPoolMinSize is the number of connections kept open when unused.
	EnvDBPoolMinSize = "WENYUAN_API_DB_POOL_MIN_SIZE"
	// EnvRetainAttachmentMaxSizeMB bounds one inline attachment or uploaded
	// file, in megabytes. The reference's static server-level upload limit.
	EnvRetainAttachmentMaxSizeMB = "WENYUAN_API_RETAIN_ATTACHMENT_MAX_SIZE_MB"
	// EnvRetainAttachmentMaxCount bounds how many attachments or files one
	// request may carry.
	EnvRetainAttachmentMaxCount = "WENYUAN_API_RETAIN_ATTACHMENT_MAX_COUNT"
	// EnvRecencyDecayFunction selects the age-to-freshness curve
	// (linear, exponential, none).
	EnvRecencyDecayFunction = "WENYUAN_API_RECENCY_DECAY_FUNCTION"
	// EnvRecencyDecayLinearWindowDays is the age at which the linear curve
	// reaches its floor.
	EnvRecencyDecayLinearWindowDays = "WENYUAN_API_RECENCY_DECAY_LINEAR_WINDOW_DAYS"
	// EnvRecencyDecayHalflifeDays is the exponential curve's half-life.
	EnvRecencyDecayHalflifeDays = "WENYUAN_API_RECENCY_DECAY_HALFLIFE_DAYS"
	// EnvRecallMaxCandidatesPerSource caps each recall retrieval arm before
	// fusion, so one over-expanding backend cannot crowd the others out of the
	// merged pool. Zero disables the cap.
	EnvRecallMaxCandidatesPerSource = "WENYUAN_API_RECALL_MAX_CANDIDATES_PER_SOURCE"
	// EnvVectorIndexMinRows is the row count at which a (bank, fact_type)
	// partition earns its own partial vector index. Zero disables reconciliation.
	EnvVectorIndexMinRows = "WENYUAN_API_VECTOR_INDEX_MIN_ROWS"
	// EnvVectorIndexReconcileInterval is the sweep interval in seconds. It is
	// the reference's key, named as the reference names it.
	EnvVectorIndexReconcileInterval = "WENYUAN_API_VECTOR_INDEX_MAINTENANCE_MIN_INTERVAL_SECONDS"
	// EnvOTELTracesEnabled turns OpenTelemetry span export on. Default off.
	EnvOTELTracesEnabled = "WENYUAN_API_OTEL_TRACES_ENABLED"
	// EnvOTELExporterOTLPEndpoint is the OTLP/HTTP collector endpoint.
	EnvOTELExporterOTLPEndpoint = "WENYUAN_API_OTEL_EXPORTER_OTLP_ENDPOINT"
	// EnvOTELExporterOTLPHeaders is the comma-separated key=value header list
	// sent on every export request.
	EnvOTELExporterOTLPHeaders = "WENYUAN_API_OTEL_EXPORTER_OTLP_HEADERS"
	// EnvOTELServiceName is the service.name resource attribute.
	EnvOTELServiceName = "WENYUAN_API_OTEL_SERVICE_NAME"
	// EnvOTELDeploymentEnvironment is the deployment.environment attribute.
	EnvOTELDeploymentEnvironment = "WENYUAN_API_OTEL_DEPLOYMENT_ENVIRONMENT"
	EnvWorkerReflectWallSeconds  = "WENYUAN_API_WORKER_REFLECT_WALL_TIMEOUT_SECONDS"
	// EnvWorkerInitialBackoffSeconds is the first retry delay.
	EnvWorkerInitialBackoffSeconds = "WENYUAN_API_WORKER_INITIAL_BACKOFF_SECONDS"
	// EnvWorkerMaxBackoffSeconds is the retry backoff ceiling.
	EnvWorkerMaxBackoffSeconds = "WENYUAN_API_WORKER_MAX_BACKOFF_SECONDS"
	// EnvRetentionSweepIntervalSeconds is how often the retention jobs run.
	EnvRetentionSweepIntervalSeconds = "WENYUAN_API_RETENTION_SWEEP_INTERVAL_SECONDS"
	// EnvAuditLogRetentionDays is how long an audit row is kept; a negative
	// value keeps them forever.
	EnvAuditLogRetentionDays = "WENYUAN_API_AUDIT_LOG_RETENTION_DAYS"
	// EnvConsolidationReconcileIntervalSeconds is how often banks holding
	// stranded consolidation facts are re-scheduled.
	EnvConsolidationReconcileIntervalSeconds = "WENYUAN_API_CONSOLIDATION_RECONCILE_INTERVAL_SECONDS"
	// EnvMentalModelRefreshTickSeconds is how often scheduled mental models are
	// checked for due-ness.
	EnvMentalModelRefreshTickSeconds = "WENYUAN_API_MENTAL_MODEL_REFRESH_TICK_SECONDS"
	// EnvOperationCleanupIntervalSeconds is how often terminal operations are
	// pruned.
	EnvOperationCleanupIntervalSeconds = "WENYUAN_API_OPERATION_CLEANUP_INTERVAL_SECONDS"
	// EnvOperationRetentionDays is how long a terminal operation is kept; zero
	// disables the prune.
	EnvOperationRetentionDays = "WENYUAN_API_OPERATION_RETENTION_DAYS"
	// EnvOperationCleanupBatchSize bounds one prune per schema per run.
	EnvOperationCleanupBatchSize = "WENYUAN_API_OPERATION_CLEANUP_BATCH_SIZE"
	// EnvMaintenanceStartJitterSeconds spreads the first maintenance tick, so a
	// fleet started together does not fire every probe at once.
	EnvMaintenanceStartJitterSeconds = "WENYUAN_API_MAINTENANCE_START_JITTER_SECONDS"
	// EnvGzipMinSize bounds the response size above which a body is compressed.
	EnvGzipMinSize = "WENYUAN_API_GZIP_MIN_SIZE"
	// EnvProfile holds the reference's profiling request as raw JSON, e.g.
	// {"every":60,"top":20}. The profile is emitted to the log stream; the
	// reference exposes no profiling endpoint.
	EnvProfile = "WENYUAN_API_PROFILE"
	// EnvPprofEnabled is this project's own switch for the Go pprof endpoints
	// under /debug/pprof. It is deliberately separate from EnvProfile, which
	// the reference owns with a different meaning.
	EnvPprofEnabled = "WENYUAN_API_PPROF_ENABLED"
	// EnvBasePath serves every route under a path prefix as well as at the
	// root, mirroring the reference's FastAPI root_path.
	EnvBasePath = "WENYUAN_API_BASE_PATH"
	// EnvEnableObservations gates observations (auto-consolidation). On by
	// default, like the reference.
	EnvEnableObservations = "WENYUAN_API_ENABLE_OBSERVATIONS"
	// EnvAuditLogEnabled gates audit logging by default; a bank may override.
	// Off by default, like the reference.
	EnvAuditLogEnabled = "WENYUAN_API_AUDIT_LOG_ENABLED"
	// EnvStoreDocumentText controls whether raw source text is persisted. On
	// by default, like the reference.
	EnvStoreDocumentText = "WENYUAN_API_STORE_DOCUMENT_TEXT"
	// EnvTenantExtension selects the tenant authenticator by its class path,
	// the reference's extension convention. Unset means no authentication,
	// which is the reference's single-tenant default.
	EnvTenantExtension = "WENYUAN_API_TENANT_EXTENSION"
	// EnvTenantAPIKey is the credential the built-in API-key authenticator
	// accepts.
	EnvTenantAPIKey = "WENYUAN_API_TENANT_API_KEY"
	// EnvTenantMCPAuthDisabled exempts the MCP namespace from tenant
	// authentication, which a deployment that fronts MCP with its own gate
	// needs.
	EnvTenantMCPAuthDisabled = "WENYUAN_API_TENANT_MCP_AUTH_DISABLED"
	// EnvOperationValidatorExtension selects the operation validator by its
	// class path, the reference's extension convention. The one this build
	// implements natively is the reference suite's tag-scope validator, which
	// confines a "bank:<id>" API key to writing only its own bank.
	EnvOperationValidatorExtension = "WENYUAN_API_OPERATION_VALIDATOR_EXTENSION"
	// EnvLoopLagReportSeconds is how often the loop-lag probe samples the
	// scheduler; EnvLoopLagStallSeconds is the delay above which it reports a
	// stall.
	EnvLoopLagReportSeconds = "WENYUAN_API_LOOP_LAG_REPORT_SECONDS"
	EnvLoopLagStallSeconds  = "WENYUAN_API_LOOP_LAG_STALL_SECONDS"
	// Admission keys bound how many expensive operations run at once.
	EnvAdmissionMaxWaitSeconds = "WENYUAN_API_ADMISSION_MAX_WAIT_SECONDS"
	EnvAdmissionRecallLimit    = "WENYUAN_API_ADMISSION_RECALL_LIMIT"
	EnvAdmissionRetainLimit    = "WENYUAN_API_ADMISSION_RETAIN_LIMIT"
	EnvAdmissionReflectLimit   = "WENYUAN_API_ADMISSION_REFLECT_LIMIT"
	EnvRetainBatchEnabled      = "WENYUAN_API_RETAIN_BATCH_ENABLED"
	// EnvFileStorageType selects the attachment storage backend. Empty or
	// "postgresql" keeps payloads in the database, which is the default.
	EnvFileStorageType = "WENYUAN_API_FILE_STORAGE_TYPE"
	// The S3 family configures the S3-compatible backend, which also serves
	// MinIO, Cloudflare R2, Tigris and any other S3-compliant endpoint.
	EnvFileStorageS3Bucket          = "WENYUAN_API_FILE_STORAGE_S3_BUCKET"
	EnvFileStorageS3Region          = "WENYUAN_API_FILE_STORAGE_S3_REGION"
	EnvFileStorageS3Endpoint        = "WENYUAN_API_FILE_STORAGE_S3_ENDPOINT"
	EnvFileStorageS3AccessKeyID     = "WENYUAN_API_FILE_STORAGE_S3_ACCESS_KEY_ID"
	EnvFileStorageS3SecretAccessKey = "WENYUAN_API_FILE_STORAGE_S3_SECRET_ACCESS_KEY"
	// EnvRecallStrategyBoosts prioritises one or more recall retrieval arms
	// over the others. The value is a comma-separated list of arm:level pairs
	// where level is low, medium or high.
	EnvRecallStrategyBoosts = "WENYUAN_API_RECALL_STRATEGY_BOOSTS"

	// EnvReflectSearchObservationsMaxTokens is the process-level default for
	// the per-bank reflection observation-search token budget.
	EnvReflectSearchObservationsMaxTokens = "WENYUAN_API_REFLECT_SEARCH_OBSERVATIONS_MAX_TOKENS"
	// EnvReflectSearchObservationsIncludeEntities is the process-level default
	// for the per-bank reflection observation entity-attachment flag.
	EnvReflectSearchObservationsIncludeEntities = "WENYUAN_API_REFLECT_SEARCH_OBSERVATIONS_INCLUDE_ENTITIES"

	// EnvForwardedAllowIPs is the reference HTTP server's own variable for the
	// peers whose X-Forwarded-* headers are trusted. It is deliberately not a
	// WENYUAN_API_ key: it is resolved by the server, not by the application
	// configuration, and it is read here for the same reason.
	EnvForwardedAllowIPs = "FORWARDED_ALLOW_IPS"
)

// forwardedAllowIPsFromEnv resolves the trusted-proxy allow list the way the
// reference's server does: the entries of FORWARDED_ALLOW_IPS, comma separated,
// defaulting to loopback only.
func forwardedAllowIPsFromEnv(lookup func(string) (string, bool)) []string {
	raw, ok := lookup(EnvForwardedAllowIPs)
	if !ok || strings.TrimSpace(raw) == "" {
		return []string{DefaultForwardedAllowIPs}
	}
	out := make([]string, 0, 2)
	for _, entry := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return []string{DefaultForwardedAllowIPs}
	}
	return out
}

// Default values used when neither a flag nor an environment variable supplies
// a value.
const (
	DefaultHost = "0.0.0.0"
	// DefaultPort is the API listen port. It is the reference's 8888 offset by
	// 10000 on purpose: the reference implementation and wenyuan must be able
	// to run side by side for comparison, so the out-of-the-box ports must not
	// collide. WENYUAN_API_PORT still overrides, and every other default
	// keeps matching the reference.
	DefaultPort = 18888
	// DefaultDaemonPort is the port daemon mode binds unless the operator passed
	// --port explicitly: the reference resolves daemon mode from its own default
	// rather than from a configured WENYUAN_API_PORT, so a launcher that
	// exports a port for foreground runs does not move the daemon.
	DefaultDaemonPort = 18888
	// DefaultDaemonHost is the address daemon mode binds unless the operator
	// explicitly configured a host (--host or WENYUAN_API_HOST).
	DefaultDaemonHost = "127.0.0.1"
	// DefaultDaemonLogName is the file daemon mode appends its output to, under
	// the operator's home directory, when WENYUAN_API_DAEMON_LOG is unset.
	DefaultDaemonLogName = ".hindsight/daemon.log"
	// DefaultForwardedAllowIPs is the peer the proxy headers are trusted from
	// when neither --forwarded-allow-ips nor FORWARDED_ALLOW_IPS names one. It
	// is the reference's own default: only a loopback proxy is trusted.
	DefaultForwardedAllowIPs = "127.0.0.1"
	DefaultDatabaseSchema    = "public"
	DefaultVectorExtension   = "pgvector"
	// DefaultDBPoolMaxSize bounds simultaneous database connections, matching
	// the reference's default. An unbounded pool grows until the database
	// refuses connections, turning a load spike into an outage for every other
	// client of that database.
	DefaultDBPoolMaxSize = 100
	// DefaultDBPoolMinSize is the pool size the reference keeps open.
	DefaultDBPoolMinSize       = 5
	DefaultTextSearchExtension = "native"
	// DefaultTextSearchExtensionNativeLanguage is the text-search dictionary the
	// native tsvector backend tokenizes with, the reference's own default. It is
	// a deployment-level choice, not a per-query one: the dictionary is embedded
	// in the vector expression every write stores and in every query, so rows
	// written under one dictionary are not equivalent under another.
	DefaultTextSearchExtensionNativeLanguage = "english"
	DefaultGzipMinSize                       = 1024
	DefaultReadTimeoutSeconds                = 30
	DefaultRetryAfterSeconds                 = 5
	DefaultLogLevel                          = "info"
	// DefaultFileParser is the document-parser chain's default, the reference's
	// own. The chain is tried in order until one parser converts a document.
	DefaultFileParser = "markitdown"
	// DefaultFileConversionMaxBatchSize and DefaultFileConversionMaxBatchSizeMB
	// bound one upload batch: how many files, and how many megabytes combined.
	DefaultFileConversionMaxBatchSize   = 10
	DefaultFileConversionMaxBatchSizeMB = 100
	// DefaultFileDeleteAfterRetain drops uploaded bytes once they are retained.
	DefaultFileDeleteAfterRetain = true
	// DefaultTokenizerEncoding is the reference's vocabulary for token counts.
	DefaultTokenizerEncoding = "o200k_base"
	// DefaultLogFormat is the reference's default log shape.
	DefaultLogFormat = "text"
	// DefaultRetainExtractionMode is the reference's default extraction mode.
	DefaultRetainExtractionMode = "concise"
	// RetainExtractionModes are the modes the extractor implements; the reference
	// accepts exactly these and refuses any other value.
	RetainExtractionModes = "concise, verbose, custom, verbatim, chunks"
	DefaultWorkers        = 1
	// DefaultRunMigrationsOnStartup matches the reference: the server migrates
	// its schema on startup unless told not to.
	DefaultRunMigrationsOnStartup = true
	DefaultWorkerEnabled          = true
	DefaultAdmissionMaxWaitS      = 5
	DefaultAdmissionLimit         = 8
	DefaultLoopLagReportS         = 0
	DefaultLoopLagStallS          = 1
	DefaultMigrationGraceWindow   = 5 * time.Second
	// DefaultReadPoolMinSize and DefaultReadPoolMaxSize are the reference's own
	// bounds for the replica pool.
	DefaultReadPoolMinSize = 5
	DefaultReadPoolMaxSize = 100
)

// Defaults for the reference's tuning keys. They are the reference's own
// shipped defaults, so an environment that sets none of them resolves to the
// same behaviour on both sides.
const (
	DefaultChunkSize                    = 3000
	DefaultRetainChunkBatchSize         = 100
	DefaultRetainMaxAttachmentsPerChunk = 8
	DefaultConsolidationLLMBatchSize    = 8
	// DefaultConsolidationLLMParallelism is the reference's parallelism for
	// consolidation model calls.
	DefaultConsolidationLLMParallelism                     = 4
	DefaultConsolidationMaxMemoriesPerRound                = 100
	DefaultConsolidationSourceFactsMaxTokens               = 4096
	DefaultConsolidationSourceFactsMaxTokensPerObservation = 256
	DefaultConsolidationMemoryBatchSize                    = 50
	DefaultConsolidationDedupThreshold                     = 0.97
	DefaultConsolidationMaxTokens                          = 512
	DefaultConsolidationMaxAttempts                        = 3
	DefaultReflectSourceFactsMaxTokens                     = -1
	DefaultReflectMaxIterations                            = 10
	DefaultReflectMaxContextTokens                         = 100000
	DefaultRecallMaxTokens                                 = 2048
	DefaultRecallChunksMaxTokens                           = 1000
	DefaultRecallBudgetFunction                            = "fixed"
	DefaultRecallBudgetFixedLow                            = 100
	DefaultRecallBudgetFixedMid                            = 300
	DefaultRecallBudgetFixedHigh                           = 1000
	DefaultRecallBudgetAdaptiveLow                         = 0.025
	DefaultRecallBudgetAdaptiveMid                         = 0.075
	DefaultRecallBudgetAdaptiveHigh                        = 0.25
	DefaultRecallBudgetMin                                 = 20
	DefaultRecallBudgetMax                                 = 2000
	DefaultGraphRetriever                                  = "link_expansion"
	DefaultGraphSeedMinSimilarity                          = 0.3
	DefaultSemanticMinSimilarity                           = 0.3
	DefaultTemporalSemanticMinSimilarity                   = 0.1
	DefaultSemanticLinkMinSimilarity                       = 0.7
	DefaultBM25MinScore                                    = 0.0
	DefaultBM25MaxQueryTerms                               = 16
	DefaultLinkExpansionPerEntityLimit                     = 200
	DefaultLinkExpansionTimeoutS                           = 10 * time.Second
	DefaultRecallMaxQueryTokens                            = 500
	// DefaultRecallConnectionBudget bounds the connections one recall's arms may
	// hold at once.
	DefaultRecallConnectionBudget        = 4
	DefaultRecallMaxConcurrent           = 32
	DefaultRetainMaxCompletionTokens     = 64000
	DefaultRetainSubBatchTokens          = 10000
	DefaultEntityResolutionBatchSize     = 100
	DefaultEntityResolutionMaxCandidates = 200
	DefaultHistoryMaxEntries             = 50
	// DefaultBankAliasCacheTTLSeconds and DefaultBankAliasCacheMaxEntries are the
	// reference's alias-cache defaults.
	DefaultBankAliasCacheTTLSeconds = 10
	DefaultBankAliasCacheMaxEntries = 2048
	// DefaultBankInfoCacheMaxEntries bounds the per-process bank-row cache, and
	// DefaultBankStatsCacheMaxEntries the stored bank-statistics summaries.
	DefaultBankInfoCacheMaxEntries         = 2048
	DefaultBankStatsCacheMaxEntries        = 1024
	DefaultBackpressureDeferS              = 120
	DefaultDBStatementTimeoutS             = 600 * time.Second
	DefaultDBCommandTimeoutS               = 60 * time.Second
	DefaultANNMaxScanTuples                = 4000
	DefaultEntityTRGMSimilarityThreshold   = 0.3
	DefaultEntityIntrabatchMergeSimilarity = 0.5
	DefaultEntityMergeMinSimilarity        = 0.3
	DefaultModelInitTimeoutS               = 300 * time.Second
	// DefaultRetainMaxConcurrent, DefaultRetainSubBatchConcurrency and
	// DefaultRetainStoreMaxConcurrent are the reference's write-side concurrency
	// bounds; DefaultRetainMemoryBudgetMB caps the unwritten state.
	DefaultRetainMaxConcurrent       = 4
	DefaultRetainSubBatchConcurrency = 1
	DefaultRetainStoreMaxConcurrent  = 16
	DefaultRetainMemoryBudgetMB      = 128
	// DefaultDBAcquireTimeoutS, DefaultDBPoolSlowAcquireS and
	// DefaultDBAcquireWarn are the reference's pool-acquire defaults.
	DefaultDBAcquireTimeoutS  = 30 * time.Second
	DefaultDBPoolSlowAcquireS = 50 * time.Millisecond
	DefaultDBAcquireWarn      = 1000 * time.Millisecond
	// DefaultLoopWatchdogPoll and DefaultLoopWatchdogStall are the
	// reference watchdog's own intervals.
	DefaultLoopWatchdogPoll  = 250 * time.Millisecond
	DefaultLoopWatchdogStall = 1000 * time.Millisecond
	DefaultWebhookEventTypes = "consolidation.completed"
	// DefaultDatabaseBackend is the reference's own backend selection.
	DefaultDatabaseBackend = "postgresql"
	// DefaultTextSearchPGFunctionSchema is the ParadeDB function schema's own
	// name, which is the reference's default for that key.
	DefaultTextSearchPGFunctionSchema = "paradedb"
	// DefaultRetainEntityLookup is the reference's entity-lookup mode.
	DefaultRetainEntityLookup = "trigram"
	// DefaultConsolidationRecallBudget is the reference's recall budget level
	// for the consolidation pass.
	DefaultConsolidationRecallBudget = "low"
)

// Config is the resolved, typed process configuration. It is built once at
// startup from defaults, environment values, and command-line flags.
type Config struct {
	Host                string
	Port                int
	DatabaseURL         string
	DatabaseSchema      string
	VectorExtension     string
	TextSearchExtension string
	// TextSearchExtensionNativeLanguage is the tsvector dictionary the native
	// text-search backend tokenizes with (the reference's
	// WENYUAN_API_TEXT_SEARCH_EXTENSION_NATIVE_LANGUAGE).
	TextSearchExtensionNativeLanguage string
	RunMigrationsOnStartup            bool
	MCPEnabled                        bool
	EnableBankConfigAPI               bool
	// EnableBankLLMHealth gates the per-bank /health/llm route.
	EnableBankLLMHealth bool
	// EnableFileUploadAPI gates the file retain route.
	EnableFileUploadAPI bool
	// EnableDocumentExportAPI gates the document export routes.
	EnableDocumentExportAPI bool
	// EnableDocumentImportAPI gates the document import routes.
	EnableDocumentImportAPI bool
	// EnableDryRunExtract gates the dry-run extraction route: when off it
	// answers the reference's 404 rather than extracting.
	EnableDryRunExtract bool
	// MCPAuthToken is the legacy MCP credential; MCPInstructions are appended to
	// the retain and recall tool descriptions; MCPEnabledTools is the
	// server-level tool allowlist (nil means every tool).
	MCPAuthToken    string
	MCPInstructions string
	MCPEnabledTools []string
	// EnableObservationHistory and EnableMentalModelHistory gate the two
	// history records; with one off, its read routes answer with no entries
	// because none are written.
	EnableObservationHistory bool
	EnableMentalModelHistory bool
	// Worker tunes the background queue consumer.
	Worker WorkerConfig
	// WorkerEnabled runs the queue consumer and the maintenance loop inside the
	// API process. Defaults to true, matching the reference's standalone mode.
	WorkerEnabled bool
	// Maintenance tunes the background maintenance loop.
	Maintenance MaintenanceConfig
	// WebhookAllowInsecure permits a plain-HTTP webhook target.
	WebhookAllowInsecure bool
	// WebhookAllowPrivateHosts permits a webhook target inside the private
	// network. Off by default, because a registered URL is fetched by the
	// server and an unchecked one is a server-side request forgery.
	WebhookAllowPrivateHosts bool
	// WebhookAllowedHosts narrows webhook targets to an explicit host set.
	WebhookAllowedHosts []string
	GzipMinSize         int
	// Admission bounds the concurrency of the expensive operations.
	AdmissionMaxWait time.Duration
	// AdmissionRecallMaxWait, AdmissionReflectMaxWait and AdmissionRetainMaxWait
	// are the per-lane queue deadlines. A zero value falls back to
	// AdmissionMaxWait, which is what a lane with no configured wait uses.
	AdmissionRecallMaxWait  time.Duration
	AdmissionReflectMaxWait time.Duration
	AdmissionRetainMaxWait  time.Duration
	AdmissionRecallLimit    int
	AdmissionRetainLimit    int
	AdmissionReflectLimit   int
	// BasePath serves the whole surface under a prefix when set, in addition
	// to the root paths.
	BasePath string
	// Profile holds the reference's raw profiling request, unparsed.
	Profile string
	// PprofEnabled exposes the Go pprof endpoints under /debug/pprof. It is
	// this project's own switch: the reference's ProfileEnv asks for a
	// log-stream profile instead.
	PprofEnabled bool
	// EnableObservations, EnableAutoConsolidation, AuditLogEnabled and
	// StoreDocumentText are feature flags the version endpoint reports.
	// EnableObservations gates observations; EnableAutoConsolidation gates the
	// post-retain consolidation pass that produces them. Both are the
	// deployment defaults a bank may override.
	EnableObservations      bool
	EnableAutoConsolidation bool
	AuditLogEnabled         bool
	// AuditLogActions narrows the audit trail to a set of action names. Empty
	// means every eligible action is recorded, which is the reference default.
	AuditLogActions   []string
	StoreDocumentText bool
	// LogFormat selects the log stream's shape ("text" or "json");
	// LogJSONFields narrows the JSON object's keys (empty means every key).
	LogFormat     string
	LogJSONFields []string
	// TokenizerEncoding is the BPE vocabulary every reported token count is
	// measured with, spelled as the reference spells it.
	TokenizerEncoding string
	// RetainExtractionMode is the deployment default for a bank that does not
	// configure its own; RetainOptionalFactDimensions relaxes the extraction
	// schema so the descriptive dimensions may be omitted.
	RetainExtractionMode         string
	RetainOptionalFactDimensions bool
	// FileParser is the document-parser fallback chain, tried in order;
	// FileParserAllowlist narrows which parsers a client may name (empty means
	// every registered parser).
	FileParser          []string
	FileParserAllowlist []string
	// FileParserIrisToken/OrgID and FileParserLlamaParseAPIKey configure the
	// remote parsers; an empty value leaves that parser unavailable.
	FileParserIrisToken        string
	FileParserIrisOrgID        string
	FileParserLlamaParseAPIKey string
	// FileConversionMaxBatchSize and FileConversionMaxBatchSizeMB bound one
	// upload batch; FileDeleteAfterRetain drops the bytes once retained.
	FileConversionMaxBatchSize   int
	FileConversionMaxBatchSizeMB int
	FileDeleteAfterRetain        bool
	// TenantExtension is the selected tenant authenticator's class path.
	TenantExtension string
	// TenantAPIKey is the credential the built-in authenticator accepts.
	TenantAPIKey string
	// TenantMCPAuthDisabled exempts the MCP namespace from authentication.
	TenantMCPAuthDisabled bool
	// OperationValidatorExtension is the selected operation validator's class
	// path, empty when the deployment registers none.
	OperationValidatorExtension string
	// LoopLagReport is the loop-lag probe interval; LoopLagStall is the delay
	// above which a stall is reported. A zero interval disables the probe.
	LoopLagReport time.Duration
	LoopLagStall  time.Duration
	// LoopWatchdogInterval and LoopWatchdogStall are the resolved probe cadence
	// and stall threshold: the watchdog keys, with the lag-reporter keys
	// overriding them when a deployment set those.
	LoopWatchdogInterval time.Duration
	LoopWatchdogStall    time.Duration
	RetainBatchEnabled   bool
	// RecallStrategyBoosts maps a retrieval arm identifier to a boost level
	// (low, medium, high). Empty leaves every arm unboosted.
	RecallStrategyBoosts map[string]string
	// Recency is the resolved age-to-freshness tuning for recall.
	Recency RecencyConfig
	// RetainAttachmentMaxSizeMB and RetainAttachmentMaxCount bound an upload.
	// Zero disables the corresponding cap.
	RetainAttachmentMaxSizeMB int
	RetainAttachmentMaxCount  int
	// DBPoolMaxSize and DBPoolMinSize bound the database connection pool. Zero
	// leaves the driver's own default.
	DBPoolMaxSize int
	DBPoolMinSize int
	// ReflectSearchObservationsMaxTokens is the shipped per-bank reflection
	// default for the observation-search token budget; nil means unset.
	ReflectSearchObservationsMaxTokens *int
	// ReflectSearchObservationsIncludeEntities is the shipped per-bank
	// reflection default for entity attachment on observation evidence.
	ReflectSearchObservationsIncludeEntities *bool

	// FileStorage is the resolved attachment backend. An empty Type keeps
	// payloads in the database, which is what a deployment with no object
	// store configured gets.
	FileStorage FileStorageConfig

	// Provider is the resolved model-provider configuration, resolved inside
	// the same environment source as every other key.
	Provider ProviderConfig
	// Tracing is the resolved OpenTelemetry exporter configuration. Tracing is
	// off unless a deployment enables it.
	Tracing TracingConfig
	// VectorIndex tunes per-(bank, fact_type) partial vector indexes.
	VectorIndex VectorIndexConfig
	// ProviderScopes is the resolved strict-schema and temperature family.
	ProviderScopes ScopedConfig
	// Consolidation is the resolved second provider scope.
	Consolidation ConsolidationScope
	// RetainLLM, ReflectLLM and MentalModelRefreshLLM are the remaining
	// per-role chat scopes, resolved the same way as Consolidation so a role's
	// own override is accepted rather than dropped.
	RetainLLM             LLMScope
	ReflectLLM            LLMScope
	MentalModelRefreshLLM LLMScope

	// rawEnv retains every environment value read during resolution, so a key
	// that was configured can be refused by name rather than silently
	// ignored. It is not part of the configuration's identity.
	rawEnv map[string]string

	// CLI-only operational flags.
	LogLevel     string
	Workers      int
	AccessLog    bool
	ProxyHeaders bool
	SSLKeyFile   string
	SSLCertFile  string
	Daemon       bool
	// Reload is the reference's development auto-reload. A compiled binary has
	// no reloader, so the flag is accepted and reported rather than silently
	// honoured: see ReloadWarning.
	Reload bool
	// IdleTimeout is the reference's retired daemon idle exit, still accepted so
	// existing launchers start. It is inert in both implementations.
	IdleTimeout int
	// DaemonLog is where daemon mode appends its output.
	DaemonLog string
	// ForwardedAllowIPs are the peers whose X-Forwarded-* headers are trusted.
	// It is resolved from --forwarded-allow-ips, then the reference's
	// FORWARDED_ALLOW_IPS, then the loopback default.
	ForwardedAllowIPs []string

	// Internal tuning derived from defaults or flags.
	ReadTimeout  time.Duration
	RetryAfter   time.Duration
	BindGrace    time.Duration
	DotenvLoaded string

	// BankInfoCacheTTL bounds the per-process bank profile/configuration
	// cache's staleness; BankStatsCacheTTL bounds the cross-process bank
	// statistics summary. A zero value disables the corresponding cache,
	// matching the reference's "0 disables" convention.
	BankInfoCacheTTL  time.Duration
	BankStatsCacheTTL time.Duration
	// BankAliasCacheTTL and BankAliasCacheMaxEntries bound the per-process
	// alias-resolution cache. A zero TTL disables it.
	BankAliasCacheTTL        time.Duration
	BankAliasCacheMaxEntries int

	// ProcessDefaults holds the reference's per-bank configurable fields as
	// resolved from the environment. A bank's configuration read layers its own
	// overrides over these, so a deployment that sets one of the keys below
	// changes the effective value of every bank that did not override it — see
	// bankapi.PublicConfigDefaults.
	ProcessDefaults ProcessDefaults

	// RecallTuning holds the reference's retrieval knobs. Each one is the
	// process-level value a bank's configuration read reports and the value the
	// engine uses when a caller does not override it per request.
	RecallTuning RecallTuning

	// RetainTuning holds the reference's write-path knobs.
	RetainTuning RetainTuning

	// ReflectTuning holds the reference's reflection-loop knobs.
	ReflectTuning ReflectTuning

	// ConsolidationTuning holds the reference's consolidation knobs that are
	// process-level rather than per-bank.
	ConsolidationTuning ConsolidationTuning

	// WorkerTuning holds the reference's queue-scheduling knobs.
	WorkerTuning WorkerTuning

	// DBTuning holds the reference's database session and scanning knobs.
	DBTuning DBTuning

	// MetricsTuning holds the reference's observability knobs.
	MetricsTuning MetricsTuning

	// EntityTuning holds the reference's entity-resolution knobs.
	EntityTuning EntityTuning

	// WebhookGlobal is the server-level webhook target every bank's events are
	// also delivered to, independent of the per-bank registrations.
	WebhookGlobal WebhookGlobal

	// ExtensionPassThroughHeaders are the request headers copied into the
	// extension context, so an extension can read a deployment's own header
	// (a tenant hint, a trace id) without the HTTP layer knowing its meaning.
	ExtensionPassThroughHeaders []string

	// DefaultBankTemplate is the manifest applied to every newly-created bank.
	DefaultBankTemplate map[string]any

	// DatabaseBackend names the storage dialect: the reference's own selection,
	// whose only value this build implements is PostgreSQL (with `memory` as the
	// non-persistent backend this build adds for tests and local runs).
	DatabaseBackend string

	// ReadReplica is the optional read-only pool: reads that tolerate a replica
	// go to it while writes stay on the primary.
	ReadReplica ReadReplicaConfig

	// The OCR chain: when enabled, a document's embedded images are transcribed
	// by the configured vision model and the transcription joins the parsed
	// text, which is what the reference's markitdown OCR does.
	FileParserOCREnabled bool
	FileParserOCRAPIKey  string
	FileParserOCRBaseURL string
	FileParserOCRModel   string
	FileParserOCRPrompt  string
	FileParserOCRHeaders map[string]string

	// ModelInitTimeout bounds the startup probe of the configured model
	// providers, so an unreachable endpoint fails startup instead of hanging it,
	// and SkipLLMVerification turns that probe off entirely.
	ModelInitTimeout    time.Duration
	SkipLLMVerification bool

	// Unsupported collects the reference environment keys that were set but
	// that this build cannot act on. A non-empty set refuses startup, so a
	// copied reference .env is never silently half-honoured.
	Unsupported *Unsupported
}

// WebhookGlobal is the deployment-level webhook target.
type WebhookGlobal struct {
	// URL is the receiver. Empty disables the global webhook.
	URL string
	// Secret signs the deliveries.
	Secret string
	// EventTypes is the event filter; empty means the reference's default set.
	EventTypes []string
	// ExposeResponseBody includes the receiver's response body in the delivery
	// record, which is what a deployment debugging a receiver wants and what a
	// deployment holding other people's data usually does not.
	ExposeResponseBody bool
}

// ReadReplicaConfig is the optional read-only database pool.
type ReadReplicaConfig struct {
	// URL is the replica's connection string. Empty disables the replica.
	URL string
	// PoolMin and PoolMax bound the replica pool, the same two bounds the
	// primary pool uses.
	PoolMin int
	PoolMax int
}

// ProcessDefaults is the reference's per-bank configurable surface as resolved
// from the process environment. Pointer-typed fields mirror the reference's own
// nullability: a field whose process value is null stays null in a bank's read,
// which is what distinguishes "the deployment set nothing" from "the deployment
// set the empty string".
type ProcessDefaults struct {
	// MCPEnabledTools is the reference's list[str] | None: the parsed allowlist
	// when the deployment set one, and null otherwise. An explicit empty list is
	// distinct from null, which is why this is a slice rather than a pointer to
	// the raw string.
	MCPEnabledTools                             []string
	EnableTextSearch                            bool
	EnableTemporalRetrieval                     bool
	EnableGraphRetrieval                        bool
	EnableReranking                             bool
	RetainChunkSize                             int
	RetainStructuredChunkSize                   *int
	RetainMission                               *string
	RetainCustomInstructions                    *string
	RetainDefaultStrategy                       *string
	RetainStrategies                            *string
	RetainChunkBatchSize                        int
	RetainMaxAttachments                        int
	ConsolidationLLMBatchSize                   int
	ConsolidationLLMParallelism                 int
	ConsolidationMaxPerRound                    int
	ConsolidationSourceFactTokens               int
	ConsolidationSourceFactTokensPerObservation int
	ObservationsMission                         *string
	MaxObservationsPerScope                     int
	// ObservationScopeLimits, ConsolidationStrategies and
	// KnowledgePageDefaultTrigger are JSON-valued: the reference parses them out
	// of the environment, so the process layer holds the parsed container (or
	// null) rather than the raw string the deployment set.
	ObservationScopeLimits       any
	ConsolidationStrategies      any
	MentalModelMinRefreshSeconds int
	KnowledgePageDefaultTrigger  any
	// LLMGeminiSafetySettings is the reference's parsed JSON safety-settings
	// override (list | None). It is reported through the bank configuration
	// surface, which is where the reference exposes it.
	LLMGeminiSafetySettings     any
	ReflectMission              *string
	ReflectSourceFactsMaxTokens int
	ReflectDefaultOptions       map[string]any
	RecallIncludeChunks         bool
	RecallMaxTokens             int
	RecallChunksMaxTokens       int
	RecallBudgetFunction        string
	RecallBudgetFixedLow        int
	RecallBudgetFixedMid        int
	RecallBudgetFixedHigh       int
	RecallBudgetAdaptiveLow     float64
	RecallBudgetAdaptiveMid     float64
	RecallBudgetAdaptiveHigh    float64
	RecallBudgetMin             int
	RecallBudgetMax             int
	DispositionSkepticism       *int
	DispositionLiteralism       *int
	DispositionEmpathy          *int
}

// RecallTuning is the retrieval pipeline's process-level tuning.
type RecallTuning struct {
	GraphRetriever                string
	GraphSeedMinSimilarity        float64
	SemanticMinSimilarity         float64
	TemporalSemanticMinSimilarity float64
	SemanticLinkMinSimilarity     float64
	BM25MinScore                  float64
	BM25MaxQueryTerms             int
	BM25SelectiveTerms            bool
	LinkExpansionPerEntityLimit   int
	LinkExpansionTimeout          time.Duration
	MaxQueryTokens                int
	MaxConcurrent                 int
	// ConnectionBudget bounds the connections one recall's arms hold at once.
	ConnectionBudget int
	// DiagnosticPhases and PhaseSampleEvery drive the per-phase recall metrics.
	DiagnosticPhases bool
	PhaseSampleEvery int
}

// RetainTuning is the write path's process-level tuning.
type RetainTuning struct {
	MaxCompletionTokens        int
	ExtractCausalLinks         bool
	SubBatchTokens             int
	EntityLookup               string
	EntityResolutionBatchSize  int
	EntityResolutionCandidates int
	// MaxConcurrent bounds concurrent retain DB phases; SubBatchConcurrency
	// bounds the sub-batches of one document in flight; StoreMaxConcurrent
	// bounds the store-owned write phase; MemoryBudgetMB caps the unwritten
	// state a write may hold; BatchDocumentWrites writes documents as it goes.
	MaxConcurrent       int
	SubBatchConcurrency int
	StoreMaxConcurrent  int
	MemoryBudgetMB      int
	BatchDocumentWrites bool
	// FailOnExtractionErrors fails a retain on the first chunk whose extraction
	// failed, instead of storing the chunks that succeeded.
	FailOnExtractionErrors bool
}

// ReflectTuning is the reflection loop's process-level tuning.
type ReflectTuning struct {
	MaxIterations       int
	PromptCacheEnabled  bool
	MaxContextTokens    int
	MaxCompletionTokens *int
}

// ConsolidationTuning is the consolidation subsystem's process-level tuning.
type ConsolidationTuning struct {
	MemoryBatchSize           int
	MaxCompletionTokens       *int
	DedupThreshold            float64
	MaxAttempts               int
	MaxTokens                 int
	RecallBudget              string
	HistoryMaxEntries         int
	MentalModelHistoryEntries int
	// BankInfoCacheMaxEntries bounds the per-process bank-row cache, and
	// BankStatsCacheMaxEntries the stored bank-statistics summaries.
	BankInfoCacheMaxEntries  int
	BankStatsCacheMaxEntries int
}

// WorkerTuning is the queue scheduler's process-level tuning. The per-type
// reserved slots live on WorkerConfig.ReservedSlots, where the consumer reads
// them.
type WorkerTuning struct {
	ConsolidationByBank string
	BackpressureDefer   time.Duration
}

// DBTuning is the database session's process-level tuning.
type DBTuning struct {
	StatementTimeout         time.Duration
	CommandTimeout           time.Duration
	MaxParallelWorkersGather *int
	SessionSetupOnAcquire    bool
	MigrationDatabaseURL     string
	AcquireTimeout           time.Duration
	PoolSlowAcquireThreshold time.Duration
	AcquireWarnThreshold     time.Duration
	ANNIterativeScan         bool
	ANNMaxScanTuples         int
	// PGFunctionSchema and PGTokenizer are the ParadeDB pg_search backend's
	// function schema and tokenizer.
	PGFunctionSchema string
	PGTokenizer      string
}

// MetricsTuning is the metrics and health-probe surface's process-level tuning.
type MetricsTuning struct {
	IncludeBankID  bool
	BacklogEnabled bool
	IncludeTenant  bool
	WorkerLabel    bool
	LoopLagMetric  bool
	// LoopWatchdog starts the stall probe; the two durations below are the
	// reference watchdog's poll interval and stall threshold.
	LoopWatchdog      bool
	LoopWatchdogStall time.Duration
	LoopWatchdogPoll  time.Duration
}

// EntityTuning is the entity resolver's process-level tuning.
type EntityTuning struct {
	TRGMSimilarityThreshold float64
	IntrabatchMerge         float64
	MergeMinSimilarity      float64
}

// Admission tuning: the per-lane queue deadlines (see DefaultAdmissionMaxWaitS
// for the single-lane fallback the reference keeps for a lane with no key).
const (
	DefaultAdmissionRecallMaxWaitMS  = 30000
	DefaultAdmissionReflectMaxWaitMS = 5000
	DefaultAdmissionRetainMaxWaitMS  = 2000
)

// FeatureKeys is the version endpoint's feature-map key order: the reference's
// feature model declares its fields in this order and serialises them in it, so
// the served object must follow this declaration order rather than the
// alphabetical order a Go map would produce. FeatureMap's literal lists the same
// keys in the same order.
var FeatureKeys = []string{
	"observations",
	"mcp",
	"worker",
	"bank_config_api",
	"bank_llm_health",
	"file_upload_api",
	"document_export_api",
	"document_import_api",
	"audit_log",
	"llm_trace",
	"store_document_text",
}

// FeatureMap returns the version endpoint's feature map. Keys gate client
// behavior and reflect effective process configuration. The map is unordered;
// FeatureKeys carries the declaration order the version endpoint serialises in.
func (c Config) FeatureMap() map[string]bool {
	return map[string]bool{
		"observations":        c.EnableObservations,
		"mcp":                 c.MCPEnabled,
		"worker":              c.WorkerEnabled,
		"bank_config_api":     c.EnableBankConfigAPI,
		"bank_llm_health":     c.EnableBankLLMHealth,
		"file_upload_api":     c.EnableFileUploadAPI,
		"document_export_api": c.EnableDocumentExportAPI,
		"document_import_api": c.EnableDocumentImportAPI,
		"audit_log":           c.AuditLogEnabled,
		"llm_trace":           c.Provider.TraceEnabled,
		"store_document_text": c.StoreDocumentText,
	}
}

// Address returns the resolved listen address in host:port form.
func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// DatabaseURLMemory is the explicit opt-in to the process-local in-memory
// backend. It is not a URL: the reference has no in-memory backend, so an
// operator must name it deliberately rather than receiving it by leaving the
// URL unset.
const DatabaseURLMemory = "memory"

// ResolveDatabaseTarget resolves the persistence target the process will use.
//
// The reference's default database URL is "pg0", an embedded PostgreSQL it
// manages itself. This build cannot manage an embedded instance, and an unset
// URL would otherwise select the in-memory backend — so a deployment that
// configured no database would silently lose every write on restart while
// believing it was persisted. That is refused instead: the operator sets
// WENYUAN_API_DATABASE_URL to a postgresql:// URL, or opts into the
// non-persistent backend by naming it explicitly as "memory".
func (c Config) ResolveDatabaseTarget() (url string, inMemory bool, err error) {
	trimmed := strings.TrimSpace(c.DatabaseURL)
	if trimmed == "" {
		return "", false, errors.New(
			"WENYUAN_API_DATABASE_URL is not set: set it to a postgresql:// URL " +
				"to persist data, or set it to \"memory\" to run with a non-persistent " +
				"in-memory backend")
	}
	if strings.EqualFold(trimmed, DatabaseURLMemory) {
		return "", true, nil
	}
	return trimmed, false, nil
}

// FromEnv resolves configuration from environment values and built-in
// defaults, before command-line flag parsing has a chance to override them.
func FromEnv(lookup func(string) (string, bool)) Config {
	// Every value read is recorded, so a configured key of a family this build
	// does not implement can be refused by name instead of being dropped.
	rawEnv := map[string]string{}
	base := lookup
	lookup = func(key string) (string, bool) {
		value, ok := base(key)
		if ok && strings.TrimSpace(value) != "" {
			rawEnv[key] = value
		}
		return value, ok
	}
	cfg := Config{
		Host:                              DefaultHost,
		Port:                              DefaultPort,
		DatabaseSchema:                    DefaultDatabaseSchema,
		VectorExtension:                   DefaultVectorExtension,
		TextSearchExtension:               DefaultTextSearchExtension,
		TextSearchExtensionNativeLanguage: DefaultTextSearchExtensionNativeLanguage,
		GzipMinSize:                       DefaultGzipMinSize,
		LogLevel:                          DefaultLogLevel,
		Workers:                           DefaultWorkers,
		ReadTimeout:                       DefaultReadTimeoutSeconds * time.Second,
		RetryAfter:                        DefaultRetryAfterSeconds * time.Second,
		BindGrace:                         DefaultMigrationGraceWindow,
	}

	if v, ok := lookup(EnvHost); ok && strings.TrimSpace(v) != "" {
		cfg.Host = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvPort); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			cfg.Port = n
		}
	}
	if v, ok := lookup(EnvDatabaseURL); ok {
		cfg.DatabaseURL = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvDatabaseSchema); ok && strings.TrimSpace(v) != "" {
		cfg.DatabaseSchema = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvVectorExtension); ok && strings.TrimSpace(v) != "" {
		cfg.VectorExtension = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvTextSearchExtension); ok && strings.TrimSpace(v) != "" {
		cfg.TextSearchExtension = strings.TrimSpace(v)
	}
	// The reference lowercases the dictionary before validating it as a
	// PostgreSQL identifier, so the resolved value is already lowercase here;
	// the identifier check itself happens where the expression is built.
	if v, ok := lookup(EnvTextSearchExtensionNativeLanguage); ok && strings.TrimSpace(v) != "" {
		cfg.TextSearchExtensionNativeLanguage = strings.ToLower(strings.TrimSpace(v))
	}
	cfg.RunMigrationsOnStartup = boolFromEnv(lookup, EnvRunMigrationsOnStartup, DefaultRunMigrationsOnStartup)
	// The defaults are the reference's own defaults, so a deployment that sets
	// only the values it cares about sees the same surface either way.
	cfg.MCPEnabled = boolFromEnv(lookup, EnvMCPEnabled, true)
	cfg.EnableBankConfigAPI = boolFromEnv(lookup, EnvEnableBankConfigAPI, true)
	cfg.EnableBankLLMHealth = boolFromEnv(lookup, EnvEnableBankLLMHealth, false)
	cfg.EnableFileUploadAPI = boolFromEnv(lookup, EnvEnableFileUploadAPI, true)
	cfg.EnableObservations = boolFromEnv(lookup, EnvEnableObservations, true)
	cfg.DatabaseBackend = databaseBackend(lookup)
	cfg.WebhookGlobal = resolveWebhookGlobal(lookup)
	cfg.ExtensionPassThroughHeaders = stringListFromEnv(lookup, EnvExtensionPassThroughHdr)
	cfg.DefaultBankTemplate = jsonObjectFromEnv(lookup, EnvDefaultBankTemplate)
	cfg.ReadReplica = ReadReplicaConfig{
		URL:     strings.TrimSpace(stringFromEnv(lookup, EnvReadDatabaseURL)),
		PoolMin: intFromEnv(lookup, EnvReadDBPoolMinSize, DefaultReadPoolMinSize),
		PoolMax: intFromEnv(lookup, EnvReadDBPoolMaxSize, DefaultReadPoolMaxSize),
	}
	cfg.MCPAuthToken = strings.TrimSpace(stringFromEnv(lookup, EnvMCPAuthToken))
	cfg.MCPInstructions = stringFromEnv(lookup, EnvMCPInstructions)
	cfg.MCPEnabledTools = mcpEnabledToolsFromEnv(lookup)
	cfg.EnableAutoConsolidation = boolFromEnv(lookup, EnvEnableAutoConsolidation, true)
	cfg.AuditLogEnabled = boolFromEnv(lookup, EnvAuditLogEnabled, false)
	cfg.AuditLogActions = stringListFromEnv(lookup, EnvAuditLogActions)
	cfg.AccessLog = boolFromEnv(lookup, EnvAccessLog, false)
	cfg.LogFormat = strings.ToLower(stringFromEnv(lookup, EnvLogFormat))
	if cfg.LogFormat == "" {
		cfg.LogFormat = DefaultLogFormat
	}
	cfg.LogJSONFields = stringListFromEnv(lookup, EnvLogJSONFields)
	cfg.RetainExtractionMode = stringFromEnv(lookup, EnvRetainExtractionMode)
	if strings.TrimSpace(cfg.RetainExtractionMode) == "" {
		cfg.RetainExtractionMode = DefaultRetainExtractionMode
	}
	cfg.RetainOptionalFactDimensions = boolFromEnv(lookup, EnvRetainOptionalFactDimensions, false)
	cfg.TokenizerEncoding = stringFromEnv(lookup, EnvTokenizerEncoding)
	if strings.TrimSpace(cfg.TokenizerEncoding) == "" {
		cfg.TokenizerEncoding = DefaultTokenizerEncoding
	}
	cfg.FileParser = stringListFromEnv(lookup, EnvFileParser)
	if len(cfg.FileParser) == 0 {
		cfg.FileParser = []string{DefaultFileParser}
	}
	cfg.FileParserAllowlist = stringListFromEnv(lookup, EnvFileParserAllowlist)
	cfg.FileParserIrisToken = stringFromEnv(lookup, EnvFileParserIrisToken)
	cfg.FileParserIrisOrgID = stringFromEnv(lookup, EnvFileParserIrisOrgID)
	cfg.FileParserLlamaParseAPIKey = stringFromEnv(lookup, EnvFileParserLlamaParseAPIKey)
	cfg.FileParserOCREnabled = boolFromEnv(lookup, EnvFileParserMarkitdownOCREnabled, false)
	cfg.FileParserOCRAPIKey = stringFromEnv(lookup, EnvFileParserMarkitdownOCRAPIKey)
	cfg.FileParserOCRBaseURL = stringFromEnv(lookup, EnvFileParserMarkitdownOCRBaseURL)
	cfg.FileParserOCRModel = stringFromEnv(lookup, EnvFileParserMarkitdownOCRModel)
	cfg.FileParserOCRPrompt = stringFromEnv(lookup, EnvFileParserMarkitdownOCRPrompt)
	cfg.FileParserOCRHeaders = stringMapFromEnv(lookup, EnvFileParserMarkitdownOCRHeaders)
	cfg.FileConversionMaxBatchSize = intFromEnv(lookup, EnvFileConversionMaxBatchSize, DefaultFileConversionMaxBatchSize)
	cfg.FileConversionMaxBatchSizeMB = intFromEnv(lookup, EnvFileConversionMaxBatchSizeMB, DefaultFileConversionMaxBatchSizeMB)
	cfg.FileDeleteAfterRetain = boolFromEnv(lookup, EnvFileDeleteAfterRetain, DefaultFileDeleteAfterRetain)
	cfg.StoreDocumentText = boolFromEnv(lookup, EnvStoreDocumentText, true)
	cfg.Profile = stringFromEnv(lookup, EnvProfile)
	if v := stringFromEnv(lookup, EnvLogLevel); v != "" {
		cfg.LogLevel = v
	}
	cfg.TenantExtension = stringFromEnv(lookup, EnvTenantExtension)
	cfg.TenantAPIKey = stringFromEnv(lookup, EnvTenantAPIKey)
	cfg.TenantMCPAuthDisabled = boolFromEnv(lookup, EnvTenantMCPAuthDisabled, false)
	cfg.OperationValidatorExtension = stringFromEnv(lookup, EnvOperationValidatorExtension)
	cfg.PprofEnabled = boolFromEnv(lookup, EnvPprofEnabled, false)
	// The worker count and daemon log are the reference's own keys: a launcher
	// that exports WENYUAN_API_WORKERS, or WENYUAN_API_DAEMON_LOG so the
	// detach writes somewhere it can read, gets the same resolution here.
	cfg.Workers = intFromEnv(lookup, EnvWorkers, DefaultWorkers)
	cfg.DaemonLog = strings.TrimSpace(stringFromEnv(lookup, EnvDaemonLog))
	// FORWARDED_ALLOW_IPS is the reference's server's own variable (not a
	// WENYUAN_API_ key): the trusted-proxy allow list is resolved there, in
	// the HTTP server, and this build reads it from the same place.
	cfg.ForwardedAllowIPs = forwardedAllowIPsFromEnv(lookup)
	// The reference's tuning surface: each group is resolved here so a
	// deployment that exported its reference .env runs with the same knobs.
	cfg.ProcessDefaults = resolveProcessDefaults(lookup)
	cfg.RecallTuning = resolveRecallTuning(lookup)
	cfg.RetainTuning = resolveRetainTuning(lookup)
	cfg.ReflectTuning = resolveReflectTuning(lookup)
	cfg.ConsolidationTuning = resolveConsolidationTuning(lookup)
	cfg.WorkerTuning = resolveWorkerTuning(lookup)
	cfg.DBTuning = resolveDBTuning(lookup)
	cfg.MetricsTuning = resolveMetricsTuning(lookup)
	cfg.EntityTuning = resolveEntityTuning(lookup)
	cfg.ModelInitTimeout = secondsDurationFromEnv(lookup, EnvModelInitTimeout, DefaultModelInitTimeoutS)
	cfg.SkipLLMVerification = boolFromEnv(lookup, EnvSkipLLMVerification, false)
	cfg.BasePath = normalizeBasePath(stringFromEnv(lookup, EnvBasePath))
	// The stall probe has two spellings in the reference: the watchdog's own
	// millisecond keys, which default on, and the lag reporter's second-valued
	// keys, which default off. The reporter's interval overrides the watchdog's
	// poll cadence when it was set; otherwise the watchdog's own interval is
	// used, which is what makes the probe run by default.
	cfg.LoopLagReport = secondsFromEnv(lookup, EnvLoopLagReportSeconds, DefaultLoopLagReportS*time.Second)
	cfg.LoopLagStall = secondsFromEnv(lookup, EnvLoopLagStallSeconds, DefaultLoopLagStallS*time.Second)
	cfg.LoopWatchdogInterval = cfg.MetricsTuning.LoopWatchdogPoll
	if cfg.LoopLagReport > 0 {
		cfg.LoopWatchdogInterval = cfg.LoopLagReport
	}
	cfg.LoopWatchdogStall = cfg.MetricsTuning.LoopWatchdogStall
	if cfg.LoopLagStall > 0 && cfg.LoopLagStall != DefaultLoopLagStallS*time.Second {
		cfg.LoopWatchdogStall = cfg.LoopLagStall
	}
	cfg.AdmissionMaxWait = secondsFromEnv(lookup, EnvAdmissionMaxWaitSeconds, DefaultAdmissionMaxWaitS*time.Second)
	cfg.AdmissionRecallMaxWait = millisFromEnv(lookup, EnvAdmissionRecallMaxWaitMS, DefaultAdmissionRecallMaxWaitMS*time.Millisecond)
	cfg.AdmissionReflectMaxWait = millisFromEnv(lookup, EnvAdmissionReflectMaxWaitMS, DefaultAdmissionReflectMaxWaitMS*time.Millisecond)
	cfg.AdmissionRetainMaxWait = millisFromEnv(lookup, EnvAdmissionRetainMaxWaitMS, DefaultAdmissionRetainMaxWaitMS*time.Millisecond)
	cfg.AdmissionRecallLimit = intFromEnv(lookup, EnvAdmissionRecallLimit, DefaultAdmissionLimit)
	cfg.AdmissionRetainLimit = intFromEnv(lookup, EnvAdmissionRetainLimit, DefaultAdmissionLimit)
	cfg.AdmissionReflectLimit = intFromEnv(lookup, EnvAdmissionReflectLimit, DefaultAdmissionLimit)
	// The reference spells the per-lane in-flight ceiling as
	// ADMISSION_<LANE>_MAX_IN_FLIGHT. A positive value overrides the shipped
	// default; the reference's 0 means "derive from the core count", which this
	// build has no formula for, so 0 keeps the default.
	if n := intFromEnv(lookup, EnvAdmissionRecallMaxInFlight, 0); n > 0 {
		cfg.AdmissionRecallLimit = n
	}
	if n := intFromEnv(lookup, EnvAdmissionReflectMaxInFlight, 0); n > 0 {
		cfg.AdmissionReflectLimit = n
	}
	if n := intFromEnv(lookup, EnvAdmissionRetainMaxInFlight, 0); n > 0 {
		cfg.AdmissionRetainLimit = n
	}
	cfg.EnableDocumentExportAPI = boolFromEnv(lookup, EnvEnableDocumentExportAPI, true)
	cfg.EnableDryRunExtract = boolFromEnv(lookup, EnvEnableDryRunExtract, true)
	cfg.EnableObservationHistory = boolFromEnv(lookup, EnvEnableObservationHistory, true)
	cfg.EnableMentalModelHistory = boolFromEnv(lookup, EnvEnableMentalModelHistory, true)
	cfg.EnableDocumentImportAPI = boolFromEnv(lookup, EnvEnableDocumentImportAPI, true)
	cfg.Worker = resolveWorker(lookup)
	cfg.Tracing = resolveTracing(lookup)
	cfg.VectorIndex = resolveVectorIndex(lookup)
	cfg.Recency = resolveRecency(lookup)
	cfg.RetainAttachmentMaxSizeMB, cfg.RetainAttachmentMaxCount = resolveUploadLimits(lookup)
	cfg.DBPoolMaxSize = intFromEnv(lookup, EnvDBPoolMaxSize, DefaultDBPoolMaxSize)
	cfg.DBPoolMinSize = intFromEnv(lookup, EnvDBPoolMinSize, DefaultDBPoolMinSize)
	cfg.BankInfoCacheTTL = cacheTTLFromEnv(lookup, EnvBankInfoCacheTTLSeconds, DefaultBankInfoCacheTTLSeconds*time.Second)
	cfg.BankAliasCacheTTL = cacheTTLFromEnv(lookup, EnvBankAliasCacheTTLSeconds, DefaultBankAliasCacheTTLSeconds*time.Second)
	cfg.BankAliasCacheMaxEntries = intFromEnv(lookup, EnvBankAliasCacheMaxEntries, DefaultBankAliasCacheMaxEntries)
	cfg.BankStatsCacheTTL = cacheTTLFromEnv(lookup, EnvBankStatsCacheTTLSeconds, DefaultBankStatsCacheTTLSeconds*time.Second)
	cfg.WorkerEnabled = boolFromEnv(lookup, EnvWorkerEnabled, DefaultWorkerEnabled)
	cfg.Maintenance = resolveMaintenance(lookup)
	cfg.WebhookAllowInsecure = boolFromEnv(lookup, EnvWebhookAllowInsecure, false)
	cfg.WebhookAllowPrivateHosts = boolFromEnv(lookup, EnvWebhookAllowPrivateHosts, false)
	cfg.WebhookAllowedHosts = hostListFromEnv(lookup, EnvWebhookAllowedHosts)
	cfg.RetainBatchEnabled = boolFromEnv(lookup, EnvRetainBatchEnabled, false)
	cfg.FileStorage = resolveFileStorage(lookup)
	if v, ok := lookup(EnvRecallStrategyBoosts); ok {
		cfg.RecallStrategyBoosts = ParseStrategyBoosts(v)
	}
	if v, ok := lookup(EnvReflectSearchObservationsMaxTokens); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			cfg.ReflectSearchObservationsMaxTokens = &n
		}
	}
	if v, ok := lookup(EnvReflectSearchObservationsIncludeEntities); ok {
		if b, ok := parseBoolValue(v); ok {
			cfg.ReflectSearchObservationsIncludeEntities = &b
		}
	}
	if v, ok := lookup(EnvGzipMinSize); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			cfg.GzipMinSize = n
		}
	}
	cfg.Provider = ResolveProvider(lookup)
	cfg.ProviderScopes = ResolveScopes(lookup)
	cfg.Provider.StrictSchema = cfg.ProviderScopes.StrictSchema
	cfg.Consolidation = ResolveConsolidation(lookup)
	cfg.RetainLLM = ResolveLLMScope(lookup, "WENYUAN_API_RETAIN_LLM")
	cfg.ReflectLLM = ResolveLLMScope(lookup, "WENYUAN_API_REFLECT_LLM")
	cfg.MentalModelRefreshLLM = ResolveLLMScope(lookup, "WENYUAN_API_MENTAL_MODEL_REFRESH_LLM")
	cfg.rawEnv = rawEnv
	cfg.Unsupported = collectUnsupportedRuntimeKeys(lookup)
	cfg.recordUnsupportedProviderKeys(lookup)
	cfg.recordUnsupportedProviderFields()
	return cfg
}

// DefaultBankInfoCacheTTLSeconds and DefaultBankStatsCacheTTLSeconds are the
// reference's cache lifetimes, in seconds.
const (
	DefaultBankInfoCacheTTLSeconds  = 30
	DefaultBankStatsCacheTTLSeconds = 60
)

// cacheTTLFromEnv reads a cache lifetime in seconds. An unset or unparseable
// value yields the fallback; an explicit 0 (or a negative value) disables the
// cache, which is the reference's convention.
func cacheTTLFromEnv(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// collectUnsupportedRuntimeKeys records every reference runtime key that is
// set to a non-blank value in the environment but that this build cannot act
// on. Refusing such a key at startup is the explicit answer to a silent no-op:
// a copied reference .env is either honoured or rejected, never partially
// ignored. A blank value is treated as "not configured", matching how the rest
// of the surface reads an empty variable.
func collectUnsupportedRuntimeKeys(lookup func(string) (string, bool)) *Unsupported {
	unsupported := &Unsupported{}
	for _, key := range referenceRuntimeKeys {
		if key.Delegated != "" {
			continue
		}
		value, ok := lookup(key.Name)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		// A value-conditional refusal comes first because it also governs keys
		// this build resolves: a key can be honoured for one value and refused
		// for the rest, which the resolved/refused split alone cannot express.
		if key.UnsupportedWhen != nil {
			if key.UnsupportedWhen(value) {
				unsupported.Add(key.Name, key.Reason)
			}
			continue
		}
		if key.Resolve != nil {
			continue
		}
		unsupported.Add(key.Name, key.Reason)
	}
	return unsupported
}

// FromEnvironment resolves configuration from the live process environment.
func FromEnvironment() Config {
	return FromEnv(func(key string) (string, bool) { return lookupEnv(key) })
}

// Strategy boost levels accepted by the recall-strategy-boost configuration.
const (
	BoostLevelLow    = "low"
	BoostLevelMedium = "medium"
	BoostLevelHigh   = "high"
)

// ParseStrategyBoosts parses a comma-separated list of arm:level pairs into a
// typed boost map. Unrecognized arms or levels are ignored; an unset or empty
// value yields a nil map so every arm stays unboosted.
func ParseStrategyBoosts(value string) map[string]string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(trimmed, ",") {
		arm, level, ok := strings.Cut(pair, ":")
		arm = strings.TrimSpace(arm)
		level = strings.ToLower(strings.TrimSpace(level))
		if !ok || arm == "" || !validBoostLevel(level) {
			continue
		}
		out[arm] = level
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validBoostLevel reports whether a level value is one of the accepted boosts.
func validBoostLevel(level string) bool {
	switch level {
	case BoostLevelLow, BoostLevelMedium, BoostLevelHigh:
		return true
	default:
		return false
	}
}

func boolFromEnv(lookup func(string) (string, bool), key string, fallback bool) bool {
	v, ok := lookup(key)
	if !ok {
		return fallback
	}
	if parsed, valid := parseBoolValue(v); valid {
		return parsed
	}
	return fallback
}

// parseBoolValue parses a boolean environment value, reporting whether it was
// recognized.
func parseBoolValue(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// normalizeBasePath trims a configured base path to the form the router
// matches: no trailing slash, and a leading slash unless empty.
func normalizeBasePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return strings.TrimRight(value, "/")
}

// BuiltinAPIKeyTenantExtension is the reference's built-in tenant authenticator,
// selected by WENYUAN_API_TENANT_EXTENSION from the package that ships it.
// Selecting it requires WENYUAN_API_TENANT_API_KEY.
const BuiltinAPIKeyTenantExtension = "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension"

// TenantAuth is the resolved tenant authentication policy.
type TenantAuth struct {
	// APIKey is the credential the authenticator accepts. Empty means no
	// authentication is configured, which is the reference's default.
	APIKey string
	// MCPAuthDisabled exempts the MCP namespace from authentication.
	MCPAuthDisabled bool
}

// Enabled reports whether requests are authenticated at all.
func (a TenantAuth) Enabled() bool { return a.APIKey != "" }

// ResolveTenantAuth validates the tenant extension selection.
//
// The reference loads the configured class from an importable module. A Go
// build cannot import a Python class, so the one extension whose behavior is
// specified here is supported by name and every other selection is refused at
// startup: a deployment that asked for authentication and silently got none
// would be worse off than one that will not start.
func (c Config) ResolveTenantAuth() (TenantAuth, error) {
	if c.TenantExtension == "" {
		return TenantAuth{}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(c.TenantExtension), BuiltinAPIKeyTenantExtension) {
		return TenantAuth{}, fmt.Errorf(
			"%s=%q is not supported by this build: only the built-in %q is implemented",
			EnvTenantExtension, c.TenantExtension, BuiltinAPIKeyTenantExtension)
	}
	if strings.TrimSpace(c.TenantAPIKey) == "" {
		return TenantAuth{}, fmt.Errorf(
			"%s is required when using ApiKeyTenantExtension", EnvTenantAPIKey)
	}
	return TenantAuth{APIKey: c.TenantAPIKey, MCPAuthDisabled: c.TenantMCPAuthDisabled}, nil
}

// JSONLogs reports whether the process writes structured JSON records.
func (c Config) JSONLogs() bool { return strings.EqualFold(strings.TrimSpace(c.LogFormat), "json") }

// ValidLogFormat reports whether the format is one the reference accepts.
func ValidLogFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text", "json":
		return true
	}
	return false
}

// ValidRetainExtractionMode reports whether the mode is one the reference
// accepts. An invalid value is refused at startup rather than silently falling
// back, because whoever set it asked for that extraction shape.
func ValidRetainExtractionMode(mode string) bool {
	for _, known := range strings.Split(RetainExtractionModes, ", ") {
		if strings.EqualFold(strings.TrimSpace(mode), known) {
			return true
		}
	}
	return false
}

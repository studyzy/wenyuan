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
	EnvHost                   = "HINDSIGHT_API_HOST"
	EnvPort                   = "HINDSIGHT_API_PORT"
	EnvDatabaseURL            = "HINDSIGHT_API_DATABASE_URL"
	EnvDatabaseSchema         = "HINDSIGHT_API_DATABASE_SCHEMA"
	EnvVectorExtension        = "HINDSIGHT_API_VECTOR_EXTENSION"
	EnvTextSearchExtension    = "HINDSIGHT_API_TEXT_SEARCH_EXTENSION"
	EnvRunMigrationsOnStartup = "HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP"
	EnvMCPEnabled             = "HINDSIGHT_API_MCP_ENABLED"
	EnvEnableBankConfigAPI    = "HINDSIGHT_API_ENABLE_BANK_CONFIG_API"
	// EnvEnableBankLLMHealth gates the per-bank /health/llm route. It is off by
	// default, matching the reference.
	EnvEnableBankLLMHealth = "HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH"
	// EnvEnableFileUploadAPI gates the file retain route. It is on by default,
	// matching the reference.
	EnvEnableFileUploadAPI = "HINDSIGHT_API_ENABLE_FILE_UPLOAD_API"
	// EnvEnableDocumentExportAPI gates the document export routes of the
	// archive transfer family.
	EnvEnableDocumentExportAPI = "HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API"
	// EnvEnableDocumentImportAPI gates the document import routes of the
	// archive transfer family.
	EnvEnableDocumentImportAPI = "HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API"

	// EnvWebhookAllowInsecure permits a plain-HTTP webhook target. Off by
	// default so a delivery cannot be downgraded to cleartext.
	EnvWebhookAllowInsecure = "HINDSIGHT_API_WEBHOOK_ALLOW_INSECURE"
	// EnvWebhookAllowPrivateHosts permits a webhook target inside the private
	// network. Off by default, because a registered URL is fetched by the
	// server and an unchecked one is a server-side request forgery.
	EnvWebhookAllowPrivateHosts = "HINDSIGHT_API_WEBHOOK_ALLOW_PRIVATE_HOSTS"
	// EnvWebhookAllowedHosts narrows webhook targets to an explicit host set.
	EnvWebhookAllowedHosts = "HINDSIGHT_API_WEBHOOK_ALLOWED_HOSTS"

	// EnvWorkerID identifies this queue consumer among consumers sharing one
	// queue, so a claim can be traced back to its holder.
	EnvWorkerID = "HINDSIGHT_API_WORKER_ID"
	// EnvWorkerEnabled controls whether the API process also runs the queue
	// consumer and the maintenance loop. It defaults to true, matching the
	// reference's standalone mode where one process serves and drains.
	EnvWorkerEnabled = "HINDSIGHT_API_WORKER_ENABLED"
	// EnvWorkerMaxRetries is the attempt budget beyond the first try.
	EnvWorkerMaxRetries = "HINDSIGHT_API_WORKER_MAX_RETRIES"
	// EnvWorkerConcurrency is how many operations one consumer processes at once.
	EnvWorkerConcurrency = "HINDSIGHT_API_WORKER_CONCURRENCY"
	// EnvWorkerLeaseSeconds bounds how long a claim stays valid before another
	// consumer may reclaim the work.
	EnvWorkerLeaseSeconds = "HINDSIGHT_API_WORKER_LEASE_SECONDS"
	// EnvWorkerPollSeconds is how long to wait between empty claims.
	EnvWorkerPollSeconds = "HINDSIGHT_API_WORKER_POLL_SECONDS"
	// EnvWorkerHTTPPort is the port the worker's health/metrics surface binds.
	EnvWorkerHTTPPort = "HINDSIGHT_API_WORKER_HTTP_PORT"
	// The following are the reference's worker key names, accepted as aliases
	// alongside this project's own spellings so an existing deployment's env
	// is honoured.
	EnvWorkerPollIntervalMS       = "HINDSIGHT_API_WORKER_POLL_INTERVAL_MS"
	EnvWorkerMaxSlots             = "HINDSIGHT_API_WORKER_MAX_SLOTS"
	EnvWorkerTaskRetryBackoffSecs = "HINDSIGHT_API_WORKER_TASK_RETRY_BACKOFF_SECONDS"
	// Per-operation wall ceilings. A value of zero means the claim's lease is
	// the only bound.
	EnvWorkerRetainWallSeconds        = "HINDSIGHT_API_WORKER_RETAIN_WALL_TIMEOUT_SECONDS"
	EnvWorkerConsolidationWallSeconds = "HINDSIGHT_API_WORKER_CONSOLIDATION_WALL_TIMEOUT_SECONDS"
	// EnvWorkerConsolidationReservedSlots floors the concurrency reserved for
	// consolidation, so a bank mid-bulk-ingest cannot starve observation
	// folding. Default 2.
	EnvWorkerConsolidationReservedSlots = "HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS"
	// EnvDBPoolMaxSize bounds simultaneous database connections. The reference
	// defaults it to 100; zero leaves the driver's own default.
	EnvDBPoolMaxSize = "HINDSIGHT_API_DB_POOL_MAX_SIZE"
	// EnvDBPoolMinSize is the number of connections kept open when unused.
	EnvDBPoolMinSize = "HINDSIGHT_API_DB_POOL_MIN_SIZE"
	// EnvRetainAttachmentMaxSizeMB bounds one inline attachment or uploaded
	// file, in megabytes. The reference's static server-level upload limit.
	EnvRetainAttachmentMaxSizeMB = "HINDSIGHT_API_RETAIN_ATTACHMENT_MAX_SIZE_MB"
	// EnvRetainAttachmentMaxCount bounds how many attachments or files one
	// request may carry.
	EnvRetainAttachmentMaxCount = "HINDSIGHT_API_RETAIN_ATTACHMENT_MAX_COUNT"
	// EnvRecencyDecayFunction selects the age-to-freshness curve
	// (linear, exponential, none).
	EnvRecencyDecayFunction = "HINDSIGHT_API_RECENCY_DECAY_FUNCTION"
	// EnvRecencyDecayLinearWindowDays is the age at which the linear curve
	// reaches its floor.
	EnvRecencyDecayLinearWindowDays = "HINDSIGHT_API_RECENCY_DECAY_LINEAR_WINDOW_DAYS"
	// EnvRecencyDecayHalflifeDays is the exponential curve's half-life.
	EnvRecencyDecayHalflifeDays = "HINDSIGHT_API_RECENCY_DECAY_HALFLIFE_DAYS"
	// EnvRecallMaxCandidatesPerSource caps each recall retrieval arm before
	// fusion, so one over-expanding backend cannot crowd the others out of the
	// merged pool. Zero disables the cap.
	EnvRecallMaxCandidatesPerSource = "HINDSIGHT_API_RECALL_MAX_CANDIDATES_PER_SOURCE"
	// EnvVectorIndexMinRows is the row count at which a (bank, fact_type)
	// partition earns its own partial vector index. Zero disables reconciliation.
	EnvVectorIndexMinRows = "HINDSIGHT_API_VECTOR_INDEX_MIN_ROWS"
	// EnvVectorIndexReconcileInterval is the sweep interval in seconds. It is
	// the reference's key, named as the reference names it.
	EnvVectorIndexReconcileInterval = "HINDSIGHT_API_VECTOR_INDEX_MAINTENANCE_MIN_INTERVAL_SECONDS"
	// EnvOTELTracesEnabled turns OpenTelemetry span export on. Default off.
	EnvOTELTracesEnabled = "HINDSIGHT_API_OTEL_TRACES_ENABLED"
	// EnvOTELExporterOTLPEndpoint is the OTLP/HTTP collector endpoint.
	EnvOTELExporterOTLPEndpoint = "HINDSIGHT_API_OTEL_EXPORTER_OTLP_ENDPOINT"
	// EnvOTELExporterOTLPHeaders is the comma-separated key=value header list
	// sent on every export request.
	EnvOTELExporterOTLPHeaders = "HINDSIGHT_API_OTEL_EXPORTER_OTLP_HEADERS"
	// EnvOTELServiceName is the service.name resource attribute.
	EnvOTELServiceName = "HINDSIGHT_API_OTEL_SERVICE_NAME"
	// EnvOTELDeploymentEnvironment is the deployment.environment attribute.
	EnvOTELDeploymentEnvironment = "HINDSIGHT_API_OTEL_DEPLOYMENT_ENVIRONMENT"
	EnvWorkerReflectWallSeconds  = "HINDSIGHT_API_WORKER_REFLECT_WALL_TIMEOUT_SECONDS"
	// EnvWorkerInitialBackoffSeconds is the first retry delay.
	EnvWorkerInitialBackoffSeconds = "HINDSIGHT_API_WORKER_INITIAL_BACKOFF_SECONDS"
	// EnvWorkerMaxBackoffSeconds is the retry backoff ceiling.
	EnvWorkerMaxBackoffSeconds = "HINDSIGHT_API_WORKER_MAX_BACKOFF_SECONDS"
	// EnvRetentionSweepIntervalSeconds is how often the retention jobs run.
	EnvRetentionSweepIntervalSeconds = "HINDSIGHT_API_RETENTION_SWEEP_INTERVAL_SECONDS"
	// EnvAuditLogRetentionDays is how long an audit row is kept; a negative
	// value keeps them forever.
	EnvAuditLogRetentionDays = "HINDSIGHT_API_AUDIT_LOG_RETENTION_DAYS"
	// EnvConsolidationReconcileIntervalSeconds is how often banks holding
	// stranded consolidation facts are re-scheduled.
	EnvConsolidationReconcileIntervalSeconds = "HINDSIGHT_API_CONSOLIDATION_RECONCILE_INTERVAL_SECONDS"
	// EnvMentalModelRefreshTickSeconds is how often scheduled mental models are
	// checked for due-ness.
	EnvMentalModelRefreshTickSeconds = "HINDSIGHT_API_MENTAL_MODEL_REFRESH_TICK_SECONDS"
	// EnvOperationCleanupIntervalSeconds is how often terminal operations are
	// pruned.
	EnvOperationCleanupIntervalSeconds = "HINDSIGHT_API_OPERATION_CLEANUP_INTERVAL_SECONDS"
	// EnvOperationRetentionDays is how long a terminal operation is kept; zero
	// disables the prune.
	EnvOperationRetentionDays = "HINDSIGHT_API_OPERATION_RETENTION_DAYS"
	// EnvOperationCleanupBatchSize bounds one prune per schema per run.
	EnvOperationCleanupBatchSize = "HINDSIGHT_API_OPERATION_CLEANUP_BATCH_SIZE"
	// EnvMaintenanceStartJitterSeconds spreads the first maintenance tick, so a
	// fleet started together does not fire every probe at once.
	EnvMaintenanceStartJitterSeconds = "HINDSIGHT_API_MAINTENANCE_START_JITTER_SECONDS"
	// EnvGzipMinSize bounds the response size above which a body is compressed.
	EnvGzipMinSize = "HINDSIGHT_API_GZIP_MIN_SIZE"
	// EnvProfile holds the reference's profiling request as raw JSON, e.g.
	// {"every":60,"top":20}. The profile is emitted to the log stream; the
	// reference exposes no profiling endpoint.
	EnvProfile = "HINDSIGHT_API_PROFILE"
	// EnvPprofEnabled is this project's own switch for the Go pprof endpoints
	// under /debug/pprof. It is deliberately separate from EnvProfile, which
	// the reference owns with a different meaning.
	EnvPprofEnabled = "HINDSIGHT_API_PPROF_ENABLED"
	// EnvBasePath serves every route under a path prefix as well as at the
	// root, mirroring the reference's FastAPI root_path.
	EnvBasePath = "HINDSIGHT_API_BASE_PATH"
	// EnvEnableObservations gates observations (auto-consolidation). On by
	// default, like the reference.
	EnvEnableObservations = "HINDSIGHT_API_ENABLE_OBSERVATIONS"
	// EnvAuditLogEnabled gates audit logging by default; a bank may override.
	// Off by default, like the reference.
	EnvAuditLogEnabled = "HINDSIGHT_API_AUDIT_LOG_ENABLED"
	// EnvStoreDocumentText controls whether raw source text is persisted. On
	// by default, like the reference.
	EnvStoreDocumentText = "HINDSIGHT_API_STORE_DOCUMENT_TEXT"
	// EnvTenantExtension selects the tenant authenticator by its class path,
	// the reference's extension convention. Unset means no authentication,
	// which is the reference's single-tenant default.
	EnvTenantExtension = "HINDSIGHT_API_TENANT_EXTENSION"
	// EnvTenantAPIKey is the credential the built-in API-key authenticator
	// accepts.
	EnvTenantAPIKey = "HINDSIGHT_API_TENANT_API_KEY"
	// EnvTenantMCPAuthDisabled exempts the MCP namespace from tenant
	// authentication, which a deployment that fronts MCP with its own gate
	// needs.
	EnvTenantMCPAuthDisabled = "HINDSIGHT_API_TENANT_MCP_AUTH_DISABLED"
	// EnvOperationValidatorExtension selects the operation validator by its
	// class path, the reference's extension convention. The one this build
	// implements natively is the reference suite's tag-scope validator, which
	// confines a "bank:<id>" API key to writing only its own bank.
	EnvOperationValidatorExtension = "HINDSIGHT_API_OPERATION_VALIDATOR_EXTENSION"
	// EnvLoopLagReportSeconds is how often the loop-lag probe samples the
	// scheduler; EnvLoopLagStallSeconds is the delay above which it reports a
	// stall.
	EnvLoopLagReportSeconds = "HINDSIGHT_API_LOOP_LAG_REPORT_SECONDS"
	EnvLoopLagStallSeconds  = "HINDSIGHT_API_LOOP_LAG_STALL_SECONDS"
	// Admission keys bound how many expensive operations run at once.
	EnvAdmissionMaxWaitSeconds = "HINDSIGHT_API_ADMISSION_MAX_WAIT_SECONDS"
	EnvAdmissionRecallLimit    = "HINDSIGHT_API_ADMISSION_RECALL_LIMIT"
	EnvAdmissionRetainLimit    = "HINDSIGHT_API_ADMISSION_RETAIN_LIMIT"
	EnvAdmissionReflectLimit   = "HINDSIGHT_API_ADMISSION_REFLECT_LIMIT"
	EnvRetainBatchEnabled      = "HINDSIGHT_API_RETAIN_BATCH_ENABLED"
	// EnvFileStorageType selects the attachment storage backend. Empty or
	// "postgresql" keeps payloads in the database, which is the default.
	EnvFileStorageType = "HINDSIGHT_API_FILE_STORAGE_TYPE"
	// The S3 family configures the S3-compatible backend, which also serves
	// MinIO, Cloudflare R2, Tigris and any other S3-compliant endpoint.
	EnvFileStorageS3Bucket          = "HINDSIGHT_API_FILE_STORAGE_S3_BUCKET"
	EnvFileStorageS3Region          = "HINDSIGHT_API_FILE_STORAGE_S3_REGION"
	EnvFileStorageS3Endpoint        = "HINDSIGHT_API_FILE_STORAGE_S3_ENDPOINT"
	EnvFileStorageS3AccessKeyID     = "HINDSIGHT_API_FILE_STORAGE_S3_ACCESS_KEY_ID"
	EnvFileStorageS3SecretAccessKey = "HINDSIGHT_API_FILE_STORAGE_S3_SECRET_ACCESS_KEY"
	// EnvRecallStrategyBoosts prioritises one or more recall retrieval arms
	// over the others. The value is a comma-separated list of arm:level pairs
	// where level is low, medium or high.
	EnvRecallStrategyBoosts = "HINDSIGHT_API_RECALL_STRATEGY_BOOSTS"

	// EnvReflectSearchObservationsMaxTokens is the process-level default for
	// the per-bank reflection observation-search token budget.
	EnvReflectSearchObservationsMaxTokens = "HINDSIGHT_API_REFLECT_SEARCH_OBSERVATIONS_MAX_TOKENS"
	// EnvReflectSearchObservationsIncludeEntities is the process-level default
	// for the per-bank reflection observation entity-attachment flag.
	EnvReflectSearchObservationsIncludeEntities = "HINDSIGHT_API_REFLECT_SEARCH_OBSERVATIONS_INCLUDE_ENTITIES"

	// EnvNullDropFields is the internal tuning switch for dropping explicit
	// null object fields from responses. It is intentionally not part of the
	// approved public environment-variable allowlist; see the implementation
	// report assumption A-null-drop.
	EnvNullDropFields = "HINDSIGHT_API_DROP_NULL_FIELDS"
)

// Default values used when neither a flag nor an environment variable supplies
// a value.
const (
	DefaultHost = "0.0.0.0"
	// DefaultPort is the reference's API port.
	DefaultPort            = 8888
	DefaultDatabaseSchema  = "public"
	DefaultVectorExtension = "pgvector"
	// DefaultDBPoolMaxSize bounds simultaneous database connections, matching
	// the reference's default. An unbounded pool grows until the database
	// refuses connections, turning a load spike into an outage for every other
	// client of that database.
	DefaultDBPoolMaxSize = 100
	// DefaultDBPoolMinSize is the pool size the reference keeps open.
	DefaultDBPoolMinSize       = 5
	DefaultTextSearchExtension = "native"
	DefaultGzipMinSize         = 1024
	DefaultReadTimeoutSeconds  = 30
	DefaultRetryAfterSeconds   = 5
	DefaultLogLevel            = "info"
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
)

// Config is the resolved, typed process configuration. It is built once at
// startup from defaults, environment values, and command-line flags.
type Config struct {
	Host                   string
	Port                   int
	DatabaseURL            string
	DatabaseSchema         string
	VectorExtension        string
	TextSearchExtension    string
	RunMigrationsOnStartup bool
	MCPEnabled             bool
	EnableBankConfigAPI    bool
	// EnableBankLLMHealth gates the per-bank /health/llm route.
	EnableBankLLMHealth bool
	// EnableFileUploadAPI gates the file retain route.
	EnableFileUploadAPI bool
	// EnableDocumentExportAPI gates the document export routes.
	EnableDocumentExportAPI bool
	// EnableDocumentImportAPI gates the document import routes.
	EnableDocumentImportAPI bool
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
	NullDropFields      bool
	// Admission bounds the concurrency of the expensive operations.
	AdmissionMaxWait      time.Duration
	AdmissionRecallLimit  int
	AdmissionRetainLimit  int
	AdmissionReflectLimit int
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
	LoopLagReport      time.Duration
	LoopLagStall       time.Duration
	RetainBatchEnabled bool
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

	// Unsupported collects the reference environment keys that were set but
	// that this build cannot act on. A non-empty set refuses startup, so a
	// copied reference .env is never silently half-honoured.
	Unsupported *Unsupported
}

// FeatureMap returns the version endpoint's feature map. Keys gate client
// behavior and reflect effective process configuration.
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
// HINDSIGHT_API_DATABASE_URL to a postgresql:// URL, or opts into the
// non-persistent backend by naming it explicitly as "memory".
func (c Config) ResolveDatabaseTarget() (url string, inMemory bool, err error) {
	trimmed := strings.TrimSpace(c.DatabaseURL)
	if trimmed == "" {
		return "", false, errors.New(
			"HINDSIGHT_API_DATABASE_URL is not set: set it to a postgresql:// URL " +
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
		Host:                DefaultHost,
		Port:                DefaultPort,
		DatabaseSchema:      DefaultDatabaseSchema,
		VectorExtension:     DefaultVectorExtension,
		TextSearchExtension: DefaultTextSearchExtension,
		GzipMinSize:         DefaultGzipMinSize,
		LogLevel:            DefaultLogLevel,
		Workers:             DefaultWorkers,
		ReadTimeout:         DefaultReadTimeoutSeconds * time.Second,
		RetryAfter:          DefaultRetryAfterSeconds * time.Second,
		BindGrace:           DefaultMigrationGraceWindow,
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
	cfg.RunMigrationsOnStartup = boolFromEnv(lookup, EnvRunMigrationsOnStartup, DefaultRunMigrationsOnStartup)
	// The defaults are the reference's own defaults, so a deployment that sets
	// only the values it cares about sees the same surface either way.
	cfg.MCPEnabled = boolFromEnv(lookup, EnvMCPEnabled, true)
	cfg.EnableBankConfigAPI = boolFromEnv(lookup, EnvEnableBankConfigAPI, true)
	cfg.EnableBankLLMHealth = boolFromEnv(lookup, EnvEnableBankLLMHealth, false)
	cfg.EnableFileUploadAPI = boolFromEnv(lookup, EnvEnableFileUploadAPI, true)
	cfg.EnableObservations = boolFromEnv(lookup, EnvEnableObservations, true)
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
	cfg.BasePath = normalizeBasePath(stringFromEnv(lookup, EnvBasePath))
	cfg.LoopLagReport = secondsFromEnv(lookup, EnvLoopLagReportSeconds, DefaultLoopLagReportS*time.Second)
	cfg.LoopLagStall = secondsFromEnv(lookup, EnvLoopLagStallSeconds, DefaultLoopLagStallS*time.Second)
	cfg.AdmissionMaxWait = secondsFromEnv(lookup, EnvAdmissionMaxWaitSeconds, DefaultAdmissionMaxWaitS*time.Second)
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
	cfg.EnableDocumentImportAPI = boolFromEnv(lookup, EnvEnableDocumentImportAPI, true)
	cfg.Worker = resolveWorker(lookup)
	cfg.Tracing = resolveTracing(lookup)
	cfg.VectorIndex = resolveVectorIndex(lookup)
	cfg.Recency = resolveRecency(lookup)
	cfg.RetainAttachmentMaxSizeMB, cfg.RetainAttachmentMaxCount = resolveUploadLimits(lookup)
	cfg.DBPoolMaxSize = intFromEnv(lookup, EnvDBPoolMaxSize, DefaultDBPoolMaxSize)
	cfg.DBPoolMinSize = intFromEnv(lookup, EnvDBPoolMinSize, DefaultDBPoolMinSize)
	cfg.BankInfoCacheTTL = cacheTTLFromEnv(lookup, EnvBankInfoCacheTTLSeconds, DefaultBankInfoCacheTTLSeconds*time.Second)
	cfg.BankStatsCacheTTL = cacheTTLFromEnv(lookup, EnvBankStatsCacheTTLSeconds, DefaultBankStatsCacheTTLSeconds*time.Second)
	cfg.WorkerEnabled = boolFromEnv(lookup, EnvWorkerEnabled, DefaultWorkerEnabled)
	cfg.Maintenance = resolveMaintenance(lookup)
	cfg.WebhookAllowInsecure = boolFromEnv(lookup, EnvWebhookAllowInsecure, false)
	cfg.WebhookAllowPrivateHosts = boolFromEnv(lookup, EnvWebhookAllowPrivateHosts, false)
	cfg.WebhookAllowedHosts = hostListFromEnv(lookup, EnvWebhookAllowedHosts)
	cfg.NullDropFields = boolFromEnv(lookup, EnvNullDropFields, true)
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
	cfg.RetainLLM = ResolveLLMScope(lookup, "HINDSIGHT_API_RETAIN_LLM")
	cfg.ReflectLLM = ResolveLLMScope(lookup, "HINDSIGHT_API_REFLECT_LLM")
	cfg.MentalModelRefreshLLM = ResolveLLMScope(lookup, "HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM")
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
// selected by HINDSIGHT_API_TENANT_EXTENSION from the package that ships it.
// Selecting it requires HINDSIGHT_API_TENANT_API_KEY.
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

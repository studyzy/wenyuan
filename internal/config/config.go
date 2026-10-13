package config

import (
	"fmt"
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
	// EnvWorkerMaxRetries is the attempt budget beyond the first try.
	EnvWorkerMaxRetries = "HINDSIGHT_API_WORKER_MAX_RETRIES"
	// EnvWorkerConcurrency is how many operations one consumer processes at once.
	EnvWorkerConcurrency = "HINDSIGHT_API_WORKER_CONCURRENCY"
	// EnvWorkerLeaseSeconds bounds how long a claim stays valid before another
	// consumer may reclaim the work.
	EnvWorkerLeaseSeconds = "HINDSIGHT_API_WORKER_LEASE_SECONDS"
	// EnvWorkerPollSeconds is how long to wait between empty claims.
	EnvWorkerPollSeconds = "HINDSIGHT_API_WORKER_POLL_SECONDS"
	// EnvWorkerInitialBackoffSeconds is the first retry delay.
	EnvWorkerInitialBackoffSeconds = "HINDSIGHT_API_WORKER_INITIAL_BACKOFF_SECONDS"
	// EnvWorkerMaxBackoffSeconds is the retry backoff ceiling.
	EnvWorkerMaxBackoffSeconds = "HINDSIGHT_API_WORKER_MAX_BACKOFF_SECONDS"
	EnvGzipMinSize             = "HINDSIGHT_API_GZIP_MIN_SIZE"
	EnvRetainBatchEnabled      = "HINDSIGHT_API_RETAIN_BATCH_ENABLED"
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
	DefaultHost                 = "127.0.0.1"
	DefaultPort                 = 8000
	DefaultDatabaseSchema       = "public"
	DefaultVectorExtension      = "pgvector"
	DefaultTextSearchExtension  = "postgres"
	DefaultGzipMinSize          = 1024
	DefaultReadTimeoutSeconds   = 30
	DefaultRetryAfterSeconds    = 5
	DefaultLogLevel             = "info"
	DefaultWorkers              = 1
	DefaultMigrationGraceWindow = 5 * time.Second
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
	// EnableDocumentExportAPI gates the document export routes.
	EnableDocumentExportAPI bool
	// EnableDocumentImportAPI gates the document import routes.
	EnableDocumentImportAPI bool
	// Worker tunes the background queue consumer.
	Worker WorkerConfig
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
	RetainBatchEnabled  bool
	// RecallStrategyBoosts maps a retrieval arm identifier to a boost level
	// (low, medium, high). Empty leaves every arm unboosted.
	RecallStrategyBoosts map[string]string
	// ReflectSearchObservationsMaxTokens is the shipped per-bank reflection
	// default for the observation-search token budget; nil means unset.
	ReflectSearchObservationsMaxTokens *int
	// ReflectSearchObservationsIncludeEntities is the shipped per-bank
	// reflection default for entity attachment on observation evidence.
	ReflectSearchObservationsIncludeEntities *bool

	// Provider is the resolved model-provider configuration, resolved inside
	// the same environment source as every other key.
	Provider ProviderConfig
	// ProviderScopes is the resolved strict-schema and temperature family.
	ProviderScopes ScopedConfig
	// Consolidation is the resolved second provider scope.
	Consolidation ConsolidationScope

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
}

// FeatureMap returns the version endpoint's feature map. Keys gate client
// behavior and reflect effective process configuration.
func (c Config) FeatureMap() map[string]bool {
	return map[string]bool{
		"bank_config_api":     c.EnableBankConfigAPI,
		"document_export_api": c.EnableDocumentExportAPI,
		"document_import_api": c.EnableDocumentImportAPI,
		"mcp":                 c.MCPEnabled,
	}
}

// Address returns the resolved listen address in host:port form.
func (c Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// FromEnv resolves configuration from environment values and built-in
// defaults, before command-line flag parsing has a chance to override them.
func FromEnv(lookup func(string) (string, bool)) Config {
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
	cfg.RunMigrationsOnStartup = boolFromEnv(lookup, EnvRunMigrationsOnStartup, false)
	cfg.MCPEnabled = boolFromEnv(lookup, EnvMCPEnabled, false)
	cfg.EnableBankConfigAPI = boolFromEnv(lookup, EnvEnableBankConfigAPI, false)
	cfg.EnableDocumentExportAPI = boolFromEnv(lookup, EnvEnableDocumentExportAPI, false)
	cfg.EnableDocumentImportAPI = boolFromEnv(lookup, EnvEnableDocumentImportAPI, false)
	cfg.Worker = resolveWorker(lookup)
	cfg.WebhookAllowInsecure = boolFromEnv(lookup, EnvWebhookAllowInsecure, false)
	cfg.WebhookAllowPrivateHosts = boolFromEnv(lookup, EnvWebhookAllowPrivateHosts, false)
	cfg.WebhookAllowedHosts = hostListFromEnv(lookup, EnvWebhookAllowedHosts)
	cfg.NullDropFields = boolFromEnv(lookup, EnvNullDropFields, false)
	cfg.RetainBatchEnabled = boolFromEnv(lookup, EnvRetainBatchEnabled, false)
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
	return cfg
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

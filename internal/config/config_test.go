package config

import (
	"testing"
	"time"
)

func envMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestFromEnvDefaults(t *testing.T) {
	cfg := FromEnv(envMap(nil))
	if cfg.Host != DefaultHost {
		t.Fatalf("host default = %q, want %q", cfg.Host, DefaultHost)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("port default = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.DatabaseSchema != DefaultDatabaseSchema {
		t.Fatalf("schema default = %q", cfg.DatabaseSchema)
	}
	if cfg.VectorExtension != DefaultVectorExtension {
		t.Fatalf("vector extension default = %q", cfg.VectorExtension)
	}
	if cfg.TextSearchExtension != DefaultTextSearchExtension {
		t.Fatalf("text search extension default = %q", cfg.TextSearchExtension)
	}
	if cfg.TextSearchExtensionNativeLanguage != DefaultTextSearchExtensionNativeLanguage {
		t.Fatalf("native text-search dictionary default = %q", cfg.TextSearchExtensionNativeLanguage)
	}
	if cfg.GzipMinSize != DefaultGzipMinSize {
		t.Fatalf("gzip floor default = %d", cfg.GzipMinSize)
	}
	// The defaults are the reference's own, so a deployment that sets only the
	// values it cares about sees the same surface either way.
	// RunMigrationsOnStartup defaults on, as it does in the reference; the
	// other two are opt-in.
	if !cfg.RunMigrationsOnStartup || cfg.PprofEnabled || cfg.AuditLogEnabled {
		t.Fatalf("boolean env defaults must match the reference: %+v", cfg)
	}
	if !cfg.MCPEnabled || !cfg.EnableBankConfigAPI || !cfg.EnableObservations || !cfg.StoreDocumentText {
		t.Fatalf("boolean env defaults must match the reference: %+v", cfg)
	}
}

func TestFromEnvReadsEveryApprovedVariable(t *testing.T) {
	values := map[string]string{
		EnvHost:                   "0.0.0.0",
		EnvPort:                   "9001",
		EnvDatabaseURL:            "postgres://user:pw@localhost:5432/db",
		EnvDatabaseSchema:         "tenant_a",
		EnvVectorExtension:        "pgvector",
		EnvTextSearchExtension:    "postgres",
		EnvRunMigrationsOnStartup: "true",
		EnvMCPEnabled:             "1",
		EnvEnableBankConfigAPI:    "yes",
		EnvGzipMinSize:            "-1",
	}
	cfg := FromEnv(envMap(values))
	if cfg.Host != "0.0.0.0" || cfg.Port != 9001 {
		t.Fatalf("host/port = %s/%d", cfg.Host, cfg.Port)
	}
	if cfg.DatabaseURL != values[EnvDatabaseURL] {
		t.Fatalf("database url = %q", cfg.DatabaseURL)
	}
	if cfg.DatabaseSchema != "tenant_a" {
		t.Fatalf("schema = %q", cfg.DatabaseSchema)
	}
	if !cfg.RunMigrationsOnStartup || !cfg.MCPEnabled || !cfg.EnableBankConfigAPI {
		t.Fatalf("boolean env parsing failed: %+v", cfg)
	}
	if cfg.GzipMinSize != -1 {
		t.Fatalf("negative gzip floor must be preserved, got %d", cfg.GzipMinSize)
	}
}

func TestFromEnvReadsTheNativeTextSearchDictionary(t *testing.T) {
	// The reference accepts any PostgreSQL identifier here (it lowercases the
	// value first), so a deployment that switches to a CJK dictionary or to
	// `simple` must resolve rather than be refused: the dictionary is embedded
	// in the stored search_vector and in every keyword query, so a silent
	// fallback to english would leave such a deployment searching with a
	// tokenizer its rows were not written with.
	cfg := FromEnv(envMap(map[string]string{EnvTextSearchExtensionNativeLanguage: "  ZHParser "}))
	if cfg.TextSearchExtensionNativeLanguage != "zhparser" {
		t.Fatalf("dictionary = %q, want the lowercased value", cfg.TextSearchExtensionNativeLanguage)
	}
	if err := cfg.ValidateSupported(); err != nil {
		t.Fatalf("a configured dictionary must be honoured, not refused: %v", err)
	}
}

func TestFromEnvIgnoresUnparseableValues(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{EnvPort: "not-a-number", EnvGzipMinSize: "abc"}))
	if cfg.Port != DefaultPort {
		t.Fatalf("port = %d, want default", cfg.Port)
	}
	if cfg.GzipMinSize != DefaultGzipMinSize {
		t.Fatalf("gzip floor = %d, want default", cfg.GzipMinSize)
	}
}

func TestAddressAndFeatureMap(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvHost: "127.0.0.1", EnvPort: "8080",
		EnvMCPEnabled: "false", EnvEnableBankConfigAPI: "false", EnvAuditLogEnabled: "true",
	}))
	if got := cfg.Address(); got != "127.0.0.1:8080" {
		t.Fatalf("address = %q", got)
	}
	features := cfg.FeatureMap()
	if features["mcp"] {
		t.Fatalf("feature map must reflect mcp enablement: %v", features)
	}
	if features["bank_config_api"] {
		t.Fatalf("feature map must reflect the bank config gate: %v", features)
	}
	if !features["audit_log"] {
		t.Fatalf("feature map must reflect the audit log gate: %v", features)
	}
	// Every flag the reference reports is present.
	for _, name := range []string{"observations", "mcp", "worker", "bank_config_api", "bank_llm_health",
		"file_upload_api", "document_export_api", "document_import_api", "audit_log", "llm_trace",
		"store_document_text"} {
		if _, ok := features[name]; !ok {
			t.Fatalf("the feature map must carry %q: %v", name, features)
		}
	}
}

// ts-0015 / cmp-0008: the retain batch gate is read from its environment
// variable and defaults to false.
func TestRetainBatchEnabledFromEnv(t *testing.T) {
	if cfg := FromEnv(envMap(nil)); cfg.RetainBatchEnabled {
		t.Fatalf("retain batch gate must default to false")
	}
	cfg := FromEnv(envMap(map[string]string{EnvRetainBatchEnabled: "true"}))
	if !cfg.RetainBatchEnabled {
		t.Fatalf("retain batch gate must reflect %s", EnvRetainBatchEnabled)
	}
}

// beh-0026: the two per-bank reflection defaults resolve from the environment.
func TestReflectDefaultsFromEnv(t *testing.T) {
	def := FromEnv(envMap(nil))
	if def.ReflectSearchObservationsMaxTokens != nil || def.ReflectSearchObservationsIncludeEntities != nil {
		t.Fatalf("reflection defaults must be unset when the environment omits them")
	}
	cfg := FromEnv(envMap(map[string]string{
		EnvReflectSearchObservationsMaxTokens:       "256",
		EnvReflectSearchObservationsIncludeEntities: "true",
	}))
	if cfg.ReflectSearchObservationsMaxTokens == nil || *cfg.ReflectSearchObservationsMaxTokens != 256 {
		t.Fatalf("observation token default not read: %+v", cfg.ReflectSearchObservationsMaxTokens)
	}
	if cfg.ReflectSearchObservationsIncludeEntities == nil || !*cfg.ReflectSearchObservationsIncludeEntities {
		t.Fatalf("entity-attachment default not read: %+v", cfg.ReflectSearchObservationsIncludeEntities)
	}
}

// The attachment backend defaults to the database, and selecting the object
// store reads its endpoint settings.
func TestResolveFileStorage(t *testing.T) {
	base := map[string]string{}
	lookup := func(key string) (string, bool) {
		value, ok := base[key]
		return value, ok
	}
	if got := resolveFileStorage(lookup); got.Type != FileStoragePostgres {
		t.Fatalf("default type = %q, want the database", got.Type)
	}
	if resolveFileStorage(lookup).Enabled() {
		t.Fatal("the database backend must not report an object store")
	}

	base[EnvFileStorageType] = FileStorageS3
	base[EnvFileStorageS3Bucket] = "bank-data"
	base[EnvFileStorageS3Endpoint] = "https://minio.internal"
	base[EnvFileStorageS3AccessKeyID] = "key"
	base[EnvFileStorageS3SecretAccessKey] = "secret"
	got := resolveFileStorage(lookup)
	if !got.Enabled() {
		t.Fatal("the s3 backend must report an object store")
	}
	if got.S3.Bucket != "bank-data" || got.S3.Endpoint != "https://minio.internal" {
		t.Fatalf("s3 settings = %+v", got.S3)
	}
	if !got.S3.BucketConfigured() {
		t.Fatal("a named bucket must report as configured")
	}
}

// An unrecognised backend name falls back to the database rather than failing
// startup, so a typo degrades a deployment instead of blocking it.
func TestResolveFileStorageUnknownType(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key == EnvFileStorageType {
			return "nonsense", true
		}
		return "", false
	}
	got := resolveFileStorage(lookup)
	if got.Type != FileStoragePostgres {
		t.Fatalf("type = %q, want the database fallback", got.Type)
	}
}

// The version endpoint's feature map reports all eleven reference keys, so a
// client that gates on one of them sees it.
func TestFeatureMapHasReferenceKeys(t *testing.T) {
	cfg := FromEnv(func(string) (string, bool) { return "", false })
	features := cfg.FeatureMap()
	want := []string{
		"observations", "mcp", "worker", "bank_config_api", "bank_llm_health",
		"file_upload_api", "document_export_api", "document_import_api",
		"audit_log", "llm_trace", "store_document_text",
	}
	for _, key := range want {
		if _, ok := features[key]; !ok {
			t.Fatalf("feature map is missing %q: %v", key, features)
		}
	}
	if len(features) != len(want) {
		t.Fatalf("feature map has %d keys, want %d: %v", len(features), len(want), features)
	}
}

// The reference's worker key names are accepted as aliases.
func TestWorkerReferenceKeyAliases(t *testing.T) {
	environ := func(key string) (string, bool) {
		switch key {
		case "WENYUAN_API_WORKER_POLL_INTERVAL_MS":
			return "250", true
		case "WENYUAN_API_WORKER_MAX_SLOTS":
			return "12", true
		case "WENYUAN_API_WORKER_TASK_RETRY_BACKOFF_SECONDS":
			return "7", true
		}
		return "", false
	}
	cfg := FromEnv(environ)
	if cfg.Worker.PollInterval.Milliseconds() != 250 {
		t.Fatalf("poll interval = %v, want 250ms", cfg.Worker.PollInterval)
	}
	if cfg.Worker.Concurrency != 12 {
		t.Fatalf("concurrency = %d, want 12", cfg.Worker.Concurrency)
	}
	if cfg.Worker.InitialBackoff.Seconds() != 7 {
		t.Fatalf("initial backoff = %v, want 7s", cfg.Worker.InitialBackoff)
	}
}

// The reference reserves a floor of the consumer's concurrency per operation
// type, one key per type. Each key must land on the type it names, or the
// reservation would protect the wrong work.
func TestPerTypeReservedSlotsResolveToTheirOperationType(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvWorkerRetainReservedSlots:             "3",
		EnvWorkerGraphMaintenanceReservedSlots:   "1",
		EnvWorkerVectorIndexMaintReservedSlots:   "2",
		EnvWorkerImportBankReservedSlots:         "5",
		EnvWorkerRefreshMentalModelReservedSlots: "4",
		EnvWorkerFileConvertRetainReservedSlots:  "6",
		EnvWorkerImportDocumentsReservedSlots:    "7",
		EnvWorkerExportDocumentsReservedSlots:    "8",
		EnvWorkerExportBankReservedSlots:         "9",
		EnvWorkerCloneBankReservedSlots:          "10",
	}))
	want := map[string]int{
		"consolidation": 2, "retain": 3, "file_convert_retain": 6,
		"refresh_mental_model": 4, "graph_maintenance": 1, "vector_index_maintenance": 2,
		"import_documents": 7, "export_documents": 8, "import_bank": 5,
		"export_bank": 9, "clone_bank": 10,
	}
	if len(cfg.Worker.ReservedSlots) != len(want) {
		t.Fatalf("reserved slots = %v, want %v", cfg.Worker.ReservedSlots, want)
	}
	for opType, slots := range want {
		if cfg.Worker.ReservedSlots[opType] != slots {
			t.Errorf("%s reserved = %d, want %d", opType, cfg.Worker.ReservedSlots[opType], slots)
		}
	}
}

// A type the deployment did not reserve has no entry, so the consumer's
// preference list is exactly what the operator asked for.
func TestUnsetReservedSlotsStayAbsent(t *testing.T) {
	cfg := FromEnv(envMap(nil))
	if _, ok := cfg.Worker.ReservedSlots["retain"]; ok {
		t.Fatalf("an unset reservation produced an entry: %v", cfg.Worker.ReservedSlots)
	}
	if cfg.Worker.ReservedSlots["consolidation"] != 2 {
		t.Fatalf("consolidation floor = %d, want the reference default 2", cfg.Worker.ReservedSlots["consolidation"])
	}
}

// The reference's watchdog defaults on and its poll interval is the probe's
// cadence; the lag reporter's own (second-valued) keys override it when set.
func TestLoopWatchdogKeysDriveTheProbe(t *testing.T) {
	base := FromEnv(envMap(nil))
	if !base.MetricsTuning.LoopWatchdog {
		t.Fatal("the watchdog is off by default, but the reference starts it")
	}
	if base.LoopWatchdogInterval != DefaultLoopWatchdogPoll {
		t.Fatalf("probe interval = %s, want the watchdog poll %s", base.LoopWatchdogInterval, DefaultLoopWatchdogPoll)
	}
	if base.LoopWatchdogStall != DefaultLoopWatchdogStall {
		t.Fatalf("stall threshold = %s, want %s", base.LoopWatchdogStall, DefaultLoopWatchdogStall)
	}

	tuned := FromEnv(envMap(map[string]string{
		EnvLoopWatchdogPollIntervalMS:   "500",
		EnvLoopWatchdogStallThresholdMS: "2000",
	}))
	if tuned.LoopWatchdogInterval != 500*time.Millisecond || tuned.LoopWatchdogStall != 2*time.Second {
		t.Fatalf("watchdog tuning ignored: interval=%s stall=%s", tuned.LoopWatchdogInterval, tuned.LoopWatchdogStall)
	}

	// The reporter's key keeps its own meaning: setting it moves the cadence.
	reporter := FromEnv(envMap(map[string]string{EnvLoopLagReportSeconds: "30"}))
	if reporter.LoopWatchdogInterval != 30*time.Second {
		t.Fatalf("lag reporter interval ignored: %s", reporter.LoopWatchdogInterval)
	}
	// Disabling the watchdog stops the probe even with an interval configured.
	off := FromEnv(envMap(map[string]string{EnvLoopWatchdogEnabled: "false", EnvLoopLagReportSeconds: "30"}))
	if off.MetricsTuning.LoopWatchdog {
		t.Fatal("WENYUAN_API_LOOP_WATCHDOG_ENABLED=false did not disable the probe")
	}
}

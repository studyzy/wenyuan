package config

import "testing"

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
	if cfg.GzipMinSize != DefaultGzipMinSize {
		t.Fatalf("gzip floor default = %d", cfg.GzipMinSize)
	}
	if cfg.RunMigrationsOnStartup || cfg.MCPEnabled || cfg.EnableBankConfigAPI {
		t.Fatalf("boolean env defaults must be false")
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
	cfg := FromEnv(envMap(map[string]string{EnvHost: "127.0.0.1", EnvPort: "8080", EnvMCPEnabled: "true"}))
	if got := cfg.Address(); got != "127.0.0.1:8080" {
		t.Fatalf("address = %q", got)
	}
	features := cfg.FeatureMap()
	if !features["mcp"] {
		t.Fatalf("feature map must reflect mcp enablement: %v", features)
	}
	if features["bank_config_api"] {
		t.Fatalf("feature map must reflect bank config gate: %v", features)
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
			return "gcs", true
		}
		return "", false
	}
	got := resolveFileStorage(lookup)
	if got.Type != FileStoragePostgres {
		t.Fatalf("type = %q, want the database fallback", got.Type)
	}
}

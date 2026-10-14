package config

import "strings"

// File-storage backend names accepted by HINDSIGHT_API_FILE_STORAGE_TYPE.
const (
	// FileStoragePostgres keeps attachment payloads in the database, which is
	// the default and needs no object store.
	FileStoragePostgres = "postgresql"
	// FileStorageS3 uses an S3-compatible object store, which also serves
	// MinIO, Cloudflare R2, Tigris and any other S3-compliant endpoint.
	FileStorageS3 = "s3"
)

// FileStorageConfig is the resolved attachment-storage backend.
type FileStorageConfig struct {
	// Type selects the backend. Empty means the database.
	Type string
	// S3 holds the object-store settings, populated only when Type is s3.
	S3 S3Config
}

// S3Config is the S3-compatible endpoint configuration.
type S3Config struct {
	// Bucket is the object-store bucket every payload is written under.
	Bucket string
	// Region is the signing region; empty lets the endpoint decide.
	Region string
	// Endpoint overrides the vendor host, which is what points the backend at
	// a self-hosted S3-compatible service.
	Endpoint string
	// AccessKeyID and SecretAccessKey are the static credentials. Empty means
	// the environment's own credential chain is used.
	AccessKeyID     string
	SecretAccessKey string
}

// Enabled reports whether an object-store backend is configured, so a caller
// can tell "store in the database" from "store in an object store".
func (c FileStorageConfig) Enabled() bool {
	return strings.EqualFold(strings.TrimSpace(c.Type), FileStorageS3)
}

// BucketConfigured reports whether the object-store settings name a bucket,
// which every S3 request needs.
func (c S3Config) BucketConfigured() bool { return strings.TrimSpace(c.Bucket) != "" }

// resolveFileStorage reads the attachment-storage configuration. An
// unrecognised type falls back to the database rather than failing startup, so
// a typo degrades a deployment instead of blocking it.
func resolveFileStorage(lookup func(string) (string, bool)) FileStorageConfig {
	cfg := FileStorageConfig{Type: FileStoragePostgres}
	if v, ok := lookup(EnvFileStorageType); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case FileStorageS3:
			cfg.Type = FileStorageS3
		case "", FileStoragePostgres:
			cfg.Type = FileStoragePostgres
		default:
			cfg.Type = FileStoragePostgres
		}
	}
	cfg.S3 = S3Config{
		Bucket:          stringFromEnv(lookup, EnvFileStorageS3Bucket),
		Region:          stringFromEnv(lookup, EnvFileStorageS3Region),
		Endpoint:        stringFromEnv(lookup, EnvFileStorageS3Endpoint),
		AccessKeyID:     stringFromEnv(lookup, EnvFileStorageS3AccessKeyID),
		SecretAccessKey: stringFromEnv(lookup, EnvFileStorageS3SecretAccessKey),
	}
	return cfg
}

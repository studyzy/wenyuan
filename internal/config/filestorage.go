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
	// FileStorageGCS uses a Google Cloud Storage bucket.
	FileStorageGCS = "gcs"
	// FileStorageAzure uses an Azure Blob Storage container.
	FileStorageAzure = "azure"
)

// FileStorageConfig is the resolved attachment-storage backend.
type FileStorageConfig struct {
	// Type selects the backend. Empty means the database.
	Type string
	// S3 holds the object-store settings, populated only when Type is s3.
	S3 S3Config
	// GCS holds the Google Cloud Storage settings, populated only when Type is gcs.
	GCS GCSConfig
	// Azure holds the Azure Blob Storage settings, populated only when Type is azure.
	Azure AzureConfig
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

// GCSConfig is the Google Cloud Storage configuration.
type GCSConfig struct {
	// Bucket is the GCS bucket every payload is written under.
	Bucket string
	// ServiceAccountKey is the JSON service-account credential. Empty means
	// the environment's Application Default Credentials are used.
	ServiceAccountKey string
	// Endpoint overrides the API host, which points the backend at an emulator
	// or a local stub. It is not an environment key.
	Endpoint string
}

// AzureConfig is the Azure Blob Storage configuration.
type AzureConfig struct {
	// Container is the blob container every payload is written under.
	Container string
	// AccountName and AccountKey are the static storage-account credentials.
	AccountName string
	AccountKey  string
	// Endpoint overrides the account host, which points the backend at an
	// emulator or a local stub. It is not an environment key.
	Endpoint string
}

// Enabled reports whether an object-store backend is configured, so a caller
// can tell "store in the database" from "store in an object store".
func (c FileStorageConfig) Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case FileStorageS3, FileStorageGCS, FileStorageAzure:
		return true
	default:
		return false
	}
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
		case FileStorageGCS:
			cfg.Type = FileStorageGCS
		case FileStorageAzure:
			cfg.Type = FileStorageAzure
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
	cfg.GCS = GCSConfig{
		Bucket:            stringFromEnv(lookup, EnvFileStorageGCSBucket),
		ServiceAccountKey: stringFromEnv(lookup, EnvFileStorageGCSServiceAccountKey),
	}
	cfg.Azure = AzureConfig{
		Container:   stringFromEnv(lookup, EnvFileStorageAzureContainer),
		AccountName: stringFromEnv(lookup, EnvFileStorageAzureAccountName),
		AccountKey:  stringFromEnv(lookup, EnvFileStorageAzureAccountKey),
	}
	return cfg
}

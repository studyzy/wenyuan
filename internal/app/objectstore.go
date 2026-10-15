package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/objectstore"
	"github.com/studyzy/wenyuan/internal/storage"
)

// objectStoreAdapter bridges the object-store package onto the storage area's
// payload boundary, translating the miss sentinel so the caller sees the
// storage area's own error either way.
type objectStoreAdapter struct {
	backend objectstore.Backend
}

// Put stores the payload under the key.
func (a objectStoreAdapter) Put(ctx context.Context, key string, data []byte, contentType string) error {
	return a.backend.Put(ctx, key, data, contentType)
}

// Get returns the payload, translating a miss into the storage area's
// sentinel.
func (a objectStoreAdapter) Get(ctx context.Context, key string) ([]byte, string, error) {
	data, contentType, err := a.backend.Get(ctx, key)
	if err != nil {
		if err == objectstore.ErrNotFound {
			return nil, "", storage.ErrObjectNotFound
		}
		return nil, "", err
	}
	return data, contentType, nil
}

// OpenStream hands the object body straight through, translating a miss into
// the storage area's sentinel. It exposes the object store's streaming
// capability so a stored-file read can write the first chunk without the whole
// object resident; without it the storage area falls back to the buffered Get.
func (a objectStoreAdapter) OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	reader, contentType, size, err := a.backend.OpenStream(ctx, key)
	if err != nil {
		if err == objectstore.ErrNotFound {
			return nil, "", -1, storage.ErrObjectNotFound
		}
		return nil, "", -1, err
	}
	return reader, contentType, size, nil
}

// DeletePrefix removes every object under the prefix, which is what a bank
// teardown uses to reclaim the bank's payloads from the object store.
func (a objectStoreAdapter) DeletePrefix(ctx context.Context, prefix string) error {
	return a.backend.DeletePrefix(ctx, prefix)
}

// GetDownloadURL hands out a time-limited direct URL for an object. The empty
// string means the backend cannot sign one, so the caller keeps proxying the
// payload.
func (a objectStoreAdapter) GetDownloadURL(ctx context.Context, key string, expiresIn time.Duration) (string, error) {
	return a.backend.GetDownloadURL(ctx, key, expiresIn)
}

// newObjectStore builds the payload backend the resolved file-storage settings
// select. A backend whose required setting is missing fails here, naming the
// variable the operator has to set, rather than at the first request.
func newObjectStore(cfg config.FileStorageConfig) (storage.ObjectStore, error) {
	backend, err := newObjectStoreBackend(cfg)
	if err != nil {
		return nil, err
	}
	return objectStoreAdapter{backend: backend}, nil
}

// newObjectStoreBackend constructs the selected object-store backend. The
// selection mirrors the reference factory: only the type names it accepts are
// reachable, because config has already mapped anything else onto the database
// backend.
func newObjectStoreBackend(cfg config.FileStorageConfig) (objectstore.Backend, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case config.FileStorageGCS:
		return objectstore.NewGCS(objectstore.GCSConfig{
			Bucket:            strings.TrimSpace(cfg.GCS.Bucket),
			ServiceAccountKey: strings.TrimSpace(cfg.GCS.ServiceAccountKey),
			Endpoint:          strings.TrimSpace(cfg.GCS.Endpoint),
		}, nil)
	case config.FileStorageAzure:
		return objectstore.NewAzure(objectstore.AzureConfig{
			Container:   strings.TrimSpace(cfg.Azure.Container),
			AccountName: strings.TrimSpace(cfg.Azure.AccountName),
			AccountKey:  cfg.Azure.AccountKey,
			Endpoint:    strings.TrimSpace(cfg.Azure.Endpoint),
		}, nil)
	case config.FileStorageS3:
		return objectstore.New(objectstore.Config{
			Bucket:          strings.TrimSpace(cfg.S3.Bucket),
			Region:          strings.TrimSpace(cfg.S3.Region),
			Endpoint:        strings.TrimSpace(cfg.S3.Endpoint),
			AccessKeyID:     strings.TrimSpace(cfg.S3.AccessKeyID),
			SecretAccessKey: cfg.S3.SecretAccessKey,
			// A self-hosted endpoint generally serves the bucket in the path,
			// because it cannot mint a per-bucket subdomain; AWS keeps the
			// virtual-host default.
			PathStyle: strings.TrimSpace(cfg.S3.Endpoint) != "",
		}, nil), nil
	default:
		return nil, fmt.Errorf("file storage type %q has no object-store backend", cfg.Type)
	}
}

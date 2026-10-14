package app

import (
	"context"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/objectstore"
	"github.com/studyzy/wenyuan/internal/storage"
)

// objectStoreAdapter bridges the object-store package onto the storage area's
// payload boundary, translating the miss sentinel so the caller sees the
// storage area's own error either way.
type objectStoreAdapter struct {
	store *objectstore.Store
}

// Put stores the payload under the key.
func (a objectStoreAdapter) Put(ctx context.Context, key string, data []byte, contentType string) error {
	return a.store.Put(ctx, key, data, contentType)
}

// Get returns the payload, translating a miss into the storage area's
// sentinel.
func (a objectStoreAdapter) Get(ctx context.Context, key string) ([]byte, string, error) {
	data, contentType, err := a.store.Get(ctx, key)
	if err != nil {
		if err == objectstore.ErrNotFound {
			return nil, "", storage.ErrObjectNotFound
		}
		return nil, "", err
	}
	return data, contentType, nil
}

// newObjectStore builds the payload backend from the resolved settings.
func newObjectStore(cfg config.S3Config) storage.ObjectStore {
	return objectStoreAdapter{store: objectstore.New(objectstore.Config{
		Bucket:          strings.TrimSpace(cfg.Bucket),
		Region:          strings.TrimSpace(cfg.Region),
		Endpoint:        strings.TrimSpace(cfg.Endpoint),
		AccessKeyID:     strings.TrimSpace(cfg.AccessKeyID),
		SecretAccessKey: cfg.SecretAccessKey,
		// A self-hosted endpoint generally serves the bucket in the path,
		// because it cannot mint a per-bucket subdomain; AWS keeps the
		// virtual-host default.
		PathStyle: strings.TrimSpace(cfg.Endpoint) != "",
	}, nil)}
}

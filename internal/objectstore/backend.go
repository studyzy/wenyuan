package objectstore

import (
	"context"
	"io"
	"time"
)

// Backend is the operation set every object-store backend provides. It is the
// boundary the composition root builds a payload store from, so a deployment
// can select S3, Google Cloud Storage or Azure Blob Storage without the
// storage area or the HTTP handlers knowing which one is behind it.
//
// The methods mirror the reference file-storage surface: put, get (buffered
// and streamed), delete-prefix, and a time-limited download URL. A miss is
// reported as ErrNotFound by every backend, and a non-success response as
// *ResponseError.
type Backend interface {
	// Put stores the payload under the key, replacing any existing object.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Get returns the payload stored under the key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, string, error)
	// Delete removes one object. A key that is already gone is not an error,
	// so a delete is idempotent.
	Delete(ctx context.Context, key string) error
	// DeletePrefix removes every object whose key begins with prefix.
	DeletePrefix(ctx context.Context, prefix string) error
	// OpenStream returns the object body without buffering it. The size is the
	// reported Content-Length, or -1 when the backend does not declare one.
	OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error)
	// GetDownloadURL returns a URL that grants time-limited read access to the
	// key. It is empty (with a nil error) when the backend cannot sign, which
	// lets a caller keep serving the payload through its own route.
	GetDownloadURL(ctx context.Context, key string, expiresIn time.Duration) (string, error)
}

// clampExpiry bounds a requested signature lifetime to what the signing
// services accept: at least one second, at most seven days (the S3 and GCS
// ceiling). A zero or negative request falls back to one hour, the reference's
// default.
func clampExpiry(expiresIn time.Duration) time.Duration {
	if expiresIn <= 0 {
		return time.Hour
	}
	if expiresIn < time.Second {
		return time.Second
	}
	ceiling := 7 * 24 * time.Hour
	if expiresIn > ceiling {
		return ceiling
	}
	return expiresIn
}

package storage

import (
	"context"
	"time"
)

// StoredFileDownloadURLer is the optional Store capability that yields a
// time-limited URL which serves a stored payload directly. A download route
// type-asserts its Store to this interface: when the assertion succeeds and a
// URL comes back, the bytes are served by whatever signed the URL rather than
// by the route itself; when it fails, or the URL is empty, the route keeps
// proxying the payload, so every backend serves downloads unchanged.
type StoredFileDownloadURLer interface {
	// StoredFileDownloadURL returns a time-limited URL for the payload, the
	// empty string when the backend cannot sign one, or ErrStoredFileNotFound
	// when the key is unknown.
	StoredFileDownloadURL(ctx context.Context, key string, expiresIn time.Duration) (string, error)
}

// objectSigner is the optional object-store capability behind a signed
// download URL. It is declared here, like ObjectStore and objectStreamer, so
// the storage area owns the shape it needs without importing the object-store
// package.
type objectSigner interface {
	// GetDownloadURL returns a time-limited URL for the object, or the empty
	// string when the backend cannot sign one.
	GetDownloadURL(ctx context.Context, key string, expiresIn time.Duration) (string, error)
}

// StoredFileDownloadURL returns a time-limited direct URL for a stored payload
// when the configured object store can sign one.
//
// The row backend returns the empty string rather than its own API path: the
// route that would serve that path is the caller, so a URL pointing back at it
// would be a redirect loop. Only an object store — which serves the bytes
// itself — offers a URL here.
func (s *PostgresStore) StoredFileDownloadURL(ctx context.Context, key string, expiresIn time.Duration) (string, error) {
	if err := ValidateStoredFileKey(key); err != nil {
		return "", ErrStoredFileNotFound
	}
	if s.objectStore == nil {
		return "", nil
	}
	signer, ok := s.objectStore.(objectSigner)
	if !ok {
		return "", nil
	}
	return signer.GetDownloadURL(ctx, key, expiresIn)
}

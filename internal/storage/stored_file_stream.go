package storage

import (
	"context"
	"io"
)

// SizeUnknown is the Size a stream reports when the backend cannot supply the
// payload length before the payload is read. It mirrors an unknown
// Content-Length, so a caller omits the header rather than declaring zero.
const SizeUnknown int64 = -1

// StoredFileStream is a reader over a stored payload, together with the media
// type and, when known, the size. It exists so a caller can write the first
// byte of a response without first loading the whole payload.
type StoredFileStream struct {
	// Reader yields the payload. The caller owns it and must close it.
	Reader io.ReadCloser
	// ContentType is the media type of the payload. Empty lets the caller
	// fall back to the type inferred from the key.
	ContentType string
	// Size is the payload length in bytes, or SizeUnknown when the backend
	// does not report it before the payload is read.
	Size int64
}

// StoredFileStreamer is the optional capability a Store may implement to open
// a stored payload without holding it whole in memory. A caller type-asserts
// its Store to this interface and keeps its buffered path when the assertion
// fails, so a backend that does not implement it keeps working unchanged.
type StoredFileStreamer interface {
	// OpenStoredFile opens the payload by key. It returns
	// ErrStoredFileNotFound when the key is unknown.
	OpenStoredFile(ctx context.Context, key string) (StoredFileStream, error)
}

// objectStreamer is the optional object-store capability a stored-file read
// uses to stream an object rather than buffer it. It is declared here, like
// ObjectStore, so the storage area owns the shape it needs without importing
// the object-store package.
type objectStreamer interface {
	// OpenStream returns the object body, its media type, and its size (or
	// -1 when unknown). The caller owns the reader and must close it. A miss
	// is reported as ErrObjectNotFound.
	OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error)
}

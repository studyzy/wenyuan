package objectstore

import (
	"context"
	"io"
	"net/http"
)

// OpenStream returns the object body without buffering it: the live HTTP
// response body is handed back as the reader, so the first byte reaches the
// caller before the object has been fully transferred. The size is the
// response's Content-Length, or -1 when the endpoint does not report one. The
// caller owns the reader and must close it.
//
// It is the streaming counterpart to Get: Get reads the whole object into
// memory so a caller that needs the bytes whole can have them, while OpenStream
// lets a byte-serving handler write the first chunk without the whole object
// resident.
func (s *Store) OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	resp, err := s.do(ctx, http.MethodGet, key, nil, map[string]string{"x-amz-content-sha256": emptyDigest})
	if err != nil {
		return nil, "", -1, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", -1, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, "", -1, s.statusError(resp)
	}
	// ContentLength is -1 when the length was not declared, which is exactly
	// the "unknown" a caller omits a Content-Length header for.
	return resp.Body, resp.Header.Get("Content-Type"), resp.ContentLength, nil
}

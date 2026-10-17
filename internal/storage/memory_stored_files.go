package storage

import (
	"bytes"
	"context"
	"io"
)

// PutStoredFile persists a payload in the in-memory backend.
func (s *MemoryStore) PutStoredFile(ctx context.Context, file StoredFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateStoredFileKey(file.Key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.storedFiles == nil {
		s.storedFiles = map[string]StoredFile{}
	}
	copied := file
	copied.Data = append([]byte(nil), file.Data...)
	s.storedFiles[file.Key] = copied
	return nil
}

// GetStoredFile returns a payload from the in-memory backend.
func (s *MemoryStore) GetStoredFile(ctx context.Context, key string) (StoredFile, error) {
	if err := ctx.Err(); err != nil {
		return StoredFile{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return StoredFile{}, s.readErr
	}
	file, ok := s.storedFiles[key]
	if !ok {
		return StoredFile{}, ErrStoredFileNotFound
	}
	copied := file
	copied.Data = append([]byte(nil), file.Data...)
	return copied, nil
}

// DeleteStoredFile removes a payload from the in-memory backend. A key that
// names no payload is a no-op, so removing an export operation's archive never
// fails on a blob that is already gone.
func (s *MemoryStore) DeleteStoredFile(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	delete(s.storedFiles, key)
	return nil
}

// OpenStoredFile opens a payload from the in-memory backend for streaming
// read. The bytes already live in memory, so the reader simply wraps them; the
// gain is that the caller stops needing a separate whole-payload handle.
func (s *MemoryStore) OpenStoredFile(ctx context.Context, key string) (StoredFileStream, error) {
	file, err := s.GetStoredFile(ctx, key)
	if err != nil {
		return StoredFileStream{}, err
	}
	return StoredFileStream{
		Reader:      io.NopCloser(bytes.NewReader(file.Data)),
		ContentType: file.ContentType,
		Size:        int64(len(file.Data)),
	}, nil
}

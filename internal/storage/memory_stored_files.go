package storage

import (
	"context"
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

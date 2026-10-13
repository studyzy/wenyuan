package storage

import "context"

// PutStoredFile persists a payload through the filesystem-backed store. The
// PostgreSQL backend delegates payload bytes to the filesystem because the
// existing schema has no blob column and this unit must not change the schema.
func (s *PostgresStore) PutStoredFile(ctx context.Context, file StoredFile) error {
	if s.files == nil {
		return ErrStoredFileNotFound
	}
	return s.files.PutStoredFile(ctx, file)
}

// GetStoredFile reads a payload through the filesystem-backed store.
func (s *PostgresStore) GetStoredFile(ctx context.Context, key string) (StoredFile, error) {
	if s.files == nil {
		return StoredFile{}, ErrStoredFileNotFound
	}
	return s.files.GetStoredFile(ctx, key)
}

package storage

import (
	"context"
	"database/sql"
	"errors"
)

// PutStoredFile persists a payload in the stored-file table.
func (s *PostgresStore) PutStoredFile(ctx context.Context, file StoredFile) error {
	if err := ValidateStoredFileKey(file.Key); err != nil {
		return err
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.file_storage (storage_key, data)
	          VALUES ($1, $2)
	          ON CONFLICT (storage_key) DO UPDATE SET data = $2`
	_, err := s.db.ExecContext(ctx, query, file.Key, file.Data)
	return err
}

// GetStoredFile reads a payload from the stored-file table.
func (s *PostgresStore) GetStoredFile(ctx context.Context, key string) (StoredFile, error) {
	if err := ValidateStoredFileKey(key); err != nil {
		return StoredFile{}, ErrStoredFileNotFound
	}
	query := `SELECT storage_key, data FROM ` + quoteIdent(s.schema) + `.file_storage WHERE storage_key = $1`
	var storedKey string
	var data []byte
	err := s.db.QueryRowContext(ctx, query, key).Scan(&storedKey, &data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StoredFile{}, ErrStoredFileNotFound
		}
		return StoredFile{}, err
	}
	return StoredFile{Key: storedKey, Data: data, ContentType: contentTypeForKey(storedKey)}, nil
}

package storage

import (
	"context"
	"database/sql"
	"errors"
)

// PutStoredFile persists a payload. With an object store configured the bytes
// go there and only the key is recorded locally; otherwise the payload is kept
// in the stored-file table.
func (s *PostgresStore) PutStoredFile(ctx context.Context, file StoredFile) error {
	if err := ValidateStoredFileKey(file.Key); err != nil {
		return err
	}
	if s.objectStore != nil {
		return s.objectStore.Put(ctx, file.Key, file.Data, contentTypeForKey(file.Key))
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.file_storage (storage_key, data)
	          VALUES ($1, $2)
	          ON CONFLICT (storage_key) DO UPDATE SET data = $2`
	_, err := s.db.ExecContext(ctx, query, file.Key, file.Data)
	return err
}

// GetStoredFile reads a payload. An object-store miss is reported through the
// same sentinel as a missing row, so a caller cannot tell which backend held
// the payload — nor needs to.
func (s *PostgresStore) GetStoredFile(ctx context.Context, key string) (StoredFile, error) {
	if err := ValidateStoredFileKey(key); err != nil {
		return StoredFile{}, ErrStoredFileNotFound
	}
	if s.objectStore != nil {
		data, contentType, err := s.objectStore.Get(ctx, key)
		if err != nil {
			if errors.Is(err, ErrObjectNotFound) {
				return StoredFile{}, ErrStoredFileNotFound
			}
			return StoredFile{}, err
		}
		if contentType == "" {
			contentType = contentTypeForKey(key)
		}
		return StoredFile{Key: key, Data: data, ContentType: contentType}, nil
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

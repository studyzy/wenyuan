package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
)

// PutStoredFile persists a payload. With an object store configured the bytes
// go there and only the key is recorded locally; otherwise the payload is kept
// in the stored-file table.
func (s *PostgresStore) PutStoredFile(ctx context.Context, file StoredFile) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if err := ValidateStoredFileKey(file.Key); err != nil {
		return err
	}
	if s.objectStore != nil {
		return s.objectStore.Put(ctx, file.Key, file.Data, contentTypeForKey(file.Key))
	}
	query := `INSERT INTO ` + quoteIdent(schema) + `.file_storage (storage_key, data)
	          VALUES ($1, $2)
	          ON CONFLICT (storage_key) DO UPDATE SET data = $2`
	_, err := s.db.ExecContext(ctx, query, file.Key, file.Data)
	return err
}

// GetStoredFile reads a payload. An object-store miss is reported through the
// same sentinel as a missing row, so a caller cannot tell which backend held
// the payload — nor needs to.
func (s *PostgresStore) GetStoredFile(ctx context.Context, key string) (StoredFile, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 StoredFile
		return zero0, schemaErr
	}
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
	query := `SELECT storage_key, data FROM ` + quoteIdent(schema) + `.file_storage WHERE storage_key = $1`
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

// DeleteStoredFile removes a payload. With an object store configured the blob
// is dropped there; otherwise the stored-file row is deleted. A key that names
// no payload, or one that is not a well-formed key, is a no-op: cleanup never
// fails on a blob that is already gone.
func (s *PostgresStore) DeleteStoredFile(ctx context.Context, key string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if err := ValidateStoredFileKey(key); err != nil {
		return nil
	}
	if s.objectStore != nil {
		if deleter, ok := s.objectStore.(objectDeleter); ok {
			return deleter.Delete(ctx, key)
		}
		return nil
	}
	query := `DELETE FROM ` + quoteIdent(schema) + `.file_storage WHERE storage_key = $1`
	_, err := s.db.ExecContext(ctx, query, key)
	return err
}

// objectDeleter is the optional object-store capability a stored-file removal
// uses. It is declared here, like prefixDeleter, so the storage area owns the
// shape it needs while the object-store package owns the protocol.
type objectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// OpenStoredFile opens a payload for streaming read. With an object store that
// can stream, the object body is handed straight to the caller so the first
// byte is written without the object being buffered. Otherwise — the row
// backend, or an object store without a streaming capability — the payload is
// loaded and wrapped, which keeps every backend working.
func (s *PostgresStore) OpenStoredFile(ctx context.Context, key string) (StoredFileStream, error) {
	if err := ValidateStoredFileKey(key); err != nil {
		return StoredFileStream{}, ErrStoredFileNotFound
	}
	if s.objectStore != nil {
		if streamer, ok := s.objectStore.(objectStreamer); ok {
			reader, contentType, size, err := streamer.OpenStream(ctx, key)
			if err != nil {
				if errors.Is(err, ErrObjectNotFound) {
					return StoredFileStream{}, ErrStoredFileNotFound
				}
				return StoredFileStream{}, err
			}
			// A stream always carries a reader, so the backend must report a
			// miss as an error rather than a nil reader; guard anyway so a
			// misbehaving implementation cannot hand back a nil body.
			if reader == nil {
				return StoredFileStream{}, ErrStoredFileNotFound
			}
			if contentType == "" {
				contentType = contentTypeForKey(key)
			}
			return StoredFileStream{Reader: reader, ContentType: contentType, Size: size}, nil
		}
	}
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

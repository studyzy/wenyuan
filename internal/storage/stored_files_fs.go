package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileStoreConfig configures the filesystem-backed stored-file location used
// by the PostgreSQL backend.
type FileStoreConfig struct {
	// Root is the directory payloads are written under. An empty root
	// disables the stored-file facility, and every read then reports
	// not-found.
	Root string
	// MaxBytes bounds a single payload. Zero means the caller's own bound
	// applies.
	MaxBytes int64
}

// FileStoredFiles is a filesystem-backed stored-file implementation. The
// PostgreSQL backend uses it because the existing schema has no blob column
// and this unit must not change the schema: payload bytes live on disk while
// the operation queue keeps only the key.
type FileStoredFiles struct {
	root     string
	maxBytes int64
}

// NewFileStoredFiles builds a filesystem-backed store rooted at cfg.Root.
func NewFileStoredFiles(cfg FileStoreConfig) *FileStoredFiles {
	return &FileStoredFiles{root: cfg.Root, maxBytes: cfg.MaxBytes}
}

// PutStoredFile writes a payload under its key.
func (f *FileStoredFiles) PutStoredFile(ctx context.Context, file StoredFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateStoredFileKey(file.Key); err != nil {
		return err
	}
	if f.root == "" {
		return fmt.Errorf("stored file root is not configured")
	}
	if f.maxBytes > 0 && int64(len(file.Data)) > f.maxBytes {
		return fmt.Errorf("stored file exceeds the configured size bound")
	}
	path, err := f.resolve(file.Key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Write to a sibling temporary file and rename, so a reader never observes
	// a partially written payload.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".staged-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(file.Data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// GetStoredFile reads a payload by key.
func (f *FileStoredFiles) GetStoredFile(ctx context.Context, key string) (StoredFile, error) {
	if err := ctx.Err(); err != nil {
		return StoredFile{}, err
	}
	if err := ValidateStoredFileKey(key); err != nil {
		return StoredFile{}, ErrStoredFileNotFound
	}
	path, err := f.resolve(key)
	if err != nil {
		return StoredFile{}, ErrStoredFileNotFound
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoredFile{}, ErrStoredFileNotFound
		}
		// A key that escapes the root or is unreadable must be
		// indistinguishable from an absent one.
		return StoredFile{}, ErrStoredFileNotFound
	}
	return StoredFile{Key: key, Data: data, ContentType: contentTypeForKey(key)}, nil
}

// resolve maps a key to an absolute path and refuses any path that escapes the
// configured root, so a crafted key cannot read or write outside it.
func (f *FileStoredFiles) resolve(key string) (string, error) {
	if f.root == "" {
		return "", fmt.Errorf("stored file root is not configured")
	}
	// Segment-wise containment: a parent-directory segment already fails
	// ValidateStoredFileKey, and the joined path is re-checked against the
	// cleaned root so an encoded separator cannot escape either.
	joined := filepath.Join(f.root, filepath.FromSlash(key))
	rootClean := filepath.Clean(f.root)
	joinedClean := filepath.Clean(joined)
	if joinedClean != rootClean && !strings.HasPrefix(joinedClean, rootClean+string(os.PathSeparator)) {
		return "", ErrInvalidStoredFileKey
	}
	return joinedClean, nil
}

// contentTypeForKey infers the media type a stored archive is served with.
func contentTypeForKey(key string) string {
	if strings.HasSuffix(key, ".zip") {
		return "application/zip"
	}
	return "application/octet-stream"
}

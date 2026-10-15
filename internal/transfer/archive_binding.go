package transfer

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
)

// maxArchiveUploadBytes bounds a single archive upload. A larger body is
// rejected before any parsing work happens.
const maxArchiveUploadBytes = 64 << 20

// Archive preview and apply. The archive format itself is owned by the
// archive area; this binder only drives it.

// ParseArchive parses an archive payload. It is exported so the worker reuses
// exactly the same zip handling the HTTP routes validate with, instead of
// duplicating it.
func ParseArchive(data []byte) (*hindsightarchive.Archive, error) {
	return validateArchive(data)
}

// validateArchive parses an uploaded archive synchronously so a malformed
// payload or an unsupported schema version fails as a caller error before any
// background work is queued.
func validateArchive(data []byte) (*hindsightarchive.Archive, error) {
	if int64(len(data)) > maxArchiveUploadBytes {
		return nil, fmt.Errorf("archive exceeds the %d byte upload bound", maxArchiveUploadBytes)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("archive is not a readable zip: %w", err)
	}
	archive, err := hindsightarchive.Read(reader)
	if err != nil {
		return nil, err
	}
	return archive, nil
}

// ArchiveReport is the observable result of validating or applying one
// archive.
type ArchiveReport struct {
	BankID   string
	Mode     string
	Document int
	Chunk    int
	Fact     int
	Entity   int
	Link     int
	DryRun   bool
}

// readAllBounded reads a body with a hard ceiling so a hostile upload cannot
// exhaust memory.
func readAllBounded(r io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body exceeds the %d byte bound", limit)
	}
	return data, nil
}

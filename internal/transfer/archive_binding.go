package transfer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
)

// Archive preview and apply. The archive format itself is owned by the
// archive area; this binder only drives it.

// The two names, in the reference's own words, of how a caller obtains a valid
// archive. The name is served verbatim in a malformed-archive error, so each
// endpoint uses its own and never borrows the other's.
const (
	producedByDocumentExport = "the document export endpoint"
	producedByBankExport     = "the bank export endpoint"
)

// ParseArchive parses an archive payload. It is exported so the worker and the
// CLI reuse exactly the zip handling the HTTP routes validate with, instead of
// duplicating it. Its malformed-archive error names the bank export endpoint,
// which is the whole-bank path both callers drive.
func ParseArchive(data []byte) (*hindsightarchive.Archive, error) {
	return validateArchive(data, producedByBankExport)
}

// validateArchive parses an uploaded archive synchronously so a malformed
// payload or an unsupported schema version fails as a caller error before any
// background work is queued.
//
// A non-zip body and a zip without a manifest.json carry the reference's own
// wording: the reference opens the zip and reports Python's BadZipFile text for
// the first, and names the producing endpoint for the second. producedBy is
// that name, so a documents import and a whole-bank restore each say which
// archive they accept.
//
// There is no upload-size bound here: the reference reads the whole body with no
// ceiling, so a larger-than-64-MiB archive is a caller the reference accepts and
// this route must accept too. The multipart parser spills the body to disk, so
// the only copy held in memory is the one the archive reader makes.
func validateArchive(data []byte, producedBy string) (*hindsightarchive.Archive, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		//nolint:staticcheck // the reference's own wording, capitalised
		return nil, errors.New(
			"Invalid transfer archive: the uploaded file is not a readable .zip (File is not a zip file)")
	}
	archive, err := hindsightarchive.Read(reader)
	if err != nil {
		if errors.Is(err, hindsightarchive.ErrManifestMissing) {
			//nolint:staticcheck // the reference's own wording, capitalised and sentence-final
			return nil, fmt.Errorf("Invalid transfer archive: manifest.json is missing. "+
				"This endpoint only accepts a .zip produced by %s — it is not a way to upload a zip of ordinary "+
				"files (PDF, text, Markdown). Use the file upload / retain endpoint for those.", producedBy)
		}
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

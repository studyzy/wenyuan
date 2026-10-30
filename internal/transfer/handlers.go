package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// multipartMemoryBound is how much of a multipart upload is held in memory
// before spilling to a temporary file.
const multipartMemoryBound = 8 << 20

// Handlers serves the archive transfer route family.
type Handlers struct {
	Store storage.Store
	// DocumentExportEnabled gates the document export routes.
	DocumentExportEnabled bool
	// DocumentImportEnabled gates the document import routes.
	DocumentImportEnabled bool
	// ImportGate answers the deployment's import gate, or nil when it carries
	// none: it sees every document of an archive before an import is queued and
	// may refuse it. It is asked per submission rather than held, so a gate the
	// deployment registers after composition is honoured (a restored archive
	// would otherwise bypass it). nil lets every import through, which is the
	// unextended behaviour.
	ImportGate func() httpx.ImportGate
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// NewHandlers builds the transfer handlers over the existing store.
func NewHandlers(store storage.Store, exportEnabled, importEnabled bool) *Handlers {
	return &Handlers{
		Store:                 store,
		DocumentExportEnabled: exportEnabled,
		DocumentImportEnabled: importEnabled,
		Now:                   func() time.Time { return time.Now().UTC() },
	}
}

// operationAccepted is the exact body the asynchronous submit routes return.
// It carries only the operation id and its status.
type operationAccepted struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
}

// scope is the tri-state section selector the export and restore routes take.
type scope struct {
	Data       bool
	BankConfig bool
	History    bool
	// Present records which include flags the caller actually supplied, so an
	// intersection with what the archive carries can be computed.
	Present map[string]bool
}

// submitError is the caller-error shape the submit routes report.
func (h *Handlers) submitRejected(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, detail))
}

// newOperationID builds an opaque operation identifier.
//
// The reference declares the operation key as uuid, so the value is laid out in
// the canonical 8-4-4-4-12 form with the version and variant bits set; a
// prefixed hex string would not be storable.
func newOperationID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(raw[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// recordOperation persists an accepted asynchronous operation. The payload
// carries only the storage key of a stashed upload, never the bytes. There is
// no consumer of this queue in this build, so the operation is accepted and
// recorded; delivery is explicitly out of scope.
func (h *Handlers) recordOperation(ctx context.Context, bankID, opType string, payload map[string]any) (storage.Operation, error) {
	return h.Store.PutOperation(ctx, storage.Operation{
		OperationID:   newOperationID(),
		BankID:        bankID,
		OperationType: opType,
		Status:        storage.StatusPending,
		Payload:       payload,
		CreatedAt:     h.Now(),
		UpdatedAt:     h.Now(),
	})
}

// writeAccepted renders the accepted submit response.
func writeAccepted(w http.ResponseWriter, r *http.Request, op storage.Operation) {
	status := string(op.Status)
	if strings.TrimSpace(status) == "" {
		status = string(storage.StatusPending)
	}
	httpx.WriteJSON(w, http.StatusAccepted, operationAccepted{OperationID: op.OperationID, Status: status})
}

// stashUpload persists an uploaded archive under a bank-scoped key and returns
// that key, so the operation payload carries a reference rather than bytes.
func (h *Handlers) stashUpload(ctx context.Context, bankID, name string, data []byte) (string, error) {
	key := storage.StoredFileKeyForBank(bankID, name)
	if err := h.Store.PutStoredFile(ctx, storage.StoredFile{
		Key:         key,
		ContentType: "application/zip",
		Data:        data,
	}); err != nil {
		return "", err
	}
	return key, nil
}

// errUploadFileMissing reports a request that carries no `file` part, or a body
// that is not multipart at all. The reference declares the archive as a
// required multipart field, so either case is a request-validation failure
// naming the missing field, not a caller error.
var errUploadFileMissing = errors.New("file is required")

// errMultipartUnparseable reports a multipart body the parser could not read.
// The reference reports the framework's own parse-error detail for this, which
// is a caller error rather than a validation failure.
//
//nolint:staticcheck // the reference's own wording, capitalised
var errMultipartUnparseable = errors.New("There was an error parsing the body")

// readMultipartArchive extracts the archive upload field from a multipart
// request. There is no size bound: the reference reads the whole upload with no
// ceiling, so neither does this, and the multipart parser already spills the
// body to disk beyond its in-memory allowance.
func readMultipartArchive(r *http.Request) ([]byte, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, errUploadFileMissing
	}
	if err := r.ParseMultipartForm(multipartMemoryBound); err != nil {
		return nil, errMultipartUnparseable
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, errUploadFileMissing
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

// readOptionalMultipartArchive returns the archive carried by the request's
// `file` part. The part is required, so a request without one reports
// errUploadFileMissing rather than a nil payload.
func readOptionalMultipartArchive(r *http.Request) ([]byte, bool, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, false, errUploadFileMissing
	}
	if err := r.ParseMultipartForm(multipartMemoryBound); err != nil {
		return nil, false, errMultipartUnparseable
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, false, errUploadFileMissing
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	return data, true, err
}

// repeatingValues collects a repeatable field, preferring the multipart form
// and falling back to the query string.
func repeatingValues(r *http.Request, field string) []string {
	var values []string
	if r.MultipartForm != nil {
		values = append(values, r.MultipartForm.Value[field]...)
	}
	values = append(values, httpx.QueryValues(r, field)...)
	return values
}

// asHTTPError maps a domain error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "the target bank already exists")
	case errors.Is(err, storage.ErrDocumentConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "conflicting document")
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}

// drainBody closes a request body, ignoring a close error because the response

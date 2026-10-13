package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	Data        bool
	BankConfig  bool
	History     bool
	AnySelected bool
	// Present records which include flags the caller actually supplied, so an
	// intersection with what the archive carries can be computed.
	Present map[string]bool
}

// submitError is the caller-error shape the submit routes report.
func (h *Handlers) submitRejected(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, detail))
}

// newOperationID builds an opaque operation identifier.
func newOperationID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return "op-" + hex.EncodeToString(raw[:])
}

// recordOperation persists an accepted asynchronous operation. The payload
// carries only the storage key of a stashed upload, never the bytes. There is
// no consumer of this queue in this build, so the operation is accepted and
// recorded; delivery is explicitly out of scope.
func (h *Handlers) recordOperation(ctx context.Context, bankID, opType string, payload map[string]any) (storage.Operation, error) {
	return h.Store.PutOperation(ctx, storage.Operation{
		ID:            newOperationID(),
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
	httpx.WriteJSONFor(w, r, http.StatusAccepted, operationAccepted{OperationID: op.ID, Status: status})
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

// readMultipartArchive extracts the archive upload field from a multipart
// request, enforcing the size bound before any parsing work.
func readMultipartArchive(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(multipartMemoryBound); err != nil {
		return nil, fmt.Errorf("request body must be a multipart upload: %w", err)
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, errors.New("multipart upload must carry a file field")
	}
	defer func() { _ = file.Close() }()
	return readAllBounded(file, maxArchiveUploadBytes)
}

// readOptionalMultipartArchive returns the archive when a file field is
// present and reports whether one was supplied.
func readOptionalMultipartArchive(r *http.Request) ([]byte, bool, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, false, nil
	}
	if err := r.ParseMultipartForm(multipartMemoryBound); err != nil {
		return nil, false, fmt.Errorf("request body must be a multipart upload: %w", err)
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, false, nil
	}
	defer func() { _ = file.Close() }()
	data, err := readAllBounded(file, maxArchiveUploadBytes)
	return data, true, err
}

// repeatingValues collects a repeatable field, preferring the multipart form
// and falling back to the query string.
func repeatingValues(r *http.Request, field string) []string {
	var values []string
	if r.MultipartForm != nil {
		values = append(values, r.MultipartForm.Value[field]...)
	}
	values = append(values, r.URL.Query()[field]...)
	return values
}

// asHTTPError maps a domain error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "bank not found")
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "the target bank already exists")
	case errors.Is(err, storage.ErrDocumentConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "conflicting document")
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	}
}

// drainBody closes a request body, ignoring a close error because the response

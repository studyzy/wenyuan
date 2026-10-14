//go:build pgx

package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// postgresTransferURL returns the database URL for the transfer end-to-end
// test. The test is skipped when it is not configured, so the default gate
// stays offline; it must be RUN explicitly rather than assumed.
func postgresTransferURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL end-to-end test was not run")
	}
	return url
}

// test-transfer-postgres-end-to-end: the archive upload, its stashed payload,
// and the recorded operation all survive a real PostgreSQL round trip. This
// exercises the postgres backend's stored-file delegation, which the
// memory-backend tests cannot cover.
func TestTransferPostgresEndToEnd(t *testing.T) {
	url := postgresTransferURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("transfer_e2e_%d", time.Now().UnixNano())

	backend := &storage.PostgresBackend{}
	opened, err := backend.Open(ctx, storage.BackendConfig{
		DatabaseURL: url,
		Schema:      schema,
	})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	store := opened
	// Drop the schema and close the pool from one registered cleanup, in that
	// order. A deferred Close would run before any cleanup, so the drop would
	// execute against a closed pool and leave the schema behind.
	t.Cleanup(func() {
		if dropper, ok := opened.(interface{ DropSchemaForTest(string) error }); ok {
			_ = dropper.DropSchemaForTest(schema)
		}
		if closer, ok := opened.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}

	const bankID = "e2e-bank"
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatalf("create bank: %v", err)
	}

	h := NewHandlers(store, true, true)
	data := archiveBytes(t, bankID)

	// The archive is accepted, validated, stashed, and recorded.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := routeRequest(http.MethodPost, "/v1/default/banks/"+bankID+"/transfer/import?mode=replace&include_data=true", &body,
		map[string]string{"bank_id": bankID})
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}

	// The operation must be readable back from the database.
	op, err := store.GetOperation(ctx, accepted.OperationID)
	if err != nil {
		t.Fatalf("operation must persist in postgres: %v", err)
	}
	if op.BankID != bankID || op.Status != storage.StatusPending {
		t.Fatalf("operation = %+v", op)
	}

	// The payload must carry only the stored key, and the bytes must be
	// readable through the postgres backend's stored-file delegation.
	key, ok := op.Payload[storage.OperationPayloadKeyStoredFile].(string)
	if !ok || key == "" {
		t.Fatalf("payload must carry the stored key, got %v", op.Payload)
	}
	file, err := store.GetStoredFile(ctx, key)
	if err != nil {
		t.Fatalf("stored file must be readable: %v", err)
	}
	if !bytes.Equal(file.Data, data) {
		t.Fatalf("stored payload differs from the upload (%d vs %d bytes)", len(file.Data), len(data))
	}

	// A traversal key must not resolve, even against real storage.
	if _, err := store.GetStoredFile(ctx, "banks/"+bankID+"/../../etc/passwd"); err == nil {
		t.Fatal("a traversal key must not resolve")
	}

	// The download route must serve the stashed archive over the real backend.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+key, nil)
	h.ServeDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.Len() != len(data) {
		t.Fatalf("downloaded %d bytes, want %d", rec.Body.Len(), len(data))
	}
}

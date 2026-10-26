package app

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/storage"
)

// uploadFileRetain posts one file to the served retain route and returns the
// response.
// fileRetainDocument is the document every file-retain upload in these tests
// declares, so the attachment row's document id is known up front.
const fileRetainDocument = "fr-doc"

func uploadFileRetain(t *testing.T, application *App, filename, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="files"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	// The document the upload is retained as, which is also the id its
	// attachment row is filed under.
	if err := writer.WriteField("request", `{"files_metadata":[{"document_id":"`+fileRetainDocument+`"}]}`); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/fr-bank/files/retain", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)
	return rec
}

// drainUntilSettled runs the real consumer until no queued operation is left
// pending, then stops it: the file route enqueues its work, so the attachment
// row and the stored bytes appear when the worker runs, not when the request
// returns.
func drainUntilSettled(t *testing.T, store storage.Store, bankID string) {
	t.Helper()
	ctx := context.Background()
	consumer := NewQueueConsumer(store, none.New(), nil, nil, config.WorkerConfig{
		ID: "w-file", MaxRetries: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		Lease: time.Minute, Concurrency: 1, PollInterval: time.Millisecond,
	}, config.WorkerTuning{}, nil, ExtractionSettings{}, false, "public")
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(runCtx) }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: bankID, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		pending := 0
		for _, op := range page.Operations {
			if !op.Status.Terminal() {
				pending++
			}
		}
		if pending == 0 && len(page.Operations) > 0 {
			for _, op := range page.Operations {
				if op.Status == storage.StatusFailed {
					t.Logf("operation %s failed: %+v", op.OperationID, op)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the queued file retain did not settle")
}

// fileRetainApp composes the served application with one file-retain setting,
// which is deployment configuration rather than request state.
func fileRetainApp(t *testing.T, deleteAfterRetain bool) *App {
	t.Helper()
	cfg := testConfig()
	cfg.FileDeleteAfterRetain = deleteAfterRetain
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Store.CreateBank(context.Background(), storage.Bank{BankID: "fr-bank", Name: "fr-bank"}); err != nil {
		t.Fatal(err)
	}
	return application
}

// The reference's file-conversion task records the upload's file metadata
// (`_file_metadata`: storage key, original name, content type) but writes no
// attachment row — verified against the live reference, where a converted upload
// leaves the document reading `attachments: null` and its attachments table
// empty. With delete-after-retain on, the bytes are gone after the conversion and
// no attachment answers for them either.
func TestFileRetainDeleteAfterRetainDropsBytes(t *testing.T) {
	application := fileRetainApp(t, true)
	rec := uploadFileRetain(t, application, "notes.txt", "the prose", "text/plain")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drainUntilSettled(t, application.Store, "fr-bank")

	attachments, err := application.Store.ListAttachments(context.Background(), "fr-bank", fileRetainDocument)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("a converted upload kept %d attachment row(s), want none: %+v", len(attachments), attachments)
	}
}

// With delete-after-retain off, a converted upload still writes no attachment row:
// the text is the document's content, and the bytes the route kept are reachable
// only through the stored-file registry, exactly as the reference leaves them.
func TestFileRetainConvertedUploadWritesNoAttachment(t *testing.T) {
	application := fileRetainApp(t, false)
	rec := uploadFileRetain(t, application, "notes.txt", "the prose", "text/plain")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drainUntilSettled(t, application.Store, "fr-bank")

	document, err := application.Store.GetDocument(context.Background(), "fr-bank", fileRetainDocument)
	if err != nil {
		t.Fatalf("the upload stored no document: %v", err)
	}
	if document.OriginalText != "the prose" {
		t.Fatalf("document text = %q, want the converted body", document.OriginalText)
	}
	attachments, err := application.Store.ListAttachments(context.Background(), "fr-bank", fileRetainDocument)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("a converted upload kept %d attachment row(s), want none: %+v", len(attachments), attachments)
	}
}

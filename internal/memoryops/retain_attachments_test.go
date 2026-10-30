package memoryops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// B1.14: a content block carrying an inline attachment renders as the canonical
// bracketed placeholder, not as the literal "[image]" or the raw bytes.
func TestContentFlattenRendersAttachmentPlaceholder(t *testing.T) {
	raw := []byte("screenshot bytes")
	data := base64.StdEncoding.EncodeToString(raw)
	shortID := AttachmentShortID(attachmentHashOf(raw))
	body := `[{"type":"text","text":"before"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}},{"type":"text","text":"after"}]`

	var content Content
	if err := json.Unmarshal([]byte(body), &content); err != nil {
		t.Fatal(err)
	}
	want := "before\n\n" + AttachmentPlaceholder(shortID) + "\n\nafter"
	if got := content.Flatten(); got != want {
		t.Fatalf("flatten = %q, want %q", got, want)
	}
}

// B1.14: the placeholder is stable across retains of identical content, because
// it is derived from the content digest rather than minted per call.
func TestAttachmentPlaceholderStableAcrossRetains(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("same bytes"))
	body := `[{"type":"image","source":{"type":"base64","data":"` + data + `"}}]`

	var first, second Content
	if err := json.Unmarshal([]byte(body), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &second); err != nil {
		t.Fatal(err)
	}
	if first.Flatten() != second.Flatten() {
		t.Fatalf("placeholder not stable: %q != %q", first.Flatten(), second.Flatten())
	}
	if !strings.HasPrefix(first.Flatten(), placeholderOpen+placeholderBody) {
		t.Fatalf("flatten = %q, want a placeholder", first.Flatten())
	}
}

// B1.14: a text block keeps its text, and an image block with no resolvable
// attachment keeps the pre-existing "[image]" rendering.
func TestContentFlattenKeepsTextAndFallsBackWithoutShortID(t *testing.T) {
	content := Content{IsList: true, Blocks: []ContentBlock{
		{Type: "text", Text: "prose"},
		{Type: "image"},
		{Type: "image", Source: &AttachmentSource{Data: "not-base64!!"}},
	}}
	want := "prose\n\n[image]\n\n[image]"
	if got := content.Flatten(); got != want {
		t.Fatalf("flatten = %q, want %q", got, want)
	}
}

// B1.14: an image uploaded through the file route is stored as an attachment
// and referenced from the retained text by its placeholder.
func TestFileRetainImageUploadUsesPlaceholder(t *testing.T) {
	mux, store := newServerWithVision(t)
	rec := uploadFile(t, mux, "pic.png", "png bytes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drainQueuedFileRetains(t, store)

	wantText := AttachmentPlaceholder(AttachmentShortID(attachmentHashOf([]byte("png bytes"))))
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range page.Memories {
		if unit.Text == wantText {
			found = true
		}
	}
	if !found {
		t.Fatalf("no memory carried the placeholder %q: %+v", wantText, page.Memories)
	}
}

// B1.16: a document the build can convert is turned into markdown and its text
// reaches the store, rather than being refused.
func TestFileRetainConvertsDocumentUpload(t *testing.T) {
	pdf, err := os.ReadFile(filepath.Join("..", "fileparse", "testdata", "sample.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	mux, store := newServer(t)
	rec := uploadFile(t, mux, "report.pdf", string(pdf), "application/pdf")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	drainQueuedFileRetains(t, store)
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range page.Memories {
		if strings.Contains(unit.Text, "sample document") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the converted PDF text did not reach the store: %+v", page.Memories)
	}
}

// B1.16: a document format no configured parser can read is refused with a 4xx
// naming both the format and the file, and nothing is written.
func TestFileRetainRefusesUnconvertibleFormat(t *testing.T) {
	mux, store := newServer(t)
	rec := uploadFile(t, mux, "legacy.doc", "\xd0\xcf\x11\xe0 binary", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Word") || !strings.Contains(body, "legacy.doc") {
		t.Fatalf("the refusal must name the format and the file: %s", body)
	}
	attachments, err := store.ListAttachments(context.TODO(), "b1", "legacy.doc")
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 0 {
		t.Fatalf("refused upload must not be stored: %+v", attachments)
	}
}

// B1.16: a text-like upload keeps working and its text reaches the store.
func TestFileRetainAcceptsTextUpload(t *testing.T) {
	mux, store := newServer(t)
	rec := uploadFile(t, mux, "notes.md", "plain text body", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drainQueuedFileRetains(t, store)
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range page.Memories {
		if strings.Contains(unit.Text, "plain text body") {
			found = true
		}
	}
	if !found {
		t.Fatalf("text upload was not retained: %+v", page.Memories)
	}
}

// uploadFile posts one file to the file route with an optional content type.
func uploadFile(t *testing.T, mux *http.ServeMux, filename, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	if contentType == "" {
		contentType = "application/octet-stream"
	}
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
	// The `request` part is required by the route, so the helper always sends
	// the empty object.
	if err := writer.WriteField("request", "{}"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

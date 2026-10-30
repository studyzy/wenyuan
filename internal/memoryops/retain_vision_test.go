package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference's canonicalize_item_content drops every attachment block and
// keeps only the text when the model that would read the attachment cannot read
// images — the retain still succeeds, it just stores the prose. Answering 422
// (this build's earlier behaviour) refuses a request the reference accepts.
func TestRetainWithoutVisionStoresTheProseOnly(t *testing.T) {
	mux, store := newServer(t)
	body := `{"items":[{"document_id":"d1","content":[` +
		`{"type":"text","text":"before"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},` +
		`{"type":"text","text":"after"}]}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	document, err := store.GetDocument(context.TODO(), "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	// The attachment's position survives as the paragraph break the block
	// occupied, so the prose reads in order.
	if want := "before\n\nafter"; document.OriginalText != want {
		t.Fatalf("original_text = %q, want %q", document.OriginalText, want)
	}
	attachments, err := store.ListAttachments(context.TODO(), "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 0 {
		t.Fatalf("attachments = %d, want none: bytes the write cannot resolve must not be stored",
			len(attachments))
	}
}

// The same request against a model that reads images keeps the placeholder, so
// the picture is handed to the model where the caller put it.
func TestRetainWithVisionKeepsThePlaceholder(t *testing.T) {
	mux, store := newServerWithVision(t)
	body := `{"items":[{"document_id":"d1","content":[` +
		`{"type":"text","text":"before"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},` +
		`{"type":"text","text":"after"}]}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	document, err := store.GetDocument(context.TODO(), "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	shortID := AttachmentShortID(attachmentHashOf([]byte("hello")))
	if want := "before\n\n" + AttachmentPlaceholder(shortID) + "\n\nafter"; document.OriginalText != want {
		t.Fatalf("original_text = %q, want %q", document.OriginalText, want)
	}
}

// The choice reaches the stored units, not only the document: a text-only
// deployment stores the prose as the memory, never the placeholder token.
func TestRetainWithoutVisionStoresProseUnits(t *testing.T) {
	mux, store := newServer(t)
	body := `{"items":[{"content":[{"type":"text","text":"a red square"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
	if rec := post(t, mux, "/v1/default/banks/b1/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range page.Memories {
		if strings.Contains(unit.Text, placeholderBody) {
			t.Fatalf("unit text still carries a placeholder token: %q", unit.Text)
		}
	}
	if len(page.Memories) != 1 {
		t.Fatalf("units = %d, want 1", len(page.Memories))
	}
	if got := page.Memories[0].Text; got != "a red square" {
		t.Fatalf("unit text = %q, want the prose alone", got)
	}
}

// An uploaded image whose model cannot read it keeps its document text as the
// prose the upload would have been, and stores no attachment row.
func TestImageUploadWithoutVisionDropsTheAttachment(t *testing.T) {
	mux, store := newServer(t)
	if rec := uploadFile(t, mux, "pic.png", "png bytes", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drainQueuedFileRetains(t, store)
	docs, err := store.ListDocuments(context.TODO(), storage.DocumentQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs.Documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(docs.Documents))
	}
	if docs.Documents[0].OriginalText != "" {
		t.Fatalf("original_text = %q, want the empty prose", docs.Documents[0].OriginalText)
	}
	attachments, err := store.ListAttachments(context.TODO(), "b1", docs.Documents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 0 {
		t.Fatalf("attachments = %d, want none: bytes no write could resolve are an orphan", len(attachments))
	}
}

// A canonical document text keeps its placeholder for a model that reads
// images: the bytes ride on the write, so the picture is handed over where the
// placeholder stands.
func TestCanonicalTextKeepsItsOwnPlaceholder(t *testing.T) {
	shortID := "d013614dc14a"
	item := RetainItem{
		Content:         Content{Text: "an article " + AttachmentPlaceholder(shortID)},
		AttachmentIDs:   []string{shortID},
		textIsCanonical: true,
		visionEnabled:   true,
	}
	if got, want := canonicalItemText(item), "an article "+AttachmentPlaceholder(shortID); got != want {
		t.Fatalf("canonical text = %q, want %q", got, want)
	}
}

// A flat text naming an attachment the write does not carry is authored
// placeholder text, so a text-only model reads the prose without it.
func TestCanonicalTextDropsAForeignPlaceholder(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"items": []map[string]any{{"content": "look at " + AttachmentPlaceholder("0123456789ab") + " this"}},
	})
	var req RetainRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	item := RetainItem{Content: req.Items[0].Content, textIsCanonical: true}
	if got, want := canonicalItemText(item), "look at  this"; got != want {
		t.Fatalf("canonical text = %q, want %q", got, want)
	}
}

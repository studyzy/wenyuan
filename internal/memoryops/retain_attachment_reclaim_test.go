package memoryops

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// imageItemBody is a one-item retain whose content carries one inline image, the
// form a refusal has to take back out of the bank afterwards.
func imageItemBody(documentID, tags string) string {
	tagsField := ""
	if tags != "" {
		tagsField = `,"tags":` + tags
	}
	return `{"items":[{"document_id":"` + documentID + `",` + `"content":[` +
		`{"type":"text","text":"before"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}` +
		`]` + tagsField + `}]}`
}

// assertNoAttachments reports whether the document kept any attachment row, and
// which short ids they carry.
func attachmentShortIDs(t *testing.T, store *storage.MemoryStore, bankID, documentID string) []string {
	t.Helper()
	attachments, err := store.ListAttachments(context.TODO(), bankID, documentID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		ids = append(ids, attachment.ShortID)
	}
	return ids
}

// A write refused after its bytes were stored at ingress takes those bytes back:
// nothing else reclaims an attachment no retain ever referenced, so the reference
// discards them on the way out (engine/memory_engine.py:6953, :23290 — the
// write-scope check moved inside that block in v1.1.0, #5416).
func TestRefusedRetainDiscardsItsIngressAttachments(t *testing.T) {
	mux, store := newServerWithVision(t)
	rec := postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		imageItemBody("d1", `["team:b"]`), []string{"team:a"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if ids := attachmentShortIDs(t, store, "b1", "d1"); len(ids) != 0 {
		t.Fatalf("attachments survived the refusal: %v", ids)
	}
	// The blob went with the row, so a later fetch has nothing to resolve.
	if _, err := store.GetAttachment(context.TODO(), "b1", "d1"); err == nil {
		t.Fatal("an attachment row survived a refused retain")
	}
}

// A refusal reaches only the documents the request named: a write that was
// accepted first keeps its attachments.
func TestRefusedRetainKeepsAnotherRequestsAttachments(t *testing.T) {
	mux, store := newServerWithVision(t)
	accepted := post(t, mux, "/v1/default/banks/b1/memories", imageItemBody("kept", ""))
	if accepted.Code != http.StatusOK {
		t.Fatalf("accepted write = %d: %s", accepted.Code, accepted.Body.String())
	}
	rec := postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		imageItemBody("refused", `["team:b"]`), []string{"team:a"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if ids := attachmentShortIDs(t, store, "b1", "kept"); len(ids) == 0 {
		t.Fatal("the refusal took the attachments of a document it never named")
	}
	if ids := attachmentShortIDs(t, store, "b1", "refused"); len(ids) != 0 {
		t.Fatalf("attachments survived the refusal: %v", ids)
	}
}

// An item that carries attachments but names no document gets the reference's
// `retain_<uuid4>` id, and it is the id its attachment rows are stored under
// (api/http.py:10346-10349). An item whose canonical text keeps only the
// placeholder has nothing a document row can hold, so the row is not written —
// but the id its attachments already live under must still be that one.
func TestAttachmentOnlyItemGetsARetainPrefixedDocumentID(t *testing.T) {
	mux, store := newServerWithVision(t)
	body := `{"items":[{"content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	attachments, err := store.AttachmentsByShortIDs(context.TODO(), "b1", []string{AttachmentShortID(attachmentHashOf([]byte("hello")))})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("attachments = %d, want the one the ingress stored", len(attachments))
	}
	for _, attachment := range attachments {
		documentID := attachment.DocumentID
		if !strings.HasPrefix(documentID, "retain_") {
			t.Fatalf("document id = %q, want the reference's retain_ prefix", documentID)
		}
		// The suffix is a bare uuid, as `f"retain_{uuid.uuid4()}"` produces.
		if suffix := strings.TrimPrefix(documentID, "retain_"); len(suffix) != 36 {
			t.Fatalf("document id suffix = %q, want a canonical uuid", suffix)
		}
	}
}

// A caller-named document keeps its own id; only a minted one carries the prefix.
func TestNamedDocumentKeepsItsIDWithAttachments(t *testing.T) {
	mux, store := newServerWithVision(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", imageItemBody("chosen", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetDocument(context.TODO(), "b1", "chosen"); err != nil {
		t.Fatalf("document under the caller's id: %v", err)
	}
}

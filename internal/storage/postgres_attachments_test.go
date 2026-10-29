//go:build pgx

package storage

import (
	"context"
	"testing"
)

// test-attachments-persist: an attachment row survives a real round trip and
// the unit that came from it names the attachment. Before the attachments
// table existed the bytes had nowhere to be recorded, so a recall could not
// say which attachment a fact came from.
func TestAttachmentsPersistAndLinkToUnit(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	key := AttachmentStorageKey("vec-bank", "doc-1", "deadbeef")
	attachment := Attachment{
		BankID:         "vec-bank",
		DocumentID:     "doc-1",
		AttachmentHash: "deadbeef",
		ShortID:        "deadbeef0123",
		MediaType:      "image/png",
		ByteSize:       9,
		StorageKey:     key,
		Kind:           "file",
		Filename:       "diagram.png",
	}
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, ContentType: "image/png", Data: []byte("png bytes")}); err != nil {
		t.Fatalf("put stored file: %v", err)
	}
	if err := store.PutAttachment(ctx, attachment); err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	// A second write of the same content must not duplicate the row.
	if err := store.PutAttachment(ctx, attachment); err != nil {
		t.Fatalf("repeat put attachment: %v", err)
	}

	stored, err := store.ListAttachments(ctx, "vec-bank", "doc-1")
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("attachments = %d, want 1", len(stored))
	}
	if stored[0].ShortID != "deadbeef0123" || stored[0].Filename != "diagram.png" {
		t.Fatalf("attachment = %+v, want short id and filename preserved", stored[0])
	}

	unit := MemoryUnit{
		ID: pgUUID("att-unit"), BankID: "vec-bank", Text: "a diagram", FactType: TypeExperience,
		AttachmentIDs: []string{"deadbeef0123"},
	}
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatalf("upsert unit: %v", err)
	}
	var ids string
	query := `SELECT attachment_ids::text FROM ` + quoteIdent(store.(*PostgresStore).defaultSchema) + `.memory_units WHERE id = $1`
	if err := store.(*PostgresStore).db.QueryRowContext(ctx, query, pgUUID("att-unit")).Scan(&ids); err != nil {
		t.Fatalf("read attachment_ids: %v", err)
	}
	if ids != `{deadbeef0123}` {
		t.Fatalf("attachment_ids = %s, want {deadbeef0123}", ids)
	}
}

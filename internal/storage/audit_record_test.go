package storage

import (
	"context"
	"testing"
)

// RecordAuditLog appends an entry the listing reads back, defaulting the
// identifier and start time so a caller only supplies what it knows.
func TestRecordAuditLogRoundTrip(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if err := store.RecordAuditLog(ctx, AuditEntry{
		Action:    "retain",
		Transport: "http",
		BankID:    "b1",
		Request:   map[string]any{"items": []any{"x"}},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListAuditLog(ctx, AuditQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(page.Entries))
	}
	entry := page.Entries[0]
	if entry.ID == "" {
		t.Errorf("identifier not defaulted")
	}
	if entry.StartedAt.IsZero() {
		t.Errorf("start time not defaulted")
	}
	if entry.Action != "retain" || entry.Transport != "http" {
		t.Errorf("entry = %+v", entry)
	}
}

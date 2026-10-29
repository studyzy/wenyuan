//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/history"
)

// The PostgreSQL reader of the document change history: the containment test on
// `result_metadata->'document_events'`, the operation-ordered page, and the
// event-ordered list the page carries.

// openHistoryStore opens a migrated store in a schema of its own.
func openHistoryStore(t *testing.T) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the document history test was not run")
	}
	schema := fmt.Sprintf("dochist_%d", time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

// TestPostgresDocumentHistoryRoundTrip writes events onto real operations and
// reads them back: newest first across operations, ordered by each event's own
// created_at within one page, with the operation filled in on read.
func TestPostgresDocumentHistoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	const bank = "dochist-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "History"}); err != nil {
		t.Fatal(err)
	}
	// Two operations, the older carrying two events for the document.
	for _, events := range [][]history.Event{
		{history.NewEvent("doc-a", history.KindRetain), history.NewEvent("doc-a", history.KindConsolidation)},
		{history.NewEvent("doc-a", history.KindFactEdited)},
	} {
		// PutOperation stamps created_at itself, so the two operations are
		// separated in real time: the page orders by that column.
		time.Sleep(2 * time.Millisecond)
		op, err := store.PutOperation(ctx, Operation{
			BankID: bank, OperationType: OperationTypeConsolidate, Status: StatusCompleted,
		})
		if err != nil {
			t.Fatal(err)
		}
		history.AppendOperationEvents(ctx, store, op.OperationID, events...)
	}
	// A different document's event is not in this document's history.
	other, err := store.PutOperation(ctx, Operation{
		BankID: bank, OperationType: OperationTypeConsolidate, Status: StatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	history.AppendOperationEvents(ctx, store, other.OperationID, history.NewEvent("doc-b", history.KindRetain))

	page, err := store.ListDocumentEvents(ctx, bank, "doc-a", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("total = %d, want 2 operations", page.Total)
	}
	if len(page.Events) != 3 {
		t.Fatalf("events = %d, want the 3 of the two carrying operations", len(page.Events))
	}
	// Every event names the operation it was recorded on.
	for _, event := range page.Events {
		if event.OperationID == "" {
			t.Fatalf("event %v carries no operation", event.Members)
		}
	}
	// The page is bounded by operations, not events.
	first, err := store.ListDocumentEvents(ctx, bank, "doc-a", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 2 || len(first.Events) != 1 {
		t.Fatalf("a one-operation page = total %d with %d events, want 2 and the newer operation's 1",
			first.Total, len(first.Events))
	}
	if kind := first.Events[0].Members["kind"]; kind != string(history.KindFactEdited) {
		t.Fatalf("the page returned %v, want the newer operation's event", kind)
	}
	// A document with no events reads an empty page.
	empty, err := store.ListDocumentEvents(ctx, bank, "doc-none", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || len(empty.Events) != 0 {
		t.Fatalf("a quiet document read %+v", empty)
	}
}

// TestPostgresDocumentFactSourcesResolvesTheDocument pins the fact -> document
// read the consolidation attribution uses, including the invalidated view and a
// non-uuid id the driver could not cast.
func TestPostgresDocumentFactSourcesResolvesTheDocument(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	const bank = "factsrc-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "Facts"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-src"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	factID := outcome.ItemUnitIDs[0][0]
	documents, err := store.DocumentFactSources(ctx, bank, []string{factID, "not-a-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if documents[factID] != "doc-src" {
		t.Fatalf("document = %q, want doc-src (map %v)", documents[factID], documents)
	}
	if _, ok := documents["not-a-uuid"]; ok {
		t.Fatalf("a non-uuid id resolved to %v", documents)
	}
}

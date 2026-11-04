package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/storage"
)

// A processed operation records one system-transport row carrying the operation
// type as its action.
func TestConsumerAuditsProcessedOperations(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", "consolidation")

	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()}).
		WithAuditor(audit.NewLogger(store, true, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusCompleted)

	page, err := store.ListAuditLog(context.Background(), storage.AuditQuery{BankID: "bank-a", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("expected one audited row, got %d: %+v", len(page.Entries), page.Entries)
	}
	entry := page.Entries[0]
	if entry.Action != "consolidation" || entry.Transport != "system" {
		t.Fatalf("row = action %q transport %q, want consolidation/system", entry.Action, entry.Transport)
	}
}

// A failing operation is recorded, and its row carries no failure marker: the
// reference's writer has none, so metadata holds only what the caller supplied
// (engine/audit.py:224-243, :278-282).
//
// Falsifier: give audit.Entry an Error field again (internal/audit/audit.go) and
// set it from the failure (internal/worker/consumer.go) and the metadata
// assertion below fails.
func TestConsumerAuditsFailedOperations(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", "retain")

	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return Permanent(errors.New("input will never be accepted"))
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()}).
		WithAuditor(audit.NewLogger(store, true, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusFailed)

	page, err := store.ListAuditLog(context.Background(), storage.AuditQuery{BankID: "bank-a", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("expected one audited row, got %d: %+v", len(page.Entries), page.Entries)
	}
	if metadata := page.Entries[0].Metadata; metadata != nil {
		if _, present := metadata["error"]; present {
			t.Fatalf("failure row carries an error key the reference never writes: %+v", metadata)
		}
		if len(metadata) != 0 {
			t.Fatalf("failure row metadata = %+v, want empty", metadata)
		}
	}
}

// With auditing off, no row is recorded.
func TestConsumerAuditDisabledProducesNoRows(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", "retain")

	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()}).
		WithAuditor(audit.NewLogger(store, false, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusCompleted)

	page, err := store.ListAuditLog(context.Background(), storage.AuditQuery{BankID: "bank-a", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("expected no rows, got %+v", page.Entries)
	}
}

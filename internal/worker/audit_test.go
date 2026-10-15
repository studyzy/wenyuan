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

// A failing operation records the failure text.
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
	if text, _ := page.Entries[0].Metadata["error"].(string); text == "" {
		t.Fatalf("failure text not recorded: %+v", page.Entries[0].Metadata)
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
